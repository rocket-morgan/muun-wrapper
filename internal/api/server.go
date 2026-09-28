package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

// WalletService is the subset of wallet operations exposed by the first API phase.
type WalletService interface {
	Create(context.Context) (wallet.WalletInfo, error)
	ReceiveAddress(context.Context) (wallet.WalletInfo, error)
	Balance(context.Context) (wallet.Balance, error)
	Operations(context.Context) ([]wallet.Operation, error)
	Operation(context.Context, int64) (wallet.OperationDetail, error)
	ReceiveInvoice(context.Context, *int64) (wallet.Invoice, error)
	InvoiceStatus(context.Context, string) (wallet.InvoiceStatus, error)
	PreviewSend(context.Context, wallet.SendPreviewRequest) (wallet.SendPreview, error)
	SendWithOutcome(context.Context, wallet.SendRequest) (wallet.SendOutcome, error)
	SendStatus(context.Context, string) (wallet.SendStatus, error)
	ReconcileSend(context.Context, string) (wallet.SendStatus, error)
	Sync(context.Context) (wallet.SyncResult, error)
	SetupRecovery(context.Context, string) (wallet.RecoverySetup, error)
	EmergencyKit(context.Context) (wallet.EmergencyKit, error)
}

// Server exposes one WalletService behind a static bearer token.
type Server struct {
	wallet WalletService
	token  string
}

// NewServer creates a REST handler for one wallet. The static token is required for every wallet route.
func NewServer(service WalletService, token string) *Server {
	return &Server{wallet: service, token: token}
}

// ServeHTTP routes authenticated wallet requests.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/healthz" {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !s.authorized(request) {
		writeError(writer, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/wallet/create":
		s.createWallet(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/receive/address":
		s.receiveAddress(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/balance":
		s.balance(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/operations":
		s.operations(writer, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, operationsPrefix):
		s.operation(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/receive/invoice":
		s.receiveInvoice(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/send/preview":
		s.sendPreview(writer, request)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, sendStatusPrefix):
		s.sendStatus(writer, request)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, sendReconcilePrefix):
		s.reconcileSend(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/send":
		s.send(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/sync":
		s.sync(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/recovery/setup":
		s.setupRecovery(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/recovery/kit":
		s.emergencyKit(writer, request)
	case strings.HasPrefix(request.URL.Path, invoicesPrefix):
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		s.invoiceStatus(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "not found")
	}
}

func (s *Server) authorized(request *http.Request) bool {
	if s.token == "" {
		return false
	}
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	if len(parts[1]) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.token)) == 1
}

func (s *Server) createWallet(writer http.ResponseWriter, request *http.Request) {
	info, err := s.wallet.Create(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, info)
}

func (s *Server) receiveAddress(writer http.ResponseWriter, request *http.Request) {
	info, err := s.wallet.ReceiveAddress(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, info)
}

func (s *Server) balance(writer http.ResponseWriter, request *http.Request) {
	balance, err := s.wallet.Balance(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, balance)
}

func (s *Server) operations(writer http.ResponseWriter, request *http.Request) {
	operations, err := s.wallet.Operations(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, operations)
}

const operationsPrefix = "/operations/"

func (s *Server) operation(writer http.ResponseWriter, request *http.Request) {
	rawID := strings.TrimPrefix(request.URL.Path, operationsPrefix)
	if rawID == "" || strings.Contains(rawID, "/") {
		writeError(writer, http.StatusBadRequest, "operation lookup needs one numeric id in the path")
		return
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		writeError(writer, http.StatusBadRequest, "operation lookup needs one positive numeric id in the path")
		return
	}
	operation, err := s.wallet.Operation(request.Context(), id)
	if errors.Is(err, wallet.ErrUnknownOperation) {
		writeError(writer, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, operation)
}

func (s *Server) receiveInvoice(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input struct {
		AmountSatoshis *int64 `json:"amountSatoshis"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid invoice request")
		return
	}
	invoice, err := s.wallet.ReceiveInvoice(request.Context(), input.AmountSatoshis)
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, invoice)
}

// invoicesPrefix is matched by prefix because this is the only route with a path parameter.
const invoicesPrefix = "/invoices/"

func (s *Server) invoiceStatus(writer http.ResponseWriter, request *http.Request) {
	paymentHash := strings.TrimPrefix(request.URL.Path, invoicesPrefix)
	if paymentHash == "" || strings.Contains(paymentHash, "/") {
		writeError(writer, http.StatusBadRequest, "invoice status needs one payment hash in the path")
		return
	}
	status, err := s.wallet.InvoiceStatus(request.Context(), paymentHash)
	if errors.Is(err, wallet.ErrUnknownPaymentHash) {
		writeError(writer, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) sync(writer http.ResponseWriter, request *http.Request) {
	result, err := s.wallet.Sync(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

const (
	sendStatusPrefix    = "/send/status/"
	sendReconcilePrefix = "/send/reconcile/"
)

func (s *Server) sendPreview(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input wallet.SendPreviewRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid send preview request")
		return
	}
	preview, err := s.wallet.PreviewSend(request.Context(), input)
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, preview)
}

func (s *Server) send(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input wallet.SendRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeSendError(writer, &wallet.SendError{Cause: errors.New("invalid send request")})
		return
	}
	outcome, err := s.wallet.SendWithOutcome(request.Context(), input)
	if err != nil {
		writeSendError(writer, err)
		return
	}
	if outcome.Replay {
		writer.Header().Set("Idempotent-Replay", "true")
		writeJSON(writer, http.StatusOK, outcome.Result)
		return
	}
	writeJSON(writer, http.StatusCreated, outcome.Result)
}

func (s *Server) sendStatus(writer http.ResponseWriter, request *http.Request) {
	key, ok := sendKeyFromPath(request, sendStatusPrefix)
	if !ok {
		writeError(writer, http.StatusBadRequest, "send status needs one idempotency key in the path")
		return
	}
	status, err := s.wallet.SendStatus(request.Context(), key)
	if errors.Is(err, wallet.ErrUnknownIdempotencyKey) {
		writeError(writer, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) reconcileSend(writer http.ResponseWriter, request *http.Request) {
	key, ok := sendKeyFromPath(request, sendReconcilePrefix)
	if !ok {
		writeError(writer, http.StatusBadRequest, "send reconciliation needs one idempotency key in the path")
		return
	}
	status, err := s.wallet.ReconcileSend(request.Context(), key)
	if errors.Is(err, wallet.ErrUnknownIdempotencyKey) {
		writeError(writer, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, wallet.ErrSendInProgress) {
		writeCodedError(writer, http.StatusConflict, err.Error(), "send_in_progress")
		return
	}
	if errors.Is(err, wallet.ErrHoustonRequestIDUnavailable) {
		writeCodedError(writer, http.StatusBadGateway, err.Error(), "submit_failed")
		return
	}
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func sendKeyFromPath(request *http.Request, prefix string) (string, bool) {
	escaped := strings.TrimPrefix(request.URL.EscapedPath(), prefix)
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", false
	}
	key, err := url.PathUnescape(escaped)
	return key, err == nil && key != ""
}

func writeSendError(writer http.ResponseWriter, err error) {
	if errors.Is(err, wallet.ErrSendInProgress) {
		writeCodedError(writer, http.StatusConflict, err.Error(), "send_in_progress")
		return
	}
	if errors.Is(err, wallet.ErrIdempotencyKeyReused) {
		writeCodedError(writer, http.StatusUnprocessableEntity, err.Error(), "idempotency_key_reused")
		return
	}
	var sendError *wallet.SendError
	if errors.As(err, &sendError) {
		if sendError.Submitted {
			writeCodedError(writer, http.StatusBadGateway, sendError.Error(), "submit_failed")
			return
		}
		if wallet.IsHoustonSessionError(sendError) {
			writeCodedError(writer, http.StatusServiceUnavailable, sendError.Error(), "houston_session")
			return
		}
	}
	writeCodedError(writer, http.StatusBadRequest, err.Error(), "validation")
}

func writeCodedError(writer http.ResponseWriter, status int, message, code string) {
	writeJSON(writer, status, map[string]string{"error": message, "code": code})
}

func (s *Server) setupRecovery(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input struct {
		RecoveryCode string `json:"recoveryCode"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.RecoveryCode) == "" {
		writeError(writer, http.StatusBadRequest, "invalid recovery setup request")
		return
	}
	result, err := s.wallet.SetupRecovery(request.Context(), input.RecoveryCode)
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (s *Server) emergencyKit(writer http.ResponseWriter, request *http.Request) {
	kit, err := s.wallet.EmergencyKit(request.Context())
	if err != nil {
		writeWalletError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, kit)
}

func writeWalletError(writer http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		writeError(writer, http.StatusRequestTimeout, err.Error())
		return
	}
	writeError(writer, http.StatusBadRequest, err.Error())
}

func methodNotAllowed(writer http.ResponseWriter) {
	writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
