package wallet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

const sendIdempotencyFile = "send-idempotency.json"

var (
	// ErrSendInProgress means an earlier attempt with this key may still have reached Houston.
	ErrSendInProgress = errors.New("send in progress")
	// ErrIdempotencyKeyReused means a key was used for a different destination or amount.
	ErrIdempotencyKeyReused = errors.New("idempotency key reused with different request")
	// ErrUnknownIdempotencyKey means no local persistent record exists for a key.
	ErrUnknownIdempotencyKey = errors.New("unknown idempotency key")
)

// SendIdempotencyState is the durable state of one keyed send attempt.
type SendIdempotencyState string

const (
	SendInFlight        SendIdempotencyState = "in_flight"
	SendDone            SendIdempotencyState = "done"
	SendFailedPreSubmit SendIdempotencyState = "failed_pre_submit"
)

// SendIdempotencyRecord is a persistent record used to prevent duplicate outgoing payments.
type SendIdempotencyRecord struct {
	Key         string               `json:"key"`
	RequestHash string               `json:"request_hash"`
	State       SendIdempotencyState `json:"state"`
	Result      json.RawMessage      `json:"result,omitempty"`
	RequestID   string               `json:"request_id"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

type sendIdempotencyFileContents struct {
	Version int                              `json:"version"`
	Records map[string]SendIdempotencyRecord `json:"records"`
}

// This namespace is deliberately fixed: a retry with the same key must generate the same Houston
// operation requestId even after the wrapper restarts.
var sendIdempotencyNamespace = uuid.MustParse("2c2a4b14-8cf7-5e43-92e7-7c474a3f5724")

func requestIDForIdempotencyKey(key string) string {
	return uuid.NewSHA1(sendIdempotencyNamespace, []byte(key)).String()
}

func sendRequestHash(request SendRequest) string {
	sum := sha256.Sum256([]byte(request.Destination + "|" + strconv.FormatInt(request.AmountSatoshis, 10)))
	return hex.EncodeToString(sum[:])
}

type sendIdempotencyStore struct {
	dataDir string
	mu      sync.Mutex
	loaded  bool
	records map[string]SendIdempotencyRecord
	active  map[string]bool
}

func newSendIdempotencyStore(dataDir string) *sendIdempotencyStore {
	return &sendIdempotencyStore{dataDir: dataDir}
}

func (s *sendIdempotencyStore) path() string {
	return filepath.Join(s.dataDir, sendIdempotencyFile)
}

func (s *sendIdempotencyStore) start(key, requestHash string) (SendIdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return SendIdempotencyRecord{}, false, err
	}
	if existing, found := s.records[key]; found {
		if existing.RequestHash != requestHash {
			return SendIdempotencyRecord{}, false, ErrIdempotencyKeyReused
		}
		switch existing.State {
		case SendInFlight:
			return SendIdempotencyRecord{}, false, ErrSendInProgress
		case SendDone:
			return existing, true, nil
		case SendFailedPreSubmit:
			record, replay, err := s.replaceLocked(key, func(record *SendIdempotencyRecord) {
				record.State = SendInFlight
				record.Result = nil
				record.UpdatedAt = time.Now().UTC()
			})
			if err == nil {
				s.active[key] = true
			}
			return record, replay, err
		default:
			return SendIdempotencyRecord{}, false, fmt.Errorf("invalid idempotency state %q", existing.State)
		}
	}

	now := time.Now().UTC()
	record := SendIdempotencyRecord{
		Key:         key,
		RequestHash: requestHash,
		State:       SendInFlight,
		RequestID:   requestIDForIdempotencyKey(key),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.writeRecordsLocked(withRecord(s.records, record)); err != nil {
		return SendIdempotencyRecord{}, false, err
	}
	s.records[key] = record
	s.active[key] = true
	return record, false, nil
}

func (s *sendIdempotencyStore) done(key string, result SendResult) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode idempotent send result: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	_, _, err = s.replaceLocked(key, func(record *SendIdempotencyRecord) {
		record.State = SendDone
		record.Result = encoded
		record.UpdatedAt = time.Now().UTC()
	})
	return err
}

func (s *sendIdempotencyStore) failedPreSubmit(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	_, _, err := s.replaceLocked(key, func(record *SendIdempotencyRecord) {
		record.State = SendFailedPreSubmit
		record.Result = nil
		record.UpdatedAt = time.Now().UTC()
	})
	return err
}

func (s *sendIdempotencyStore) get(key string) (SendIdempotencyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return SendIdempotencyRecord{}, err
	}
	record, found := s.records[key]
	if !found {
		return SendIdempotencyRecord{}, ErrUnknownIdempotencyKey
	}
	return record, nil
}

func (s *sendIdempotencyStore) activeSend(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return false, err
	}
	return s.active[key], nil
}

func (s *sendIdempotencyStore) deactivate(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		delete(s.active, key)
	}
}

func (s *sendIdempotencyStore) replaceLocked(key string, update func(*SendIdempotencyRecord)) (SendIdempotencyRecord, bool, error) {
	record, found := s.records[key]
	if !found {
		return SendIdempotencyRecord{}, false, ErrUnknownIdempotencyKey
	}
	update(&record)
	candidate := withRecord(s.records, record)
	if err := s.writeRecordsLocked(candidate); err != nil {
		return SendIdempotencyRecord{}, false, err
	}
	s.records[key] = record
	return record, false, nil
}

func (s *sendIdempotencyStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	contents, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		s.records = make(map[string]SendIdempotencyRecord)
		s.active = make(map[string]bool)
		s.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("read send idempotency store: %w", err)
	}
	var persisted sendIdempotencyFileContents
	if err := json.Unmarshal(contents, &persisted); err != nil {
		return fmt.Errorf("decode send idempotency store: %w", err)
	}
	if persisted.Version != 1 || persisted.Records == nil {
		return errors.New("invalid send idempotency store")
	}
	for key, record := range persisted.Records {
		if key == "" || record.Key != key || record.RequestHash == "" || record.RequestID == "" || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
			return errors.New("invalid send idempotency record")
		}
		switch record.State {
		case SendInFlight, SendFailedPreSubmit:
			if len(record.Result) != 0 {
				return errors.New("unfinished send idempotency record has a result")
			}
		case SendDone:
			var result SendResult
			if len(record.Result) == 0 || json.Unmarshal(record.Result, &result) != nil {
				return errors.New("completed send idempotency record has an invalid result")
			}
		default:
			return fmt.Errorf("invalid send idempotency state %q", record.State)
		}
	}
	s.records = persisted.Records
	s.active = make(map[string]bool)
	s.loaded = true
	return nil
}

func (s *sendIdempotencyStore) writeRecordsLocked(records map[string]SendIdempotencyRecord) error {
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return fmt.Errorf("create send idempotency directory: %w", err)
	}
	contents, err := json.MarshalIndent(sendIdempotencyFileContents{Version: 1, Records: records}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode send idempotency store: %w", err)
	}
	temporary, err := os.CreateTemp(s.dataDir, ".send-idempotency-*.json")
	if err != nil {
		return fmt.Errorf("create temporary send idempotency store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set send idempotency store permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write send idempotency store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync send idempotency store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close send idempotency store: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path()); err != nil {
		return fmt.Errorf("replace send idempotency store: %w", err)
	}
	directory, err := os.Open(s.dataDir)
	if err != nil {
		return fmt.Errorf("open send idempotency directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync send idempotency directory: %w", err)
	}
	return nil
}

func withRecord(records map[string]SendIdempotencyRecord, record SendIdempotencyRecord) map[string]SendIdempotencyRecord {
	candidate := make(map[string]SendIdempotencyRecord, len(records)+1)
	for key, value := range records {
		candidate[key] = value
	}
	candidate[record.Key] = record
	return candidate
}
