package wallet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jinzhu/gorm"
	libwallet "github.com/muun/libwallet"
	"github.com/muun/libwallet/walletdb"
)

const (
	basePath   = "m/schema:1'/recovery:1'"
	addressDir = "external:1"
	walletFile = "wallet.json"
)

const (
	maxRouteHintFee  = int64(1<<32 - 1)
	maxRouteHintCLTV = int64(1<<16 - 1)
)

var invoicePoolMu sync.Mutex

// Config configures the single wallet stored in a data directory.
type Config struct {
	DataDir           string
	Passphrase        string
	HoustonURL        string
	PrimaryCurrency   string
	ClientVersion     int
	ClientVersionName string
}

// WalletInfo is the public result of creating a wallet.
type WalletInfo struct {
	Address      string
	AddressIndex int
}

// Balance is the wallet's current spendable amount in satoshis.
type Balance struct {
	Satoshis int64 `json:"satoshis"`
}

// Invoice is a BOLT11 payment request created for an incoming Lightning payment.
type Invoice struct {
	Invoice        string `json:"invoice"`
	PaymentHash    string `json:"paymentHash"`
	AmountSatoshis *int64 `json:"amountSatoshis,omitempty"`
}

// Invoice settlement states reported by InvoiceStatus.
const (
	// InvoiceStatePending means this wallet issued the invoice and Houston reports no payment yet.
	InvoiceStatePending = "pending"
	// InvoiceStateReceiving means the payment reached Houston but its incoming swap is not
	// fulfilled yet, so the preimage does not exist and the funds are not spendable. Sync, or the
	// poller, fulfills it.
	InvoiceStateReceiving = "receiving"
	// InvoiceStateSettled means the incoming swap is fulfilled and the preimage proves the payment.
	InvoiceStateSettled = "settled"
)

// ErrUnknownPaymentHash reports a payment hash this wallet never issued an invoice for.
var ErrUnknownPaymentHash = errors.New("unknown payment hash")

// InvoiceStatus reports whether the Lightning payment for one issued invoice settled.
//
// Settlement is derived from the preimage Houston publishes on the incoming swap, which is the
// only proof the payment completed. AmountSatoshis is what the invoice asked for and
// ReceivedSatoshis is what Houston reports, so a caller can assert the two agree.
type InvoiceStatus struct {
	PaymentHash      string `json:"paymentHash"`
	State            string `json:"state"`
	Settled          bool   `json:"settled"`
	Preimage         string `json:"preimage,omitempty"`
	AmountSatoshis   int64  `json:"amountSatoshis"`
	ReceivedSatoshis *int64 `json:"receivedSatoshis,omitempty"`
	OperationID      *int64 `json:"operationId,omitempty"`
}

// SyncResult reports incoming Lightning swaps that were fulfilled during one synchronization.
type SyncResult struct {
	FulfilledIncomingSwaps int `json:"fulfilledIncomingSwaps"`
}

// Operation is a stable, wallet-facing view of one Houston operation.
type Operation struct {
	ID              int64  `json:"id"`
	Direction       string `json:"direction,omitempty"`
	Status          string `json:"status"`
	AmountSatoshis  int64  `json:"amountSatoshis"`
	FeeSatoshis     int64  `json:"feeSatoshis"`
	Confirmations   int64  `json:"confirmations"`
	TransactionID   string `json:"transactionId,omitempty"`
	ReceiverAddress string `json:"receiverAddress,omitempty"`
	// PaymentHash and Preimage are only present on incoming Lightning swaps. A non-empty Preimage
	// proves the payment settled.
	PaymentHash string `json:"paymentHash,omitempty"`
	Preimage    string `json:"preimage,omitempty"`
}

// OperationDetail augments the list-operation shape with the normalized payment lifecycle used
// by consumers of GET /operations/{id}.
type OperationDetail struct {
	Operation
	PaymentState string `json:"paymentState"`
}

// Manager owns one encrypted Muun wallet in Config.DataDir.
type Manager struct {
	config                  Config
	client                  *http.Client
	invoicePool             *walletdb.Pool
	receiveMu               sync.Mutex
	sendMu                  sync.Mutex
	sendIdempotency         *sendIdempotencyStore
	recoveryMu              sync.Mutex
	signOutgoingTransaction outgoingTransactionSigner
}

type walletRecord struct {
	Version                 int    `json:"version"`
	EncryptedBasePrivateKey string `json:"encryptedBasePrivateKey"`
	BasePublicKey           string `json:"basePublicKey"`
	MuunCosigningPublicKey  string `json:"muunCosigningPublicKey"`
	SwapServerPublicKey     string `json:"swapServerPublicKey"`
	SessionToken            string `json:"sessionToken"`
	RecoveryCodePublicKey   string `json:"recoveryCodePublicKey,omitempty"`
	RecoveryCodeSalt        string `json:"recoveryCodeSalt,omitempty"`
	EmergencyKitUserKey     string `json:"emergencyKitUserKey,omitempty"`
	EmergencyKitMuunKey     string `json:"emergencyKitMuunKey,omitempty"`
	EmergencyKitMuunKeyFP   string `json:"emergencyKitMuunKeyFingerprint,omitempty"`
	MaxUsedAddressIndex     int    `json:"maxUsedAddressIndex"`
	MaxWatchingAddressIndex int    `json:"maxWatchingAddressIndex"`
}

type createSessionRequest struct {
	Client          clientPayload `json:"client"`
	PrimaryCurrency string        `json:"primaryCurrency"`
	BasePublicKey   publicKey     `json:"basePublicKey"`
}

type clientPayload struct {
	Type        string `json:"type"`
	BuildType   string `json:"buildType"`
	Version     int    `json:"version"`
	VersionName string `json:"versionName"`
	DeviceModel string `json:"deviceModel"`
	Language    string `json:"language"`
}

type publicKey struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type createSessionResponse struct {
	CosigningPublicKey  publicKey `json:"cosigningPublicKey"`
	SwapServerPublicKey publicKey `json:"swapServerPublicKey"`
}

type externalAddressesRecord struct {
	MaxUsedIndex     int  `json:"maxUsedIndex"`
	MaxWatchingIndex *int `json:"maxWatchingIndex,omitempty"`
}

type nextTransactionSize struct {
	SizeProgression   []sizeForAmount `json:"sizeProgression"`
	ExpectedDebtInSat *int64          `json:"expectedDebtInSat"`
}

type realtimeData struct {
	ForwardingPolicies []forwardingPolicy  `json:"forwardingPolicies"`
	FeeWindow          *feeWindow          `json:"feeWindow"`
	ExchangeRateWindow *exchangeRateWindow `json:"exchangeRateWindow"`
}

type feeWindow struct {
	ID               int64              `json:"id"`
	TargetedFees     map[string]float64 `json:"targetedFees"`
	FastConfTarget   int64              `json:"fastConfTarget"`
	MediumConfTarget int64              `json:"mediumConfTarget"`
	SlowConfTarget   int64              `json:"slowConfTarget"`
}

type exchangeRateWindow struct {
	ID    int64              `json:"id"`
	Rates map[string]float64 `json:"rates"`
}

type forwardingPolicy struct {
	IdentityKeyHex            string `json:"identityKeyHex"`
	FeeBaseMsat               int64  `json:"feeBaseMsat"`
	FeeProportionalMillionths int64  `json:"feeProportionalMillionths"`
	CLTVExpiryDelta           int64  `json:"cltvExpiryDelta"`
}

type userInvoice struct {
	PaymentHashHex string    `json:"paymentHashHex"`
	ShortChannelID int64     `json:"shortChannelId"`
	UserPublicKey  publicKey `json:"userPublicKey"`
	MuunPublicKey  publicKey `json:"muunPublicKey"`
	IdentityPubKey publicKey `json:"identityPubKey"`
}

type incomingSwapJSON struct {
	UUID                string            `json:"uuid"`
	PaymentHashHex      string            `json:"paymentHashHex"`
	HTLC                *incomingSwapHTLC `json:"htlc"`
	SphinxPacketHex     string            `json:"sphinxPacketHex"`
	CollectInSats       int64             `json:"collectInSats"`
	PaymentAmountInSats int64             `json:"paymentAmountInSats"`
	PreimageHex         string            `json:"preimageHex"`
}

type incomingSwapHTLC struct {
	ExpirationHeight       int64  `json:"expirationHeight"`
	HTLCTxHex              string `json:"htlcTxHex"`
	SwapServerPublicKeyHex string `json:"swapServerPublicKeyHex"`
}

type incomingSwapFulfillmentData struct {
	FulfillmentTxHex string `json:"fulfillmentTxHex"`
	MuunSignatureHex string `json:"muunSignatureHex"`
	OutputPath       string `json:"outputPath"`
	OutputVersion    int    `json:"outputVersion"`
}

type preimagePayload struct {
	Hex string `json:"hex"`
}

type rawTransactionPayload struct {
	Hex string `json:"hex"`
}

type sizeForAmount struct {
	AmountInSatoshis int64  `json:"amountInSatoshis"`
	SizeInBytes      int64  `json:"sizeInBytes"`
	Outpoint         string `json:"outpoint"`
	Status           string `json:"status"`
}

type operationJSON struct {
	ID              *int64            `json:"id"`
	RequestID       string            `json:"requestId,omitempty"`
	Direction       string            `json:"direction"`
	Status          string            `json:"status"`
	ReceiverAddress string            `json:"receiverAddress"`
	Amount          *satoshiAmount    `json:"amount"`
	Fee             *satoshiAmount    `json:"fee"`
	Transaction     *transaction      `json:"transaction"`
	IncomingSwap    *incomingSwapJSON `json:"incomingSwap"`
}

type moneyAmount struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type satoshiAmount struct {
	InSatoshis        *int64      `json:"inSatoshis"`
	InInputCurrency   moneyAmount `json:"inInputCurrency"`
	InPrimaryCurrency moneyAmount `json:"inPrimaryCurrency"`
}

type transaction struct {
	Hash          string `json:"hash"`
	Confirmations *int64 `json:"confirmations"`
}

// NewManager validates the configuration without touching the filesystem or network.
func NewManager(config Config) (*Manager, error) {
	if strings.TrimSpace(config.DataDir) == "" {
		return nil, errors.New("wallet data directory is required")
	}
	if config.Passphrase == "" {
		return nil, errors.New("wallet passphrase is required")
	}
	if strings.TrimSpace(config.HoustonURL) == "" {
		return nil, errors.New("Houston URL is required")
	}
	parsedURL, err := url.Parse(config.HoustonURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid Houston URL %q", config.HoustonURL)
	}
	if config.PrimaryCurrency == "" {
		config.PrimaryCurrency = "USD"
	}
	if config.ClientVersion == 0 {
		config.ClientVersion = 19020002
	}
	if config.ClientVersionName == "" {
		config.ClientVersionName = "2.16.0"
	}
	config.HoustonURL = strings.TrimRight(config.HoustonURL, "/") + "/"

	return &Manager{
		config:                  config,
		client:                  http.DefaultClient,
		sendIdempotency:         newSendIdempotencyStore(config.DataDir),
		signOutgoingTransaction: signPartiallySignedTransaction,
	}, nil
}

// DataDir returns the directory that contains the encrypted wallet record.
func (m *Manager) DataDir() string {
	return m.config.DataDir
}

// Create creates the only wallet allowed in the data directory and returns its first receive address.
func (m *Manager) Create(ctx context.Context) (WalletInfo, error) {
	if _, err := os.Stat(m.recordPath()); err == nil {
		record, err := m.loadRecord()
		if err != nil {
			return WalletInfo{}, err
		}
		if record.MaxUsedAddressIndex < 0 {
			return m.ReceiveAddress(ctx)
		}
		return WalletInfo{}, errors.New("wallet already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return WalletInfo{}, fmt.Errorf("inspect wallet record: %w", err)
	}

	basePrivateKey, err := newBasePrivateKey()
	if err != nil {
		return WalletInfo{}, err
	}

	session, err := m.createFirstSession(ctx, basePrivateKey.PublicKey())
	if err != nil {
		return WalletInfo{}, err
	}

	encryptedKey, err := libwallet.KeyEncrypt(basePrivateKey, m.config.Passphrase)
	if err != nil {
		return WalletInfo{}, fmt.Errorf("encrypt base private key: %w", err)
	}
	record := walletRecord{
		Version:                 1,
		EncryptedBasePrivateKey: encryptedKey,
		BasePublicKey:           basePrivateKey.PublicKey().String(),
		MuunCosigningPublicKey:  session.CosigningPublicKey.Key,
		SwapServerPublicKey:     session.SwapServerPublicKey.Key,
		SessionToken:            session.Token,
		MaxUsedAddressIndex:     -1,
		MaxWatchingAddressIndex: -1,
	}
	if err := m.saveRecord(record); err != nil {
		return WalletInfo{}, err
	}

	return m.ReceiveAddress(ctx)
}

// ReceiveAddress derives and reserves the next external Taproot address.
func (m *Manager) ReceiveAddress(ctx context.Context) (WalletInfo, error) {
	m.receiveMu.Lock()
	defer m.receiveMu.Unlock()

	record, err := m.loadRecord()
	if err != nil {
		return WalletInfo{}, err
	}

	remoteRecord, err := m.fetchExternalAddressesRecord(ctx, record.SessionToken)
	if err != nil {
		return WalletInfo{}, err
	}
	if remoteRecord.MaxWatchingIndex == nil {
		return WalletInfo{}, errors.New("Houston response has no maxWatchingIndex")
	}
	if *remoteRecord.MaxWatchingIndex < remoteRecord.MaxUsedIndex {
		return WalletInfo{}, fmt.Errorf("invalid Houston address window: used=%d watching=%d", remoteRecord.MaxUsedIndex, *remoteRecord.MaxWatchingIndex)
	}

	nextIndex := remoteRecord.MaxUsedIndex + 1
	if remoteRecord.MaxUsedIndex < 0 {
		nextIndex = 0
	}
	if nextIndex > *remoteRecord.MaxWatchingIndex {
		return WalletInfo{}, fmt.Errorf("Houston has not provisioned address index %d", nextIndex)
	}

	address, err := m.deriveAddress(record, nextIndex)
	if err != nil {
		return WalletInfo{}, err
	}

	if err := m.updateExternalAddressesRecord(ctx, record.SessionToken, nextIndex); err != nil {
		return WalletInfo{}, err
	}
	record.MaxUsedAddressIndex = nextIndex
	record.MaxWatchingAddressIndex = *remoteRecord.MaxWatchingIndex
	if err := m.saveRecord(record); err != nil {
		return WalletInfo{}, err
	}

	return WalletInfo{Address: address, AddressIndex: nextIndex}, nil
}

// Balance fetches Apollo's current spendable balance calculation from Houston.
func (m *Manager) Balance(ctx context.Context) (Balance, error) {
	record, err := m.loadRecord()
	if err != nil {
		return Balance{}, err
	}

	var nextSize nextTransactionSize
	if _, err := m.doJSON(
		ctx,
		http.MethodGet,
		"operations/next-transaction-size",
		record.SessionToken,
		nil,
		&nextSize,
	); err != nil {
		return Balance{}, fmt.Errorf("fetch next transaction size: %w", err)
	}

	if len(nextSize.SizeProgression) == 0 {
		return Balance{}, nil
	}
	utxoBalance := nextSize.SizeProgression[len(nextSize.SizeProgression)-1].AmountInSatoshis
	if utxoBalance < 0 {
		return Balance{}, fmt.Errorf("Houston returned a negative UTXO balance: %d", utxoBalance)
	}

	var expectedDebt int64
	if nextSize.ExpectedDebtInSat != nil && *nextSize.ExpectedDebtInSat > 0 {
		expectedDebt = *nextSize.ExpectedDebtInSat
	}
	if expectedDebt > utxoBalance {
		return Balance{}, fmt.Errorf("Houston returned debt greater than the UTXO balance: debt=%d balance=%d", expectedDebt, utxoBalance)
	}

	return Balance{Satoshis: utxoBalance - expectedDebt}, nil
}

// Operations returns the current operation history reported by Houston.
func (m *Manager) Operations(ctx context.Context) ([]Operation, error) {
	record, err := m.loadRecord()
	if err != nil {
		return nil, err
	}

	var remote []operationJSON
	if _, err := m.doJSON(ctx, http.MethodGet, "operations", record.SessionToken, nil, &remote); err != nil {
		return nil, fmt.Errorf("fetch operations: %w", err)
	}

	operations := make([]Operation, 0, len(remote))
	for index, remoteOperation := range remote {
		operation, err := mapOperation(remoteOperation)
		if err != nil {
			return nil, fmt.Errorf("map operation %d: %w", index, err)
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

// Sync fulfills unfulfilled incoming Lightning swaps visible in Houston's operation history.
func (m *Manager) Sync(ctx context.Context) (SyncResult, error) {
	// libwallet uses a process-global SQLite pool for both invoice validation and fulfillment.
	invoicePoolMu.Lock()
	defer invoicePoolMu.Unlock()

	record, err := m.loadRecord()
	if err != nil {
		return SyncResult{}, err
	}
	if err := m.ensureInvoicePool(); err != nil {
		return SyncResult{}, err
	}

	var operations []operationJSON
	if _, err := m.doJSON(ctx, http.MethodGet, "operations", record.SessionToken, nil, &operations); err != nil {
		return SyncResult{}, fmt.Errorf("fetch operations for sync: %w", err)
	}

	result := SyncResult{}
	for index, operation := range operations {
		if operation.IncomingSwap == nil || operation.IncomingSwap.PreimageHex != "" {
			continue
		}
		if err := m.fulfillIncomingSwap(ctx, record, *operation.IncomingSwap); err != nil {
			if isIncomingSwapAlreadyFulfilled(err) {
				continue
			}
			return SyncResult{}, fmt.Errorf("fulfill incoming swap in operation %d: %w", index, err)
		}
		result.FulfilledIncomingSwaps++
	}
	return result, nil
}

func (m *Manager) fulfillIncomingSwap(ctx context.Context, record walletRecord, remote incomingSwapJSON) error {
	swap, err := incomingSwapFromJSON(remote)
	if err != nil {
		return err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return err
	}
	if err := swap.VerifyFulfillable(basePrivateKey, libwallet.Mainnet()); err != nil {
		return fmt.Errorf("verify incoming swap: %w", err)
	}

	if remote.HTLC == nil {
		fulfillment, err := swap.FulfillFullDebt()
		if err != nil {
			return fmt.Errorf("fulfill full debt: %w", err)
		}
		if _, err := m.doJSON(
			ctx,
			http.MethodPut,
			"incoming-swaps/"+remote.UUID,
			record.SessionToken,
			preimagePayload{Hex: hex.EncodeToString(fulfillment.Preimage)},
			nil,
		); err != nil {
			return fmt.Errorf("send incoming swap preimage: %w", err)
		}
		return nil
	}

	muunKey, err := libwallet.NewHDPublicKeyFromString(record.MuunCosigningPublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		return fmt.Errorf("decode Muun cosigning key: %w", err)
	}
	fulfillmentData, err := m.fetchIncomingSwapFulfillmentData(ctx, record.SessionToken, remote.UUID)
	if err != nil {
		return err
	}
	fulfillment, err := swap.Fulfill(fulfillmentData, basePrivateKey, muunKey, libwallet.Mainnet())
	if err != nil {
		return fmt.Errorf("sign incoming swap fulfillment: %w", err)
	}
	if _, err := m.doJSON(
		ctx,
		http.MethodPut,
		"incoming-swaps/"+remote.UUID+"/fulfillment",
		record.SessionToken,
		rawTransactionPayload{Hex: hex.EncodeToString(fulfillment.FulfillmentTx)},
		nil,
	); err != nil {
		return fmt.Errorf("push incoming swap fulfillment: %w", err)
	}
	return nil
}

func incomingSwapFromJSON(remote incomingSwapJSON) (*libwallet.IncomingSwap, error) {
	if remote.UUID == "" {
		return nil, errors.New("incoming swap has no UUID")
	}
	if remote.PaymentAmountInSats < 0 || remote.CollectInSats < 0 {
		return nil, errors.New("incoming swap has a negative amount")
	}
	paymentHash, err := hex.DecodeString(remote.PaymentHashHex)
	if err != nil || len(paymentHash) != 32 {
		return nil, errors.New("incoming swap has an invalid payment hash")
	}
	sphinxPacket, err := hex.DecodeString(remote.SphinxPacketHex)
	if err != nil {
		return nil, errors.New("incoming swap has an invalid sphinx packet")
	}
	swap := &libwallet.IncomingSwap{
		PaymentHash:      paymentHash,
		SphinxPacket:     sphinxPacket,
		PaymentAmountSat: remote.PaymentAmountInSats,
		CollectSat:       remote.CollectInSats,
	}
	if remote.HTLC == nil {
		return swap, nil
	}
	if remote.HTLC.ExpirationHeight < 0 {
		return nil, errors.New("incoming swap HTLC has a negative expiration height")
	}
	htlcTransaction, err := hex.DecodeString(remote.HTLC.HTLCTxHex)
	if err != nil || len(htlcTransaction) == 0 {
		return nil, errors.New("incoming swap HTLC has an invalid transaction")
	}
	swapServerKey, err := hex.DecodeString(remote.HTLC.SwapServerPublicKeyHex)
	if err != nil || len(swapServerKey) == 0 {
		return nil, errors.New("incoming swap HTLC has an invalid swap server key")
	}
	swap.Htlc = &libwallet.IncomingSwapHtlc{
		HtlcTx:              htlcTransaction,
		ExpirationHeight:    remote.HTLC.ExpirationHeight,
		SwapServerPublicKey: swapServerKey,
	}
	return swap, nil
}

func (m *Manager) fetchIncomingSwapFulfillmentData(
	ctx context.Context,
	token string,
	uuid string,
) (*libwallet.IncomingSwapFulfillmentData, error) {
	var remote incomingSwapFulfillmentData
	if _, err := m.doJSON(ctx, http.MethodPost, "incoming-swaps/"+uuid+"/fulfillment", token, nil, &remote); err != nil {
		return nil, fmt.Errorf("fetch incoming swap fulfillment data: %w", err)
	}
	transaction, err := hex.DecodeString(remote.FulfillmentTxHex)
	if err != nil || len(transaction) == 0 {
		return nil, errors.New("Houston returned an invalid incoming swap fulfillment transaction")
	}
	muunSignature, err := hex.DecodeString(remote.MuunSignatureHex)
	if err != nil || len(muunSignature) == 0 {
		return nil, errors.New("Houston returned an invalid incoming swap signature")
	}
	return &libwallet.IncomingSwapFulfillmentData{
		FulfillmentTx: transaction,
		MuunSignature: muunSignature,
		OutputPath:    remote.OutputPath,
		OutputVersion: remote.OutputVersion,
	}, nil
}

func isIncomingSwapAlreadyFulfilled(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "incoming swap is already fulfilled")
}

// ReceiveInvoice creates a BOLT11 invoice using secrets registered with Houston.
func (m *Manager) ReceiveInvoice(ctx context.Context, amountSatoshis *int64) (Invoice, error) {
	if amountSatoshis != nil && *amountSatoshis <= 0 {
		return Invoice{}, errors.New("invoice amount must be positive")
	}

	// libwallet keeps this database in a process-global variable. The wrapper supports one
	// wallet, so serializing invoice work keeps that global pointed to this wallet's database.
	invoicePoolMu.Lock()
	defer invoicePoolMu.Unlock()

	record, err := m.loadRecord()
	if err != nil {
		return Invoice{}, err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return Invoice{}, err
	}
	muunKey, err := libwallet.NewHDPublicKeyFromString(record.MuunCosigningPublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		return Invoice{}, fmt.Errorf("decode Muun cosigning key: %w", err)
	}
	if err := m.ensureInvoicePool(); err != nil {
		return Invoice{}, err
	}

	policies, err := m.fetchForwardingPolicies(ctx, record.SessionToken)
	if err != nil {
		return Invoice{}, err
	}
	secrets, err := libwallet.GenerateInvoiceSecrets(basePrivateKey.PublicKey(), muunKey)
	if err != nil {
		return Invoice{}, fmt.Errorf("generate invoice secrets: %w", err)
	}
	if err := m.registerInvoiceSecrets(ctx, record.SessionToken, secrets); err != nil {
		return Invoice{}, err
	}
	if err := libwallet.PersistInvoiceSecrets(secrets); err != nil {
		return Invoice{}, fmt.Errorf("persist invoice secrets: %w", err)
	}

	builder := (&libwallet.InvoiceBuilder{}).Network(libwallet.Mainnet()).UserKey(basePrivateKey)
	for _, policy := range policies {
		builder.AddRouteHints(&libwallet.RouteHints{
			Pubkey:                    policy.IdentityKeyHex,
			FeeBaseMsat:               policy.FeeBaseMsat,
			FeeProportionalMillionths: policy.FeeProportionalMillionths,
			CltvExpiryDelta:           int32(policy.CLTVExpiryDelta),
		})
	}
	if amountSatoshis != nil {
		builder.AmountSat(*amountSatoshis)
	}
	encoded, err := builder.Build()
	if err != nil {
		return Invoice{}, fmt.Errorf("build BOLT11 invoice: %w", err)
	}
	if encoded == "" {
		return Invoice{}, errors.New("no registered invoice secrets are available")
	}

	// The payment hash is the key callers use to check settlement later. Read it back from the
	// invoice that was just built instead of guessing which pooled secret the builder consumed.
	decoded, err := libwallet.ParseInvoice(encoded, libwallet.Mainnet())
	if err != nil {
		return Invoice{}, fmt.Errorf("decode the invoice that was just built: %w", err)
	}

	result := Invoice{Invoice: encoded, PaymentHash: hex.EncodeToString(decoded.PaymentHash)}
	if amountSatoshis != nil {
		amount := *amountSatoshis
		result.AmountSatoshis = &amount
	}
	return result, nil
}

// InvoiceStatus reports whether the invoice for paymentHashHex was paid.
//
// It answers only for invoices this wallet issued: the local invoice database is the ownership
// check, so an unknown hash returns ErrUnknownPaymentHash instead of an indefinite "not paid".
// Settlement itself comes from Houston, because the preimage is the only proof of payment.
func (m *Manager) InvoiceStatus(ctx context.Context, paymentHashHex string) (InvoiceStatus, error) {
	normalized := strings.ToLower(strings.TrimSpace(paymentHashHex))
	paymentHash, err := hex.DecodeString(normalized)
	if err != nil || len(paymentHash) != 32 {
		return InvoiceStatus{}, errors.New("payment hash must be 32 bytes in hexadecimal")
	}

	record, err := m.loadRecord()
	if err != nil {
		return InvoiceStatus{}, err
	}

	local, err := m.findLocalInvoice(paymentHash)
	if err != nil {
		return InvoiceStatus{}, err
	}

	var remote []operationJSON
	if _, err := m.doJSON(ctx, http.MethodGet, "operations", record.SessionToken, nil, &remote); err != nil {
		return InvoiceStatus{}, fmt.Errorf("fetch operations for invoice status: %w", err)
	}

	status := InvoiceStatus{
		PaymentHash:    normalized,
		State:          InvoiceStatePending,
		AmountSatoshis: local.AmountSat,
	}
	for _, operation := range remote {
		if operation.IncomingSwap == nil ||
			!strings.EqualFold(operation.IncomingSwap.PaymentHashHex, normalized) {
			continue
		}
		status.State = InvoiceStateReceiving
		if operation.ID != nil {
			id := *operation.ID
			status.OperationID = &id
		}
		if operation.Amount != nil && operation.Amount.InSatoshis != nil {
			received := *operation.Amount.InSatoshis
			status.ReceivedSatoshis = &received
		}
		if operation.IncomingSwap.PreimageHex != "" {
			status.State = InvoiceStateSettled
			status.Settled = true
			status.Preimage = operation.IncomingSwap.PreimageHex
		}
		break
	}
	return status, nil
}

// findLocalInvoice reads one invoice secret from the wallet database. It takes invoicePoolMu
// because ensureInvoicePool repoints libwallet's process-global pool, so an unsynchronized read
// can race an in-flight ReceiveInvoice or Sync. The Houston request stays outside this lock.
func (m *Manager) findLocalInvoice(paymentHash []byte) (*walletdb.Invoice, error) {
	invoicePoolMu.Lock()
	defer invoicePoolMu.Unlock()

	if err := m.ensureInvoicePool(); err != nil {
		return nil, err
	}

	var found *walletdb.Invoice
	err := m.invoicePool.WithDB(func(db *walletdb.DB) error {
		invoice, err := db.FindByPaymentHash(paymentHash)
		if err != nil {
			return err
		}
		found = invoice
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUnknownPaymentHash
	}
	if err != nil {
		return nil, fmt.Errorf("look up invoice secret: %w", err)
	}
	return found, nil
}

func (m *Manager) ensureInvoicePool() error {
	if m.invoicePool == nil {
		if err := os.MkdirAll(m.config.DataDir, 0o700); err != nil {
			return fmt.Errorf("create wallet data directory: %w", err)
		}
		pool, err := walletdb.NewPool(filepath.Join(m.config.DataDir, "wallet.db"), nil)
		if err != nil {
			return fmt.Errorf("open invoice database: %w", err)
		}
		m.invoicePool = pool
	}
	libwallet.Pool = m.invoicePool
	return nil
}

func (m *Manager) fetchForwardingPolicies(ctx context.Context, token string) ([]forwardingPolicy, error) {
	var realtime realtimeData
	if _, err := m.doJSON(ctx, http.MethodGet, "realtime", token, nil, &realtime); err != nil {
		return nil, fmt.Errorf("fetch realtime data: %w", err)
	}
	if len(realtime.ForwardingPolicies) == 0 {
		return nil, errors.New("Houston returned no Lightning forwarding policies")
	}
	for index, policy := range realtime.ForwardingPolicies {
		if policy.IdentityKeyHex == "" {
			return nil, fmt.Errorf("forwarding policy %d has no identity key", index)
		}
		if policy.FeeBaseMsat < 0 || policy.FeeBaseMsat > maxRouteHintFee ||
			policy.FeeProportionalMillionths < 0 || policy.FeeProportionalMillionths > maxRouteHintFee ||
			policy.CLTVExpiryDelta < 0 || policy.CLTVExpiryDelta > maxRouteHintCLTV {
			return nil, fmt.Errorf("forwarding policy %d has out-of-range values", index)
		}
	}
	return realtime.ForwardingPolicies, nil
}

func (m *Manager) registerInvoiceSecrets(ctx context.Context, token string, secrets *libwallet.InvoiceSecretsList) error {
	invoices := make([]userInvoice, 0, secrets.Length())
	for index := 0; index < secrets.Length(); index++ {
		secret := secrets.Get(index)
		invoices = append(invoices, userInvoice{
			PaymentHashHex: hex.EncodeToString(secret.PaymentHash),
			ShortChannelID: secret.ShortChanId,
			UserPublicKey: publicKey{
				Key:  secret.UserHtlcKey.String(),
				Path: secret.UserHtlcKey.Path,
			},
			MuunPublicKey: publicKey{
				Key:  secret.MuunHtlcKey.String(),
				Path: secret.MuunHtlcKey.Path,
			},
			IdentityPubKey: publicKey{
				Key:  secret.IdentityKey.String(),
				Path: secret.IdentityKey.Path,
			},
		})
	}
	if _, err := m.doJSON(ctx, http.MethodPost, "incoming-swaps/invoices", token, invoices, nil); err != nil {
		return fmt.Errorf("register invoice secrets: %w", err)
	}
	return nil
}

// mapPaymentState follows Apollo's outgoing-operation classification. Unknown future
// Houston statuses remain pending rather than being misreported as paid or failed.
func mapPaymentState(status string) string {
	switch strings.ToUpper(status) {
	case "CONFIRMED", "SETTLED", "SWAP_PAYED":
		return "paid"
	case "DROPPED", "FAILED", "SWAP_FAILED":
		return "failed"
	case "SWAP_EXPIRED":
		return "expired"
	case "CREATED", "SIGNING", "SIGNED", "BROADCASTED", "SWAP_PENDING", "SWAP_ROUTING", "SWAP_OPENING_CHANNEL", "SWAP_WAITING_CHANNEL":
		return "pending"
	default:
		return "pending"
	}
}

func mapOperation(remote operationJSON) (Operation, error) {
	if remote.ID == nil {
		return Operation{}, errors.New("Houston operation has no id")
	}
	if remote.Status == "" {
		return Operation{}, errors.New("Houston operation has no status")
	}
	if remote.Amount == nil || remote.Amount.InSatoshis == nil {
		return Operation{}, errors.New("Houston operation has no amount in satoshis")
	}
	if remote.Fee == nil || remote.Fee.InSatoshis == nil {
		return Operation{}, errors.New("Houston operation has no fee in satoshis")
	}
	if *remote.Amount.InSatoshis < 0 || *remote.Fee.InSatoshis < 0 {
		return Operation{}, errors.New("Houston operation has a negative amount or fee")
	}

	operation := Operation{
		ID:              *remote.ID,
		Direction:       remote.Direction,
		Status:          remote.Status,
		AmountSatoshis:  *remote.Amount.InSatoshis,
		FeeSatoshis:     *remote.Fee.InSatoshis,
		ReceiverAddress: remote.ReceiverAddress,
	}
	if remote.Transaction != nil {
		operation.TransactionID = remote.Transaction.Hash
		if remote.Transaction.Confirmations != nil {
			operation.Confirmations = *remote.Transaction.Confirmations
		}
	}
	if remote.IncomingSwap != nil {
		operation.PaymentHash = remote.IncomingSwap.PaymentHashHex
		operation.Preimage = remote.IncomingSwap.PreimageHex
	}
	return operation, nil
}

func newBasePrivateKey() (*libwallet.HDPrivateKey, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate wallet seed: %w", err)
	}
	root, err := libwallet.NewHDPrivateKey(seed, libwallet.Mainnet())
	if err != nil {
		return nil, fmt.Errorf("create root private key: %w", err)
	}
	base, err := root.DeriveTo(basePath)
	if err != nil {
		return nil, fmt.Errorf("derive Muun base key: %w", err)
	}
	return base, nil
}

type sessionResult struct {
	CosigningPublicKey  publicKey
	SwapServerPublicKey publicKey
	Token               string
}

func (m *Manager) createFirstSession(ctx context.Context, basePublicKey *libwallet.HDPublicKey) (sessionResult, error) {
	payload := createSessionRequest{
		Client: clientPayload{
			Type:        "FALCON",
			BuildType:   "release",
			Version:     m.config.ClientVersion,
			VersionName: m.config.ClientVersionName,
			DeviceModel: "muun-wrapper",
			Language:    "en",
		},
		PrimaryCurrency: m.config.PrimaryCurrency,
		BasePublicKey: publicKey{
			Key:  basePublicKey.String(),
			Path: basePublicKey.Path,
		},
	}

	var response createSessionResponse
	token, err := m.doJSON(ctx, http.MethodPost, "sessions-v2/first", "", payload, &response)
	if err != nil {
		return sessionResult{}, fmt.Errorf("create first session: %w", err)
	}
	if response.CosigningPublicKey.Key == "" || response.CosigningPublicKey.Path == "" {
		return sessionResult{}, errors.New("Houston response has no cosigning public key")
	}
	if response.SwapServerPublicKey.Key == "" || response.SwapServerPublicKey.Path == "" {
		return sessionResult{}, errors.New("Houston response has no swap server public key")
	}
	if token == "" {
		return sessionResult{}, errors.New("Houston did not return a session token")
	}
	return sessionResult{
		CosigningPublicKey:  response.CosigningPublicKey,
		SwapServerPublicKey: response.SwapServerPublicKey,
		Token:               token,
	}, nil
}

func (m *Manager) fetchExternalAddressesRecord(ctx context.Context, token string) (externalAddressesRecord, error) {
	var record externalAddressesRecord
	if _, err := m.doJSON(ctx, http.MethodGet, "user/external-addresses-record", token, nil, &record); err != nil {
		return externalAddressesRecord{}, fmt.Errorf("fetch external address record: %w", err)
	}
	return record, nil
}

func (m *Manager) updateExternalAddressesRecord(ctx context.Context, token string, maxUsedIndex int) error {
	_, err := m.doJSON(
		ctx,
		http.MethodPut,
		"user/external-addresses-record",
		token,
		externalAddressesRecord{MaxUsedIndex: maxUsedIndex},
		&externalAddressesRecord{},
	)
	if err != nil {
		return fmt.Errorf("update external address record: %w", err)
	}
	return nil
}

func (m *Manager) deriveAddress(record walletRecord, index int) (string, error) {
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return "", err
	}
	muunKey, err := libwallet.NewHDPublicKeyFromString(record.MuunCosigningPublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		return "", fmt.Errorf("decode Muun cosigning key: %w", err)
	}

	path := basePath + "/" + addressDir + "/" + strconv.Itoa(index)
	userKey, err := basePrivateKey.PublicKey().DeriveTo(path)
	if err != nil {
		return "", fmt.Errorf("derive user address key: %w", err)
	}
	muunAddressKey, err := muunKey.DeriveTo(path)
	if err != nil {
		return "", fmt.Errorf("derive Muun address key: %w", err)
	}
	address, err := libwallet.CreateAddressV5(userKey, muunAddressKey)
	if err != nil {
		return "", fmt.Errorf("derive Muun address: %w", err)
	}
	return address.Address(), nil
}

func (m *Manager) decryptBasePrivateKey(record walletRecord) (*libwallet.HDPrivateKey, error) {
	decrypted, err := libwallet.KeyDecrypt(record.EncryptedBasePrivateKey, m.config.Passphrase, libwallet.Mainnet())
	if err != nil {
		return nil, fmt.Errorf("decrypt base private key: %w", err)
	}
	if decrypted.Path != basePath {
		return nil, fmt.Errorf("unexpected base private key path %q", decrypted.Path)
	}
	return decrypted.Key, nil
}

func (m *Manager) doJSON(
	ctx context.Context,
	method string,
	path string,
	token string,
	payload any,
	response any,
) (string, error) {
	endpoint, err := url.Parse(m.config.HoustonURL)
	if err != nil {
		return "", err
	}
	relative, err := url.Parse(path)
	if err != nil {
		return "", err
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", fmt.Errorf("encode request: %w", err)
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.ResolveReference(relative).String(), body)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Client-Version", strconv.Itoa(m.config.ClientVersion))
	request.Header.Set("X-Client-Version-Name", m.config.ClientVersionName)
	request.Header.Set("X-Client-Type", "FALCON")
	request.Header.Set("X-Client-Language", "en")
	request.Header.Set("X-Idempotency-Key", uuid.NewString())
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	result, err := m.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request Houston: %w", err)
	}
	defer result.Body.Close()

	if result.StatusCode < http.StatusOK || result.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(result.Body, 64<<10))
		return "", &HoustonError{
			StatusCode: result.StatusCode,
			Message:    fmt.Sprintf("Houston returned %s: %s", result.Status, strings.TrimSpace(string(message))),
		}
	}
	if response != nil && result.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(result.Body).Decode(response); err != nil {
			return "", fmt.Errorf("decode Houston response: %w", err)
		}
	}

	return bearerToken(result.Header.Get("Authorization")), nil
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func (m *Manager) recordPath() string {
	return filepath.Join(m.config.DataDir, walletFile)
}

func (m *Manager) loadRecord() (walletRecord, error) {
	contents, err := os.ReadFile(m.recordPath())
	if err != nil {
		return walletRecord{}, fmt.Errorf("read wallet record: %w", err)
	}
	var record walletRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return walletRecord{}, fmt.Errorf("decode wallet record: %w", err)
	}
	if record.Version != 1 || record.EncryptedBasePrivateKey == "" || record.SessionToken == "" {
		return walletRecord{}, errors.New("invalid wallet record")
	}
	return record, nil
}

func (m *Manager) saveRecord(record walletRecord) error {
	if err := os.MkdirAll(m.config.DataDir, 0o700); err != nil {
		return fmt.Errorf("create wallet data directory: %w", err)
	}
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode wallet record: %w", err)
	}
	temporary, err := os.CreateTemp(m.config.DataDir, ".wallet-*.json")
	if err != nil {
		return fmt.Errorf("create temporary wallet record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set wallet record permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write wallet record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close wallet record: %w", err)
	}
	if err := os.Rename(temporaryPath, m.recordPath()); err != nil {
		return fmt.Errorf("replace wallet record: %w", err)
	}
	return nil
}
