package wallet_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	libwallet "github.com/muun/libwallet"
	"github.com/wapu/muun-wrapper/internal/wallet"
)

const basePath = "m/schema:1'/recovery:1'"

func TestCreateWalletPersistsEncryptedKeyAndReturnsTaprootAddress(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{7}, 32))
	var created bool
	var fetchedAddresses bool
	var updatedAddresses bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			created = true
			if r.Method != http.MethodPost {
				t.Fatalf("create method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("X-Client-Version"); got != "19020002" {
				t.Fatalf("X-Client-Version = %q, want 19020002", got)
			}
			if got := r.Header.Get("X-Client-Version-Name"); got != "2.16.0" {
				t.Fatalf("X-Client-Version-Name = %q, want 2.16.0", got)
			}
			if got := r.Header.Get("X-Client-Type"); got != "FALCON" {
				t.Fatalf("X-Client-Type = %q, want FALCON", got)
			}
			if got := r.Header.Get("X-Client-Sdk-Version"); got != "" {
				t.Fatalf("X-Client-Sdk-Version = %q, want empty for FALCON", got)
			}

			var payload struct {
				Client struct {
					Type                 string  `json:"type"`
					BuildType            string  `json:"buildType"`
					Version              int     `json:"version"`
					VersionName          string  `json:"versionName"`
					AndroidApplicationID *string `json:"androidApplicationId"`
				} `json:"client"`
				GcmToken        *string `json:"gcmToken"`
				PrimaryCurrency string  `json:"primaryCurrency"`
				BasePublicKey   struct {
					Path string `json:"path"`
					Key  string `json:"key"`
				} `json:"basePublicKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Client.Type != "FALCON" || payload.Client.BuildType != "release" || payload.Client.Version != 19020002 || payload.Client.VersionName != "2.16.0" {
				t.Fatalf("unexpected client payload: %#v", payload.Client)
			}
			if payload.GcmToken != nil {
				t.Fatalf("gcmToken should be omitted, got %q", *payload.GcmToken)
			}
			if payload.Client.AndroidApplicationID != nil {
				t.Fatalf("androidApplicationId should be omitted, got %q", *payload.Client.AndroidApplicationID)
			}
			if payload.PrimaryCurrency != "USD" {
				t.Fatalf("primaryCurrency = %q, want USD", payload.PrimaryCurrency)
			}
			if payload.BasePublicKey.Path != basePath || payload.BasePublicKey.Key == "" {
				t.Fatalf("unexpected base public key: %#v", payload.BasePublicKey)
			}

			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			switch r.Method {
			case http.MethodGet:
				fetchedAddresses = true
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				updatedAddresses = true
				var record struct {
					MaxUsedIndex     int  `json:"maxUsedIndex"`
					MaxWatchingIndex *int `json:"maxWatchingIndex"`
				}
				if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
					t.Fatal(err)
				}
				if record.MaxUsedIndex != 0 {
					t.Fatalf("maxUsedIndex = %d, want 0", record.MaxUsedIndex)
				}
				if record.MaxWatchingIndex != nil {
					t.Fatalf("maxWatchingIndex should be omitted, got %d", *record.MaxWatchingIndex)
				}
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	config := wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     19020002,
		ClientVersionName: "2.16.0",
	}
	manager, err := wallet.NewManager(config)
	if err != nil {
		t.Fatal(err)
	}

	info, err := manager.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !created || !fetchedAddresses || !updatedAddresses {
		t.Fatalf("create=%t fetchedAddresses=%t updatedAddresses=%t", created, fetchedAddresses, updatedAddresses)
	}
	if !strings.HasPrefix(info.Address, "bc1p") {
		t.Fatalf("address = %q, want a mainnet taproot address", info.Address)
	}
	if info.AddressIndex != 0 {
		t.Fatalf("address index = %d, want 0", info.AddressIndex)
	}

	persisted, err := os.ReadFile(filepath.Join(manager.DataDir(), "wallet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "xprv") {
		t.Fatalf("wallet record contains a plaintext xprv: %s", persisted)
	}
	if !strings.Contains(string(persisted), "encryptedBasePrivateKey") {
		t.Fatalf("wallet record does not contain encrypted private key: %s", persisted)
	}
}

func TestCreateResumesWhenInitialAddressReservationFails(t *testing.T) {
	muunKey := mustBaseKey(t, bytes.Repeat([]byte{17}, 32))
	var initialAddressLookup bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				if !initialAddressLookup {
					initialAddressLookup = true
					http.Error(w, "temporary Houston failure", http.StatusServiceUnavailable)
					return
				}
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     19020002,
		ClientVersionName: "2.16.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err == nil {
		t.Fatal("Create succeeded despite the initial address reservation failure")
	}

	info, err := manager.Create(context.Background())
	if err != nil {
		t.Fatalf("Create retry: %v", err)
	}
	if info.AddressIndex != 0 || !strings.HasPrefix(info.Address, "bc1p") {
		t.Fatalf("Create retry = %#v, want the first Taproot address", info)
	}
}

func TestReceiveAddressReservesDistinctAddressesForConcurrentRequests(t *testing.T) {
	muunKey := mustBaseKey(t, bytes.Repeat([]byte{18}, 32))
	var recordMu sync.Mutex
	maxUsedIndex := -1

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				recordMu.Lock()
				used := maxUsedIndex
				recordMu.Unlock()
				time.Sleep(100 * time.Millisecond)
				writeJSON(t, w, map[string]int{"maxUsedIndex": used, "maxWatchingIndex": 100})
			case http.MethodPut:
				var record struct {
					MaxUsedIndex int `json:"maxUsedIndex"`
				}
				if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
					t.Fatal(err)
				}
				recordMu.Lock()
				maxUsedIndex = record.MaxUsedIndex
				recordMu.Unlock()
				writeJSON(t, w, map[string]int{"maxUsedIndex": record.MaxUsedIndex, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     19020002,
		ClientVersionName: "2.16.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan wallet.WalletInfo, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			info, err := manager.ReceiveAddress(context.Background())
			if err != nil {
				errors <- err
				return
			}
			results <- info
		}()
	}
	close(start)

	var addresses []wallet.WalletInfo
	for range 2 {
		select {
		case err := <-errors:
			t.Fatal(err)
		case info := <-results:
			addresses = append(addresses, info)
		case <-time.After(2 * time.Second):
			t.Fatal("ReceiveAddress did not complete")
		}
	}
	if addresses[0].Address == addresses[1].Address || addresses[0].AddressIndex == addresses[1].AddressIndex {
		t.Fatalf("concurrent addresses = %#v, want two distinct reservations", addresses)
	}
}

func TestBalanceUsesHoustonNextTransactionSize(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{8}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/operations/next-transaction-size":
			if r.Method != http.MethodGet {
				t.Fatalf("balance method = %s, want GET", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(t, w, map[string]any{
				"sizeProgression": []map[string]int64{
					{"amountInSatoshis": 2_500},
					{"amountInSatoshis": 5_000},
				},
				"expectedDebtInSat": 1_200,
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	config := wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	}
	manager, err := wallet.NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	balance, err := manager.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if balance != (wallet.Balance{Satoshis: 3_800}) {
		t.Fatalf("balance = %#v, want 3800 sats", balance)
	}
}

func TestOperationsReturnsHoustonOperationHistory(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{9}, 32))
	transactionID := "transaction-id"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/operations":
			if r.Method != http.MethodGet {
				t.Fatalf("operations method = %s, want GET", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			writeJSON(t, w, []map[string]any{
				{
					"id":              42,
					"direction":       "INCOMING",
					"status":          "CONFIRMED",
					"creationDate":    "2026-09-28T01:02:03-03:00",
					"confirmations":   3,
					"receiverAddress": "bc1pdeposit",
					"amount":          map[string]int64{"inSatoshis": 25_000},
					"fee":             map[string]int64{"inSatoshis": 100},
					"transaction":     map[string]any{"hash": "transaction-id", "confirmations": 2},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	operations, err := manager.Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []wallet.Operation{{
		ID:              42,
		Direction:       "INCOMING",
		Status:          "CONFIRMED",
		PaymentState:    "paid",
		CreatedAt:       "2026-09-28T04:02:03Z",
		AmountSatoshis:  25_000,
		FeeSatoshis:     100,
		Confirmations:   3,
		TransactionID:   &transactionID,
		ReceiverAddress: "bc1pdeposit",
	}}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations = %#v, want %#v", operations, want)
	}
}

func TestOperationsExposeHoustonDatesAndOnchainFieldsButNullLightningTransaction(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{14}, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			if r.Method == http.MethodGet {
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
				return
			}
			writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
		case "/houston/operations":
			writeJSON(t, w, []map[string]any{
				{
					"id":              101,
					"direction":       "INCOMING",
					"status":          "CONFIRMED",
					"creationDate":    "2026-09-28T01:02:03-03:00",
					"receiverAddress": "bc1ponchainreceive",
					"confirmations":   4,
					"amount":          map[string]int64{"inSatoshis": 4_000},
					"fee":             map[string]int64{"inSatoshis": 0},
					"transaction":     map[string]any{"hash": "incoming-onchain-tx", "confirmations": 2},
				},
				{
					"id":           102,
					"direction":    "OUTGOING",
					"status":       "SWAP_PENDING",
					"creationDate": "2026-09-28T06:02:03Z",
					"swapUuid":     "outgoing-lightning-swap",
					"amount":       map[string]int64{"inSatoshis": 5_000},
					"fee":          map[string]int64{"inSatoshis": 25},
					"transaction":  map[string]any{"hash": "funding-tx-not-a-lightning-id", "confirmations": 0},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	operations, err := manager.Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(operations))
	}
	if operations[0].CreatedAt != "2026-09-28T04:02:03Z" || operations[0].PaymentState != "paid" || operations[0].TransactionID == nil || *operations[0].TransactionID != "incoming-onchain-tx" || operations[0].Confirmations != 4 || operations[0].ReceiverAddress != "bc1ponchainreceive" {
		t.Fatalf("onchain operation = %#v", operations[0])
	}
	if operations[1].CreatedAt != "2026-09-28T06:02:03Z" || operations[1].PaymentState != "pending" || operations[1].TransactionID != nil || operations[1].Confirmations != 0 {
		t.Fatalf("lightning operation = %#v", operations[1])
	}
	detail, err := manager.Operation(context.Background(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail, operations[0]) {
		t.Fatalf("detail = %#v, want %#v", detail, operations[0])
	}
}

func TestReceiveInvoiceRegistersSecretsPersistsThemAndBuildsBolt11(t *testing.T) {
	muunKey := mustBaseKey(t, bytes.Repeat([]byte{10}, 32))
	amount := int64(2_500)
	var registeredPaymentHashes []string
	var fetchedRealTimeData bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/realtime":
			if r.Method != http.MethodGet {
				t.Fatalf("realtime method = %s, want GET", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			fetchedRealTimeData = true
			writeJSON(t, w, map[string]any{
				"forwardingPolicies": []map[string]any{{
					"identityKeyHex":            hex.EncodeToString(muunKey.PublicKey().Raw()),
					"feeBaseMsat":               1_000,
					"feeProportionalMillionths": 10,
					"cltvExpiryDelta":           40,
				}},
			})
		case "/houston/incoming-swaps/invoices":
			if r.Method != http.MethodPost {
				t.Fatalf("register invoices method = %s, want POST", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
			}
			type registeredKey struct {
				Key  string `json:"key"`
				Path string `json:"path"`
			}
			var invoices []struct {
				PaymentHashHex string        `json:"paymentHashHex"`
				ShortChannelID int64         `json:"shortChannelId"`
				UserPublicKey  registeredKey `json:"userPublicKey"`
				MuunPublicKey  registeredKey `json:"muunPublicKey"`
				IdentityPubKey registeredKey `json:"identityPubKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&invoices); err != nil {
				t.Fatal(err)
			}
			if len(invoices) != 5 {
				t.Fatalf("registered %d invoices, want 5", len(invoices))
			}
			for _, invoice := range invoices {
				if len(invoice.PaymentHashHex) != 64 || invoice.ShortChannelID == 0 {
					t.Fatalf("invalid invoice registration: %#v", invoice)
				}
				if invoice.UserPublicKey.Key == "" || invoice.UserPublicKey.Path == "" ||
					invoice.MuunPublicKey.Key == "" || invoice.MuunPublicKey.Path == "" ||
					invoice.IdentityPubKey.Key == "" || invoice.IdentityPubKey.Path == "" {
					t.Fatalf("invoice registration omitted a key: %#v", invoice)
				}
				registeredPaymentHashes = append(registeredPaymentHashes, invoice.PaymentHashHex)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	dataDir := t.TempDir()
	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           dataDir,
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	invoice, err := manager.ReceiveInvoice(context.Background(), &amount)
	if err != nil {
		t.Fatal(err)
	}
	if !fetchedRealTimeData || len(registeredPaymentHashes) != 5 {
		t.Fatalf("fetched realtime=%t registered=%d", fetchedRealTimeData, len(registeredPaymentHashes))
	}
	decoded, err := libwallet.ParseInvoice(invoice.Invoice, libwallet.Mainnet())
	if err != nil {
		t.Fatalf("parse generated invoice: %v", err)
	}
	if decoded.Sats != amount {
		t.Fatalf("invoice amount = %d, want %d", decoded.Sats, amount)
	}
	if got := hex.EncodeToString(decoded.PaymentHash); !contains(registeredPaymentHashes, got) {
		t.Fatalf("invoice payment hash %s was not registered", got)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "wallet.db")); err != nil {
		t.Fatalf("wallet database was not persisted: %v", err)
	}
}

func TestSyncFulfillsFullDebtIncomingSwap(t *testing.T) {
	muunKey := mustBaseKey(t, bytes.Repeat([]byte{11}, 32))
	amount := int64(2_500)
	var paymentHash string
	var fulfilled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/realtime":
			writeJSON(t, w, map[string]any{
				"forwardingPolicies": []map[string]any{{
					"identityKeyHex":            hex.EncodeToString(muunKey.PublicKey().Raw()),
					"feeBaseMsat":               1_000,
					"feeProportionalMillionths": 10,
					"cltvExpiryDelta":           40,
				}},
			})
		case "/houston/incoming-swaps/invoices":
			w.WriteHeader(http.StatusNoContent)
		case "/houston/operations":
			if r.Method != http.MethodGet {
				t.Fatalf("operations method = %s, want GET", r.Method)
			}
			writeJSON(t, w, []map[string]any{{
				"incomingSwap": map[string]any{
					"uuid":                "swap-uuid",
					"paymentHashHex":      paymentHash,
					"sphinxPacketHex":     "",
					"paymentAmountInSats": amount,
					"collectInSats":       0,
				},
			}})
		case "/houston/incoming-swaps/swap-uuid":
			if r.Method != http.MethodPut {
				t.Fatalf("fulfillment method = %s, want PUT", r.Method)
			}
			var payload struct {
				Hex string `json:"hex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			preimage, err := hex.DecodeString(payload.Hex)
			if err != nil {
				t.Fatal(err)
			}
			preimageHash := sha256.Sum256(preimage)
			if got := hex.EncodeToString(preimageHash[:]); got != paymentHash {
				t.Fatalf("preimage hash = %s, want %s", got, paymentHash)
			}
			fulfilled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	config := wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	}
	manager, err := wallet.NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	invoice, err := manager.ReceiveInvoice(context.Background(), &amount)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := libwallet.ParseInvoice(invoice.Invoice, libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	paymentHash = hex.EncodeToString(decoded.PaymentHash)

	restartedManager, err := wallet.NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := restartedManager.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (wallet.SyncResult{FulfilledIncomingSwaps: 1}) || !fulfilled {
		t.Fatalf("sync result = %#v fulfilled=%t", result, fulfilled)
	}
}

func mustBaseKey(t *testing.T, seed []byte) *libwallet.HDPrivateKey {
	t.Helper()
	key, err := libwallet.NewHDPrivateKey(seed, libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	base, err := key.DeriveTo(basePath)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// TestInvoiceStatusTracksSettlementOfAnIssuedInvoice walks one issued invoice through every state
// the API can report, and checks that a hash this wallet never issued is rejected instead of being
// reported as simply unpaid.
func TestInvoiceStatusTracksSettlementOfAnIssuedInvoice(t *testing.T) {
	muunKey := mustBaseKey(t, bytes.Repeat([]byte{12}, 32))
	amount := int64(2_500)
	preimage := strings.Repeat("cd", 32)

	var operationsMu sync.Mutex
	operations := []map[string]any{}
	setOperations := func(value []map[string]any) {
		operationsMu.Lock()
		defer operationsMu.Unlock()
		operations = value
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			default:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			}
		case "/houston/realtime":
			writeJSON(t, w, map[string]any{
				"forwardingPolicies": []map[string]any{{
					"identityKeyHex":            hex.EncodeToString(muunKey.PublicKey().Raw()),
					"feeBaseMsat":               1_000,
					"feeProportionalMillionths": 1_000,
					"cltvExpiryDelta":           72,
				}},
			})
		case "/houston/incoming-swaps/invoices":
			w.WriteHeader(http.StatusNoContent)
		case "/houston/operations":
			operationsMu.Lock()
			defer operationsMu.Unlock()
			writeJSON(t, w, operations)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	invoice, err := manager.ReceiveInvoice(context.Background(), &amount)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := libwallet.ParseInvoice(invoice.Invoice, libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	if invoice.PaymentHash != hex.EncodeToString(decoded.PaymentHash) {
		t.Fatalf("invoice payment hash = %q, want %q", invoice.PaymentHash, hex.EncodeToString(decoded.PaymentHash))
	}

	incomingOperation := func(preimageHex string) []map[string]any {
		return []map[string]any{{
			"id":           float64(77),
			"direction":    "INCOMING",
			"status":       "CONFIRMED",
			"creationDate": "2026-09-28T00:00:00Z",
			"amount":       map[string]any{"inSatoshis": amount},
			"fee":          map[string]any{"inSatoshis": 0},
			"incomingSwap": map[string]any{
				"uuid":           "swap-uuid",
				"paymentHashHex": invoice.PaymentHash,
				"preimageHex":    preimageHex,
			},
		}}
	}

	// No operation yet: the invoice is ours, so this is pending rather than unknown.
	status, err := manager.InvoiceStatus(context.Background(), invoice.PaymentHash)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != wallet.InvoiceStatePending || status.Settled || status.AmountSatoshis != amount {
		t.Fatalf("pending status = %#v", status)
	}

	// Houston saw the payment but the swap is not fulfilled, so there is no preimage yet.
	setOperations(incomingOperation(""))
	status, err = manager.InvoiceStatus(context.Background(), invoice.PaymentHash)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != wallet.InvoiceStateReceiving || status.Settled || status.Preimage != "" {
		t.Fatalf("receiving status = %#v", status)
	}
	if status.OperationID == nil || *status.OperationID != 77 {
		t.Fatalf("receiving operation id = %#v", status.OperationID)
	}
	if status.ReceivedSatoshis == nil || *status.ReceivedSatoshis != amount {
		t.Fatalf("receiving received amount = %#v", status.ReceivedSatoshis)
	}

	// The preimage is the proof of payment.
	setOperations(incomingOperation(preimage))
	status, err = manager.InvoiceStatus(context.Background(), invoice.PaymentHash)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != wallet.InvoiceStateSettled || !status.Settled || status.Preimage != preimage {
		t.Fatalf("settled status = %#v", status)
	}

	// A hash this wallet never issued must not be reported as an unpaid invoice.
	if _, err := manager.InvoiceStatus(context.Background(), strings.Repeat("ab", 32)); !errors.Is(err, wallet.ErrUnknownPaymentHash) {
		t.Fatalf("unknown hash error = %v, want ErrUnknownPaymentHash", err)
	}

	// Malformed hashes are rejected before any lookup.
	for _, malformed := range []string{"", "zz", strings.Repeat("ab", 31)} {
		if _, err := manager.InvoiceStatus(context.Background(), malformed); err == nil {
			t.Fatalf("malformed hash %q was accepted", malformed)
		}
	}
}

// TestOperationsSurfaceIncomingSwapPaymentHashAndPreimage checks the fields that let a caller
// correlate an operation with the invoice that produced it.
func TestOperationsSurfaceIncomingSwapPaymentHashAndPreimage(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{13}, 32))
	paymentHash := strings.Repeat("ef", 32)
	preimage := strings.Repeat("12", 32)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			default:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			}
		case "/houston/operations":
			writeJSON(t, w, []map[string]any{{
				"id":           float64(91),
				"direction":    "INCOMING",
				"status":       "CONFIRMED",
				"creationDate": "2026-09-28T00:00:00Z",
				"amount":       map[string]any{"inSatoshis": 4_000},
				"fee":          map[string]any{"inSatoshis": 0},
				"incomingSwap": map[string]any{
					"uuid":           "swap-uuid",
					"paymentHashHex": paymentHash,
					"preimageHex":    preimage,
				},
			}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	operations, err := manager.Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %d, want 1", len(operations))
	}
	if operations[0].PaymentHash != paymentHash || operations[0].Preimage != preimage {
		t.Fatalf("operation = %#v", operations[0])
	}
}

// TestOperationsSurfaceOutgoingSwapPaymentHash checks that an outgoing Lightning operation carries
// the hash of the invoice it paid, so a caller can match it to the POST /send result. Onchain
// operations carry none and the preimage stays incoming-only.
func TestOperationsSurfaceOutgoingSwapPaymentHash(t *testing.T) {
	t.Parallel()

	muunKey := mustBaseKey(t, bytes.Repeat([]byte{15}, 32))
	paymentHash := strings.Repeat("cd", 32)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			default:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			}
		case "/houston/operations":
			writeJSON(t, w, []map[string]any{
				{
					"id":           float64(92),
					"direction":    "OUTGOING",
					"status":       "SWAP_PAYED",
					"creationDate": "2026-09-28T00:00:00Z",
					"swapUuid":     "outgoing-swap-uuid",
					"amount":       map[string]any{"inSatoshis": 4_000},
					"fee":          map[string]any{"inSatoshis": 12},
					"swap": map[string]any{
						"swapUuid":      "outgoing-swap-uuid",
						"fundingOutput": map[string]any{"serverPaymentHashInHex": paymentHash},
					},
				},
				{
					"id":           float64(93),
					"direction":    "OUTGOING",
					"status":       "CONFIRMED",
					"creationDate": "2026-09-28T00:00:00Z",
					"amount":       map[string]any{"inSatoshis": 3_000},
					"fee":          map[string]any{"inSatoshis": 5},
					"transaction":  map[string]any{"hash": "outgoing-onchain-tx", "confirmations": 1},
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     1511,
		ClientVersionName: "55.11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	operations, err := manager.Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(operations))
	}
	if operations[0].PaymentHash != paymentHash || operations[0].Preimage != "" {
		t.Fatalf("lightning operation = %#v", operations[0])
	}
	if operations[1].PaymentHash != "" || operations[1].Preimage != "" {
		t.Fatalf("onchain operation = %#v", operations[1])
	}

	// GET /operations/{id} maps the same history entry, and the wire key is paymentHash.
	for _, operation := range operations {
		detail, err := manager.Operation(context.Background(), operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(detail, operation) {
			t.Fatalf("detail = %#v, want %#v", detail, operation)
		}
	}
	lightning, err := json.Marshal(operations[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lightning), `"paymentHash":"`+paymentHash+`"`) {
		t.Fatalf("lightning operation JSON = %s", lightning)
	}
	onchain, err := json.Marshal(operations[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(onchain), "paymentHash") {
		t.Fatalf("onchain operation JSON = %s", onchain)
	}
}
