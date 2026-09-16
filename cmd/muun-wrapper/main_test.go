package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestRunCreatePrintsAddressReturnedByAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/wallet/create" {
			t.Fatalf("request = %s %s, want POST /wallet/create", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeInfo(t, w, http.StatusCreated, wallet.WalletInfo{Address: "bc1pfirst", AddressIndex: 0})
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run([]string{"create", "--api-url", server.URL, "--api-token", "api-token"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var info wallet.WalletInfo
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info != (wallet.WalletInfo{Address: "bc1pfirst", AddressIndex: 0}) {
		t.Fatalf("output = %#v", info)
	}
}

func TestRunAddressPrintsAddressReturnedByAPI(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/receive/address" {
			t.Fatalf("request = %s %s, want GET /receive/address", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeInfo(t, w, http.StatusOK, wallet.WalletInfo{Address: "bc1pnext", AddressIndex: 1})
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run([]string{"address", "--api-url", server.URL, "--api-token", "api-token"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var info wallet.WalletInfo
	if err := json.Unmarshal(output.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info != (wallet.WalletInfo{Address: "bc1pnext", AddressIndex: 1}) {
		t.Fatalf("output = %#v", info)
	}
}

func TestRunBalancePrintsBalanceReturnedByAPI(t *testing.T) {
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

	var output bytes.Buffer
	err := run([]string{"balance", "--api-url", server.URL, "--api-token", "api-token"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var balance wallet.Balance
	if err := json.Unmarshal(output.Bytes(), &balance); err != nil {
		t.Fatal(err)
	}
	if balance != (wallet.Balance{Satoshis: 3_800}) {
		t.Fatalf("output = %#v", balance)
	}
}

func TestRunOperationsPrintsOperationHistoryReturnedByAPI(t *testing.T) {
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

	var output bytes.Buffer
	err := run([]string{"operations", "--api-url", server.URL, "--api-token", "api-token"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var operations []wallet.Operation
	if err := json.Unmarshal(output.Bytes(), &operations); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("output = %#v, want %#v", operations, want)
	}
}

func TestRunInvoicePrintsLightningInvoiceReturnedByAPI(t *testing.T) {
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

	var output bytes.Buffer
	err := run([]string{"invoice", "--api-url", server.URL, "--api-token", "api-token", "--amount-sats", "2500"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var invoice wallet.Invoice
	if err := json.Unmarshal(output.Bytes(), &invoice); err != nil {
		t.Fatal(err)
	}
	if invoice.Invoice != "lnbc2500n1invoice" || invoice.AmountSatoshis == nil || *invoice.AmountSatoshis != amount {
		t.Fatalf("invoice = %#v", invoice)
	}
}

func TestRunSyncPrintsResultReturnedByAPI(t *testing.T) {
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

	var output bytes.Buffer
	err := run([]string{"sync", "--api-url", server.URL, "--api-token", "api-token"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var result wallet.SyncResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result != (wallet.SyncResult{FulfilledIncomingSwaps: 1}) {
		t.Fatalf("sync result = %#v", result)
	}
}

func TestRunSendPrintsSubmittedPayment(t *testing.T) {
	t.Parallel()
	want := wallet.SendResult{OperationID: 42, TransactionID: "txid", AmountSatoshis: 10_000, FeeSatoshis: 500}
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
		if request != (wallet.SendRequest{Destination: "bc1pdestination", AmountSatoshis: 10_000, Description: "test payment"}) {
			t.Fatalf("request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(want); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	err := run([]string{
		"send", "--api-url", server.URL, "--api-token", "api-token",
		"--destination", "bc1pdestination", "--amount-sats", "10000", "--description", "test payment",
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var result wallet.SendResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result != want {
		t.Fatalf("output = %#v", result)
	}
}

func TestSyncIntervalReadsConfiguredDuration(t *testing.T) {
	t.Setenv("MUUN_WRAPPER_SYNC_INTERVAL", "45s")
	interval, err := syncInterval()
	if err != nil {
		t.Fatal(err)
	}
	if interval != 45*time.Second {
		t.Fatalf("interval = %s, want 45s", interval)
	}
}

func TestSyncIntervalRejectsNonPositiveDuration(t *testing.T) {
	t.Setenv("MUUN_WRAPPER_SYNC_INTERVAL", "0s")
	if _, err := syncInterval(); err == nil {
		t.Fatal("expected an invalid interval error")
	}
}

func writeInfo(t *testing.T, writer http.ResponseWriter, status int, info wallet.WalletInfo) {
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
