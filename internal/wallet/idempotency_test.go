package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdempotencyRequestIDIsStableUUIDv5(t *testing.T) {
	t.Parallel()
	first := requestIDForIdempotencyKey("withdrawal-42")
	second := requestIDForIdempotencyKey("withdrawal-42")
	if first != second {
		t.Fatalf("request ids differ: %q != %q", first, second)
	}
	if first == requestIDForIdempotencyKey("withdrawal-43") {
		t.Fatal("different idempotency keys produced the same request id")
	}
	if first[14] != '5' {
		t.Fatalf("request id version = %q, want UUIDv5", first)
	}
}

func TestSendIdempotencyStorePersistsInflightDoneAndRetryableStates(t *testing.T) {
	t.Parallel()
	store := newSendIdempotencyStore(t.TempDir())
	request := SendRequest{Destination: "bc1pdestination", AmountSatoshis: 1000}
	hash := sendRequestHash(request)

	inflight, replay, err := store.start("transfer-1", hash)
	if err != nil || replay || inflight.State != SendInFlight {
		t.Fatalf("start = %#v replay=%t err=%v", inflight, replay, err)
	}
	if _, _, err := store.start("transfer-1", hash); !errors.Is(err, ErrSendInProgress) {
		t.Fatalf("second start error = %v, want ErrSendInProgress", err)
	}
	if _, _, err := store.start("transfer-1", sendRequestHash(SendRequest{Destination: "bc1pother", AmountSatoshis: 1000})); !errors.Is(err, ErrIdempotencyKeyReused) {
		t.Fatalf("changed request error = %v, want ErrIdempotencyKeyReused", err)
	}
	if err := store.failedPreSubmit("transfer-1"); err != nil {
		t.Fatal(err)
	}
	retry, replay, err := store.start("transfer-1", hash)
	if err != nil || replay || retry.State != SendInFlight || retry.RequestID != inflight.RequestID {
		t.Fatalf("retry = %#v replay=%t err=%v", retry, replay, err)
	}

	want := SendResult{OperationID: 7, TransactionID: "txid", AmountSatoshis: 1000, FeeSatoshis: 12}
	if err := store.done("transfer-1", want); err != nil {
		t.Fatal(err)
	}
	restarted := newSendIdempotencyStore(store.dataDir)
	done, replay, err := restarted.start("transfer-1", hash)
	if err != nil || !replay || done.State != SendDone {
		t.Fatalf("replay = %#v replay=%t err=%v", done, replay, err)
	}
	var result SendResult
	if err := json.Unmarshal(done.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result != want {
		t.Fatalf("stored result = %#v, want %#v", result, want)
	}
}

func TestPreviewOnchainWithoutDestinationNeverPostsOperation(t *testing.T) {
	t.Parallel()
	var postOperations int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/houston/realtime":
			writeTestJSON(t, writer, previewRealtime())
		case "/houston/operations/next-transaction-size":
			writeTestJSON(t, writer, previewNextSize())
		case "/houston/operations":
			postOperations++
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := NewManager(Config{DataDir: t.TempDir(), Passphrase: "test passphrase", HoustonURL: server.URL + "/houston/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveRecord(walletRecord{Version: 1, EncryptedBasePrivateKey: "not-used-for-onchain-preview", SessionToken: "session-token"}); err != nil {
		t.Fatal(err)
	}
	preview, err := manager.PreviewSend(context.Background(), SendPreviewRequest{AmountSatoshis: 1_000, Method: "onchain"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Method != "onchain" || preview.AmountSatoshis != 1_000 || preview.FeeSatoshis <= 0 || preview.TotalSatoshis != preview.AmountSatoshis+preview.FeeSatoshis {
		t.Fatalf("preview = %#v", preview)
	}
	if postOperations != 0 {
		t.Fatalf("POST /operations calls = %d, want 0", postOperations)
	}
}

func TestReconcileSendClosesInflightKeyWhenHoustonReturnsMatchingRequestID(t *testing.T) {
	t.Parallel()
	var requestID string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/houston/operations" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writeTestJSON(t, writer, []map[string]any{{
			"id":           99,
			"requestId":    requestID,
			"status":       "CONFIRMED",
			"creationDate": "2026-09-28T00:00:00Z",
			"amount":       map[string]any{"inSatoshis": 1000},
			"fee":          map[string]any{"inSatoshis": 10},
			"transaction": map[string]any{
				"hash":          "reconciled-txid",
				"confirmations": 1,
			},
		}})
	}))
	defer server.Close()

	config := Config{DataDir: t.TempDir(), Passphrase: "test passphrase", HoustonURL: server.URL + "/houston/"}
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveRecord(walletRecord{Version: 1, EncryptedBasePrivateKey: "not-used-by-reconcile", SessionToken: "session-token"}); err != nil {
		t.Fatal(err)
	}
	record, _, err := manager.sendIdempotency.start("stuck-transfer", sendRequestHash(SendRequest{Destination: "bc1pdestination", AmountSatoshis: 1000}))
	if err != nil {
		t.Fatal(err)
	}
	requestID = record.RequestID
	manager.sendIdempotency.deactivate("stuck-transfer")

	status, err := manager.ReconcileSend(context.Background(), "stuck-transfer")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SendDone || status.Result == nil || status.Result.OperationID != 99 || status.Result.TransactionID != "reconciled-txid" {
		t.Fatalf("status = %#v", status)
	}

	restarted, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.SendStatus(context.Background(), "stuck-transfer")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != SendDone || persisted.Result == nil || persisted.Result.OperationID != 99 {
		t.Fatalf("persisted status = %#v", persisted)
	}
}

func TestReconcileSendPersistsLightningPaymentHashAndRequestedAmount(t *testing.T) {
	t.Parallel()

	const requestedAmount = int64(1_000)
	const fee = int64(73)
	paymentHash := strings.Repeat("ab", 32)
	var requestID string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/houston/operations" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writeTestJSON(t, writer, []map[string]any{{
			"id":                     100,
			"requestId":              requestID,
			"direction":              "OUTGOING",
			"status":                 "SWAP_PENDING",
			"creationDate":           "2026-09-28T00:00:00Z",
			"amount":                 map[string]any{"inSatoshis": requestedAmount},
			"fee":                    map[string]any{"inSatoshis": fee},
			"outputAmountInSatoshis": requestedAmount + fee,
			"swap": map[string]any{
				"swapUuid": "reconciled-lightning-swap",
				"fundingOutput": map[string]any{
					"serverPaymentHashInHex": paymentHash,
				},
			},
		}})
	}))
	defer server.Close()

	config := Config{DataDir: t.TempDir(), Passphrase: "test passphrase", HoustonURL: server.URL + "/houston/"}
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveRecord(walletRecord{Version: 1, EncryptedBasePrivateKey: "not-used-by-reconcile", SessionToken: "session-token"}); err != nil {
		t.Fatal(err)
	}
	record, _, err := manager.sendIdempotency.start("stuck-lightning-transfer", sendRequestHash(SendRequest{Destination: "lnbc1example", AmountSatoshis: requestedAmount}))
	if err != nil {
		t.Fatal(err)
	}
	requestID = record.RequestID
	manager.sendIdempotency.deactivate("stuck-lightning-transfer")

	status, err := manager.ReconcileSend(context.Background(), "stuck-lightning-transfer")
	if err != nil {
		t.Fatal(err)
	}
	want := SendResult{OperationID: 100, AmountSatoshis: requestedAmount, FeeSatoshis: fee, PaymentHash: paymentHash}
	if status.State != SendDone || status.Result == nil || *status.Result != want {
		t.Fatalf("status = %#v, want done result %#v", status, want)
	}
	persisted, err := manager.SendStatus(context.Background(), "stuck-lightning-transfer")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != SendDone || persisted.Result == nil || *persisted.Result != want {
		t.Fatalf("persisted = %#v, want done result %#v", persisted, want)
	}
}

func previewRealtime() map[string]any {
	return map[string]any{
		"feeWindow": map[string]any{
			"id":               7,
			"targetedFees":     map[string]float64{"1": 5},
			"fastConfTarget":   1,
			"mediumConfTarget": 3,
			"slowConfTarget":   6,
		},
		"exchangeRateWindow": map[string]any{"id": 9, "rates": map[string]float64{"BTC": 1, "USD": 50_000}},
	}
}

func previewNextSize() map[string]any {
	return map[string]any{
		"sizeProgression": []map[string]any{{
			"amountInSatoshis": 20_000,
			"sizeInBytes":      400,
			"outpoint":         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0",
			"status":           "CONFIRMED",
		}},
		"expectedDebtInSat": 0,
	}
}

func TestReconcileSendDoesNotReleaseAnActiveOrUnverifiableKey(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/houston/operations" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		writeTestJSON(t, writer, []map[string]any{{
			"id": 1, "status": "CONFIRMED", "amount": map[string]any{"inSatoshis": 1000}, "fee": map[string]any{"inSatoshis": 10},
		}})
	}))
	defer server.Close()

	manager, err := NewManager(Config{DataDir: t.TempDir(), Passphrase: "test passphrase", HoustonURL: server.URL + "/houston/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveRecord(walletRecord{Version: 1, EncryptedBasePrivateKey: "not-used-by-reconcile", SessionToken: "session-token"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.sendIdempotency.start("active-transfer", sendRequestHash(SendRequest{Destination: "bc1pdestination", AmountSatoshis: 1000})); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReconcileSend(context.Background(), "active-transfer"); !errors.Is(err, ErrSendInProgress) {
		t.Fatalf("active reconcile error = %v, want ErrSendInProgress", err)
	}
	manager.sendIdempotency.deactivate("active-transfer")
	if _, err := manager.ReconcileSend(context.Background(), "active-transfer"); !errors.Is(err, ErrHoustonRequestIDUnavailable) {
		t.Fatalf("unverifiable reconcile error = %v, want ErrHoustonRequestIDUnavailable", err)
	}
	status, err := manager.SendStatus(context.Background(), "active-transfer")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != SendInFlight {
		t.Fatalf("state = %q, want %q", status.State, SendInFlight)
	}
}
