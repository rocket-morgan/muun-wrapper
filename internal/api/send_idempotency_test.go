package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestSendPreviewAcceptsOnchainAmountWithoutDestination(t *testing.T) {
	t.Parallel()
	server := NewServer(&fakeWallet{}, "api-token")
	request := httptest.NewRequest(http.MethodPost, "/send/preview", strings.NewReader(`{"amountSatoshis":1000,"method":"onchain"}`))
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestSendIdempotencyKeyReplaysCompletedResultWithoutResubmitting(t *testing.T) {
	t.Parallel()
	result := wallet.SendResult{OperationID: 42, TransactionID: "txid", AmountSatoshis: 1000, FeeSatoshis: 10}
	service := &fakeWallet{sendResult: result, sendOutcomes: []wallet.SendOutcome{{Result: result}, {Result: result, Replay: true}}}
	server := NewServer(service, "api-token")
	body := `{"destination":"bc1pdestination","amountSatoshis":1000,"idempotencyKey":"transfer-1"}`

	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body))
	firstRequest.Header.Set("Authorization", "Bearer api-token")
	server.ServeHTTP(first, firstRequest)

	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(body))
	secondRequest.Header.Set("Authorization", "Bearer api-token")
	server.ServeHTTP(second, secondRequest)

	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusCreated)
	}
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusOK)
	}
	if got := second.Header().Get("Idempotent-Replay"); got != "true" {
		t.Fatalf("Idempotent-Replay = %q, want true", got)
	}
	if service.sendCalls != 2 {
		t.Fatalf("service calls = %d, want 2", service.sendCalls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed body = %q, want %q", second.Body.String(), first.Body.String())
	}
}

func TestSendStatusAndOperationLookupAreAuthenticatedRoutes(t *testing.T) {
	t.Parallel()
	server := NewServer(&fakeWallet{}, "api-token")
	for _, path := range []string{"/send/status/transfer-1", "/operations/42"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer api-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", path, response.Code, http.StatusOK)
		}
	}
}

func TestSendStatusUnknownKeyReturnsCapabilityCheckCode(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{sendStatusErr: wallet.ErrUnknownIdempotencyKey}
	server := NewServer(service, "api-token")
	request := httptest.NewRequest(http.MethodGet, "/send/status/missing-key", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unknown idempotency key" || body["code"] != "idempotency_key_not_found" {
		t.Fatalf("body = %#v", body)
	}
}

func TestReconcileSendAcceptsAnEscapedKey(t *testing.T) {
	t.Parallel()
	service := &fakeWallet{reconcileSendResult: wallet.SendStatus{State: wallet.SendFailedPreSubmit}}
	server := NewServer(service, "api-token")
	request := httptest.NewRequest(http.MethodPost, "/send/reconcile/transfer%2F1", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if service.reconcileSendCalls != 1 || service.reconcileSendKey != "transfer/1" {
		t.Fatalf("calls=%d key=%q", service.reconcileSendCalls, service.reconcileSendKey)
	}
}

func TestSendUsesContractErrorCodesByFailurePhase(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "in flight", err: &wallet.SendError{Cause: wallet.ErrSendInProgress}, status: http.StatusConflict, code: "send_in_progress"},
		{name: "reused", err: &wallet.SendError{Cause: wallet.ErrIdempotencyKeyReused}, status: http.StatusUnprocessableEntity, code: "idempotency_key_reused"},
		{name: "validation", err: &wallet.SendError{Cause: errors.New("invoice expired")}, status: http.StatusBadRequest, code: "validation"},
		{name: "session before submit", err: &wallet.SendError{Cause: &wallet.HoustonError{StatusCode: http.StatusUnauthorized, Message: "Houston returned 401"}}, status: http.StatusServiceUnavailable, code: "houston_session"},
		{name: "post submit", err: &wallet.SendError{Cause: errors.New("push failed"), Submitted: true}, status: http.StatusBadGateway, code: "submit_failed"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			service := &fakeWallet{sendErr: testCase.err}
			server := NewServer(service, "api-token")
			request := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(`{"destination":"bc1pdestination","amountSatoshis":1000,"idempotencyKey":"transfer-1"}`))
			request.Header.Set("Authorization", "Bearer api-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d", response.Code, testCase.status)
			}
			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != testCase.code {
				t.Fatalf("code = %q, want %q", body["code"], testCase.code)
			}
		})
	}
}

func TestOperationLookupReturnsNormalizedDetail(t *testing.T) {
	t.Parallel()
	want := wallet.OperationDetail{ID: 42, Status: "CONFIRMED", PaymentState: "paid", CreatedAt: "2026-09-28T00:00:00Z", AmountSatoshis: 1_000, FeeSatoshis: 10, Confirmations: 2}
	service := &fakeWallet{operationResult: want}
	server := NewServer(service, "api-token")
	request := httptest.NewRequest(http.MethodGet, "/operations/42", nil)
	request.Header.Set("Authorization", "Bearer api-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.operationID != 42 {
		t.Fatalf("status=%d operationID=%d", response.Code, service.operationID)
	}
	var detail wallet.OperationDetail
	if err := json.NewDecoder(response.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail, want) {
		t.Fatalf("detail = %#v, want %#v", detail, want)
	}
}
