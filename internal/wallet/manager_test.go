package wallet_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
		AmountSatoshis:  25_000,
		FeeSatoshis:     100,
		Confirmations:   2,
		TransactionID:   "transaction-id",
		ReceiverAddress: "bc1pdeposit",
	}}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations = %#v, want %#v", operations, want)
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
