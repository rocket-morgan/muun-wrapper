package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestCreateWalletRequiresStaticToken(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodPost, "/wallet/create", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if service.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", service.createCalls)
	}
}

func TestCreateWalletReturnsFirstAddress(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{createResult: wallet.WalletInfo{Address: "bc1pfirst", AddressIndex: 0}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodPost, "/wallet/create", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if service.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", service.createCalls)
	}
	var body wallet.WalletInfo
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body != service.createResult {
		t.Fatalf("body = %#v, want %#v", body, service.createResult)
	}
}

func TestReceiveAddressReturnsNextAddress(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{receiveResult: wallet.WalletInfo{Address: "bc1pnext", AddressIndex: 1}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodGet, "/receive/address", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if service.receiveCalls != 1 {
		t.Fatalf("receive calls = %d, want 1", service.receiveCalls)
	}
	var body wallet.WalletInfo
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body != service.receiveResult {
		t.Fatalf("body = %#v, want %#v", body, service.receiveResult)
	}
}

func TestBalanceReturnsAuthenticatedUnifiedBalance(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{balanceResult: wallet.Balance{Satoshis: 3_800}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodGet, "/balance", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if service.balanceCalls != 1 {
		t.Fatalf("balance calls = %d, want 1", service.balanceCalls)
	}
	var body wallet.Balance
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body != service.balanceResult {
		t.Fatalf("body = %#v, want %#v", body, service.balanceResult)
	}
}

func TestOperationsReturnsAuthenticatedHistory(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{operationsResult: []wallet.Operation{{ID: 42, Direction: "INCOMING", Status: "CONFIRMED", AmountSatoshis: 25_000}}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodGet, "/operations", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if service.operationsCalls != 1 {
		t.Fatalf("operations calls = %d, want 1", service.operationsCalls)
	}
	var body []wallet.Operation
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, service.operationsResult) {
		t.Fatalf("body = %#v, want %#v", body, service.operationsResult)
	}
}

func TestReceiveInvoiceCreatesAuthenticatedLightningInvoice(t *testing.T) {
	t.Parallel()
	amount := int64(2_500)
	service := &fakeWallet{invoiceResult: wallet.Invoice{Invoice: "lnbc2500n1invoice", AmountSatoshis: &amount}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodPost, "/receive/invoice", strings.NewReader(`{"amountSatoshis":2500}`))
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	if service.invoiceAmount == nil || *service.invoiceAmount != amount {
		t.Fatalf("invoice amount = %#v, want %d", service.invoiceAmount, amount)
	}
	var body wallet.Invoice
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Invoice != service.invoiceResult.Invoice || body.AmountSatoshis == nil || *body.AmountSatoshis != amount {
		t.Fatalf("response = %#v", body)
	}
}

func TestSyncFulfillsAuthenticatedIncomingSwaps(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{syncResult: wallet.SyncResult{FulfilledIncomingSwaps: 1}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodPost, "/sync", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.syncCalls != 1 {
		t.Fatalf("status=%d syncCalls=%d", response.Code, service.syncCalls)
	}
	var body wallet.SyncResult
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body != service.syncResult {
		t.Fatalf("response = %#v", body)
	}
}

func TestSendSubmitsAuthenticatedPayment(t *testing.T) {
	t.Parallel()
	requestBody := `{"destination":"bc1pdestination","amountSatoshis":10000,"description":"test payment"}`
	service := &fakeWallet{sendResult: wallet.SendResult{OperationID: 42, TransactionID: "txid", AmountSatoshis: 10_000, FeeSatoshis: 500}}
	server := NewServer(service, "api-token")

	request := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || service.sendCalls != 1 {
		t.Fatalf("status=%d sendCalls=%d", response.Code, service.sendCalls)
	}
	if service.sendRequest != (wallet.SendRequest{Destination: "bc1pdestination", AmountSatoshis: 10_000, Description: "test payment"}) {
		t.Fatalf("send request = %#v", service.sendRequest)
	}
	var body wallet.SendResult
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body != service.sendResult {
		t.Fatalf("response = %#v", body)
	}
}

type fakeWallet struct {
	createCalls      int
	receiveCalls     int
	balanceCalls     int
	operationsCalls  int
	invoiceCalls     int
	syncCalls        int
	sendCalls        int
	createResult     wallet.WalletInfo
	receiveResult    wallet.WalletInfo
	balanceResult    wallet.Balance
	operationsResult []wallet.Operation
	invoiceResult    wallet.Invoice
	syncResult       wallet.SyncResult
	sendResult       wallet.SendResult
	createErr        error
	receiveErr       error
	balanceErr       error
	operationsErr    error
	invoiceErr       error
	syncErr          error
	sendErr          error
	invoiceAmount    *int64
	sendRequest      wallet.SendRequest
}

func (w *fakeWallet) Create(context.Context) (wallet.WalletInfo, error) {
	w.createCalls++
	return w.createResult, w.createErr
}

func (w *fakeWallet) ReceiveAddress(context.Context) (wallet.WalletInfo, error) {
	w.receiveCalls++
	return w.receiveResult, w.receiveErr
}

func (w *fakeWallet) Balance(context.Context) (wallet.Balance, error) {
	w.balanceCalls++
	return w.balanceResult, w.balanceErr
}

func (w *fakeWallet) Operations(context.Context) ([]wallet.Operation, error) {
	w.operationsCalls++
	return w.operationsResult, w.operationsErr
}

func (w *fakeWallet) ReceiveInvoice(_ context.Context, amount *int64) (wallet.Invoice, error) {
	w.invoiceCalls++
	w.invoiceAmount = amount
	return w.invoiceResult, w.invoiceErr
}

func (w *fakeWallet) Sync(context.Context) (wallet.SyncResult, error) {
	w.syncCalls++
	return w.syncResult, w.syncErr
}

func (w *fakeWallet) Send(_ context.Context, request wallet.SendRequest) (wallet.SendResult, error) {
	w.sendCalls++
	w.sendRequest = request
	return w.sendResult, w.sendErr
}
