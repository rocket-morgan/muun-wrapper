package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestCreateWalletUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/wallet/create" {
			t.Fatalf("request = %s %s, want POST /wallet/create", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeWalletInfo(t, w, http.StatusCreated, wallet.WalletInfo{Address: "bc1pfirst", AddressIndex: 0})
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.CreateWallet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (wallet.WalletInfo{Address: "bc1pfirst", AddressIndex: 0}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestReceiveAddressUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/receive/address" {
			t.Fatalf("request = %s %s, want GET /receive/address", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeWalletInfo(t, w, http.StatusOK, wallet.WalletInfo{Address: "bc1pnext", AddressIndex: 1})
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.ReceiveAddress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (wallet.WalletInfo{Address: "bc1pnext", AddressIndex: 1}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestBalanceUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/balance" {
			t.Fatalf("request = %s %s, want GET /balance", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeBalance(t, w, http.StatusOK, wallet.Balance{Satoshis: 3_800})
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (wallet.Balance{Satoshis: 3_800}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestOperationsUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	want := []wallet.Operation{{ID: 42, Direction: "INCOMING", Status: "CONFIRMED", AmountSatoshis: 25_000}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/operations" {
			t.Fatalf("request = %s %s, want GET /operations", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeOperations(t, w, http.StatusOK, want)
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestReceiveInvoiceUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	amount := int64(2_500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/receive/invoice" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			AmountSatoshis *int64 `json:"amountSatoshis"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.AmountSatoshis == nil || *request.AmountSatoshis != amount {
			t.Fatalf("amount = %#v", request.AmountSatoshis)
		}
		writeInvoice(t, w, http.StatusCreated, wallet.Invoice{Invoice: "lnbc2500n1invoice", AmountSatoshis: &amount})
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.ReceiveInvoice(context.Background(), &amount)
	if err != nil {
		t.Fatal(err)
	}
	if result.Invoice != "lnbc2500n1invoice" || result.AmountSatoshis == nil || *result.AmountSatoshis != amount {
		t.Fatalf("invoice = %#v", result)
	}
}

func TestSyncUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sync" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeSyncResult(t, w, http.StatusOK, wallet.SyncResult{FulfilledIncomingSwaps: 1})
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result != (wallet.SyncResult{FulfilledIncomingSwaps: 1}) {
		t.Fatalf("sync result = %#v", result)
	}
}

func TestSendUsesAuthenticatedAPI(t *testing.T) {
	t.Parallel()
	wantRequest := wallet.SendRequest{Destination: "bc1pdestination", AmountSatoshis: 10_000, Description: "test payment"}
	wantResult := wallet.SendResult{OperationID: 42, TransactionID: "txid", AmountSatoshis: 10_000, FeeSatoshis: 500}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/send" {
			t.Fatalf("request = %s %s, want POST /send", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		var request wallet.SendRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request != wantRequest {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(wantResult); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	api, err := New(server.URL, "api-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := api.Send(context.Background(), wantRequest)
	if err != nil {
		t.Fatal(err)
	}
	if result != wantResult {
		t.Fatalf("result = %#v", result)
	}
}

func writeWalletInfo(t *testing.T, writer http.ResponseWriter, status int, info wallet.WalletInfo) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(info); err != nil {
		t.Fatal(err)
	}
}

func writeBalance(t *testing.T, writer http.ResponseWriter, status int, balance wallet.Balance) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(balance); err != nil {
		t.Fatal(err)
	}
}

func writeOperations(t *testing.T, writer http.ResponseWriter, status int, operations []wallet.Operation) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(operations); err != nil {
		t.Fatal(err)
	}
}

func writeInvoice(t *testing.T, writer http.ResponseWriter, status int, invoice wallet.Invoice) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(invoice); err != nil {
		t.Fatal(err)
	}
}

func writeSyncResult(t *testing.T, writer http.ResponseWriter, status int, result wallet.SyncResult) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(result); err != nil {
		t.Fatal(err)
	}
}
