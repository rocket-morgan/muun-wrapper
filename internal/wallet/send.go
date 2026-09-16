package wallet

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/google/uuid"
	libwallet "github.com/muun/libwallet"
	"github.com/muun/libwallet/fees"
	"github.com/muun/libwallet/operation"
)

// SendRequest describes one on-chain payment to submit through Houston.
type SendRequest struct {
	Destination    string `json:"destination"`
	AmountSatoshis int64  `json:"amountSatoshis"`
	Description    string `json:"description,omitempty"`
}

// SendResult identifies the operation and transaction accepted by Houston.
type SendResult struct {
	OperationID    int64  `json:"operationId"`
	TransactionID  string `json:"transactionId"`
	AmountSatoshis int64  `json:"amountSatoshis"`
	FeeSatoshis    int64  `json:"feeSatoshis"`
}

type outgoingTransactionSigner func(
	basePrivateKey *libwallet.HDPrivateKey,
	muunPublicKey *libwallet.HDPublicKey,
	partial partiallySignedTransactionJSON,
	nonces *libwallet.MusigNonces,
	expectations *libwallet.SigningExpectations,
) (*libwallet.Transaction, error)

type outgoingOperationRequest struct {
	RequestID                string        `json:"requestId"`
	Direction                string        `json:"direction,omitempty"`
	IsExternal               bool          `json:"isExternal"`
	SenderIsExternal         bool          `json:"senderIsExternal"`
	ReceiverIsExternal       bool          `json:"receiverIsExternal"`
	ReceiverAddress          string        `json:"receiverAddress"`
	Amount                   satoshiAmount `json:"amount"`
	Fee                      satoshiAmount `json:"fee"`
	OutputAmountInSatoshis   int64         `json:"outputAmountInSatoshis"`
	ExchangeRatesWindowID    int64         `json:"exchangeRatesWindowId"`
	Description              string        `json:"description,omitempty"`
	Status                   string        `json:"status"`
	Outpoints                []string      `json:"outpoints"`
	MarkItAsReplaceableByFee bool          `json:"markItAsReplaceableByFee"`
	UserPublicNoncesHex      []string      `json:"userPublicNoncesHex"`
	SwapUUID                 string        `json:"swapUuid,omitempty"`
}

type operationCreatedJSON struct {
	Operation                  operationJSON                    `json:"operation"`
	PartiallySignedTransaction *partiallySignedTransactionJSON  `json:"partiallySignedTransaction"`
	AlternativeTransactions    []partiallySignedTransactionJSON `json:"alternativeTransactions"`
	ChangeAddress              *muunAddressJSON                 `json:"changeAddress"`
}

type partiallySignedTransactionJSON struct {
	HexTransaction string                     `json:"hexTransaction"`
	Inputs         []partiallySignedInputJSON `json:"inputs"`
}

type incomingSwapInputJSON struct {
	SphinxHex              string `json:"sphinxHex"`
	HtlcTxHex              string `json:"htlcTxHex"`
	SwapServerPublicKeyHex string `json:"swapServerPublicKeyHex"`
	PaymentHash256Hex      string `json:"paymentHash256Hex"`
	ExpirationHeight       int64  `json:"expirationHeight"`
	CollectInSats          int64  `json:"collectInSats"`
	PreimageHex            string `json:"preimageHex,omitempty"`
	HtlcOutputKeyPath      string `json:"htlcOutputKeyPath"`
}

type partiallySignedInputJSON struct {
	PrevOut               previousOutputJSON     `json:"prevOut"`
	Address               muunAddressJSON        `json:"address"`
	UserSignature         *signatureJSON         `json:"userSignature"`
	MuunSignature         *signatureJSON         `json:"muunSignature"`
	RawMuunPublicNonceHex string                 `json:"rawMuunPublicNonceHex"`
	IncomingSwap          *incomingSwapInputJSON `json:"incomingSwap,omitempty"`
}

type previousOutputJSON struct {
	TxID   string `json:"txId"`
	Index  int    `json:"index"`
	Amount int64  `json:"amount"`
}

type muunAddressJSON struct {
	Version        int    `json:"version"`
	DerivationPath string `json:"derivationPath"`
	Address        string `json:"address"`
}

type signatureJSON struct {
	Hex string `json:"hex"`
}

type pushTransactionsRequest struct {
	RawTransaction          *rawTransactionPayload  `json:"rawTransaction"`
	AlternativeTransactions []rawTransactionPayload `json:"alternativeTransactions"`
}

const submarineSwapExpirationInBlocks = int64(1008)

type submarineSwapRequest struct {
	Invoice                string `json:"invoice"`
	SwapExpirationInBlocks int64  `json:"swapExpirationInBlocks"`
	Origin                 string `json:"origin"`
	BackgroundTimes        []any  `json:"bkgTimes"`
}

type submarineSwapJSON struct {
	SwapUUID                       string                         `json:"swapUuid"`
	RawInvoice                     string                         `json:"invoice"`
	ReceiverData                   submarineSwapReceiverJSON      `json:"receiver"`
	FundingOutputData              submarineSwapFundingOutputJSON `json:"fundingOutput"`
	Fees                           *submarineSwapFeesJSON         `json:"fees"`
	MaxAlternativeTransactionCount *int                           `json:"maxAlternativeTransactionCount,omitempty"`
}

type submarineSwapReceiverJSON struct {
	Alias     string `json:"alias"`
	PublicKey string `json:"publicKey"`
}

type submarineSwapFundingOutputJSON struct {
	OutputAddress          string           `json:"outputAddress"`
	OutputAmountInSatoshis *int64           `json:"outputAmountInSatoshis"`
	ConfirmationsNeeded    *int             `json:"confirmationsNeeded"`
	UserLockTime           *int64           `json:"userLockTime"`
	ExpirationInBlocks     *int64           `json:"expirationInBlocks"`
	UserRefundAddress      *muunAddressJSON `json:"userRefundAddress"`
	UserPublicKey          *publicKey       `json:"userPublicKey"`
	MuunPublicKey          *publicKey       `json:"muunPublicKey"`
	ServerPaymentHashInHex string           `json:"serverPaymentHashInHex"`
	ServerPublicKeyInHex   string           `json:"serverPublicKeyInHex"`
	ScriptVersion          int64            `json:"scriptVersion"`
	DebtType               string           `json:"debtType"`
	DebtAmountInSats       *int64           `json:"debtAmountInSats"`
}

type submarineSwapFeesJSON struct {
	LightningInSats int64 `json:"lightningInSats"`
	SweepInSats     int64 `json:"sweepInSats"`
}

type walletAddress struct {
	version        int
	derivationPath string
	address        string
}

func (a walletAddress) Version() int           { return a.version }
func (a walletAddress) DerivationPath() string { return a.derivationPath }
func (a walletAddress) Address() string        { return a.address }

type walletOutpoint struct {
	txID   []byte
	index  int
	amount int64
}

func (o walletOutpoint) TxId() []byte  { return o.txID }
func (o walletOutpoint) Index() int    { return o.index }
func (o walletOutpoint) Amount() int64 { return o.amount }

type walletIncomingSwap struct {
	sphinx              []byte
	htlcTx              []byte
	paymentHash         []byte
	swapServerPublicKey string
	expirationHeight    int64
	collectInSats       int64
	preimage            []byte
	htlcOutputKeyPath   string
}

func (s *walletIncomingSwap) Sphinx() []byte              { return s.sphinx }
func (s *walletIncomingSwap) HtlcTx() []byte              { return s.htlcTx }
func (s *walletIncomingSwap) PaymentHash256() []byte      { return s.paymentHash }
func (s *walletIncomingSwap) SwapServerPublicKey() string { return s.swapServerPublicKey }
func (s *walletIncomingSwap) ExpirationHeight() int64     { return s.expirationHeight }
func (s *walletIncomingSwap) CollectInSats() int64        { return s.collectInSats }
func (s *walletIncomingSwap) Preimage() []byte            { return s.preimage }
func (s *walletIncomingSwap) HtlcOutputKeyPath() string   { return s.htlcOutputKeyPath }

type walletInput struct {
	outpoint        walletOutpoint
	address         walletAddress
	userSignature   []byte
	muunSignature   []byte
	muunPublicNonce []byte
	incomingSwap    *walletIncomingSwap
}

func (i walletInput) OutPoint() libwallet.Outpoint                    { return i.outpoint }
func (i walletInput) Address() libwallet.MuunAddress                  { return i.address }
func (i walletInput) UserSignature() []byte                           { return i.userSignature }
func (i walletInput) MuunSignature() []byte                           { return i.muunSignature }
func (i walletInput) SubmarineSwapV1() libwallet.InputSubmarineSwapV1 { return nil }
func (i walletInput) SubmarineSwapV2() libwallet.InputSubmarineSwapV2 { return nil }
func (i walletInput) IncomingSwap() libwallet.InputIncomingSwap {
	if i.incomingSwap == nil {
		return nil
	}
	return i.incomingSwap
}
func (i walletInput) MuunPublicNonce() []byte { return i.muunPublicNonce }

// Send creates, signs, and broadcasts an on-chain payment or a Lightning submarine swap.
func (m *Manager) Send(ctx context.Context, request SendRequest) (SendResult, error) {
	if strings.TrimSpace(request.Destination) == "" {
		return SendResult{}, errors.New("payment destination is required")
	}
	if request.AmountSatoshis <= 0 {
		return SendResult{}, errors.New("payment amount must be positive")
	}

	m.sendMu.Lock()
	defer m.sendMu.Unlock()

	if invoice, err := libwallet.ParseInvoice(request.Destination, libwallet.Mainnet()); err == nil {
		return m.sendLightning(ctx, request, invoice)
	}

	uri, err := libwallet.GetPaymentURI(request.Destination, libwallet.Mainnet())
	if err != nil {
		return SendResult{}, fmt.Errorf("parse payment destination: %w", err)
	}
	if uri.Invoice != nil {
		return m.sendLightning(ctx, request, uri.Invoice)
	}
	if uri.Address == "" {
		return SendResult{}, errors.New("payment destination has no Bitcoin address")
	}
	if uri.Amount != "" {
		return SendResult{}, errors.New("BIP21 amount is not supported; provide amountSatoshis")
	}

	record, err := m.loadRecord()
	if err != nil {
		return SendResult{}, err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return SendResult{}, err
	}
	muunPublicKey, err := libwallet.NewHDPublicKeyFromString(record.MuunCosigningPublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		return SendResult{}, fmt.Errorf("decode Muun cosigning key: %w", err)
	}

	realtime, err := m.fetchRealtime(ctx, record.SessionToken)
	if err != nil {
		return SendResult{}, err
	}
	nextSize, err := m.fetchNextTransactionSize(ctx, record.SessionToken)
	if err != nil {
		return SendResult{}, err
	}
	analysis, outpoints, expectedDebt, err := analyzeOnchainPayment(request.AmountSatoshis, realtime, nextSize)
	if err != nil {
		return SendResult{}, err
	}

	amount, err := m.bitcoinAmount(analysis.AmountInSat, realtime.ExchangeRateWindow)
	if err != nil {
		return SendResult{}, err
	}
	fee, err := m.bitcoinAmount(analysis.FeeTotalInSat, realtime.ExchangeRateWindow)
	if err != nil {
		return SendResult{}, err
	}
	nonces := libwallet.GenerateMusigNonces(len(outpoints))
	requestPayload := outgoingOperationRequest{
		RequestID:                uuid.NewString(),
		IsExternal:               true,
		SenderIsExternal:         false,
		ReceiverIsExternal:       true,
		ReceiverAddress:          uri.Address,
		Amount:                   amount,
		Fee:                      fee,
		OutputAmountInSatoshis:   analysis.AmountInSat,
		ExchangeRatesWindowID:    realtime.ExchangeRateWindow.ID,
		Description:              strings.TrimSpace(request.Description),
		Status:                   "CREATED",
		Outpoints:                outpoints,
		MarkItAsReplaceableByFee: false,
		UserPublicNoncesHex:      musigPublicNonces(nonces),
	}

	var created operationCreatedJSON
	if _, err := m.doJSON(ctx, http.MethodPost, "operations", record.SessionToken, requestPayload, &created); err != nil {
		return SendResult{}, fmt.Errorf("create outgoing operation: %w", err)
	}
	if created.Operation.ID == nil {
		return SendResult{}, errors.New("Houston created an operation without an id")
	}
	if created.PartiallySignedTransaction == nil {
		return SendResult{}, errors.New("Houston created an operation without a partially signed transaction")
	}
	if len(created.AlternativeTransactions) != 0 {
		return SendResult{}, errors.New("Houston returned alternative transactions for an on-chain payment")
	}

	var change libwallet.MuunAddress
	if created.ChangeAddress != nil {
		change = walletAddress{
			version:        created.ChangeAddress.Version,
			derivationPath: created.ChangeAddress.DerivationPath,
			address:        created.ChangeAddress.Address,
		}
	}
	expectations := libwallet.NewSigningExpectations(
		uri.Address,
		analysis.AmountInSat,
		change,
		analysis.FeeTotalInSat+expectedDebt,
		false,
	)
	signer := m.signOutgoingTransaction
	if signer == nil {
		signer = signPartiallySignedTransaction
	}
	signed, err := signer(basePrivateKey, muunPublicKey, *created.PartiallySignedTransaction, nonces, expectations)
	if err != nil {
		return SendResult{}, fmt.Errorf("sign outgoing transaction: %w", err)
	}
	if signed == nil || len(signed.Bytes) == 0 || signed.Hash == "" {
		return SendResult{}, errors.New("transaction signer returned an empty transaction")
	}

	push := pushTransactionsRequest{
		RawTransaction:          &rawTransactionPayload{Hex: hex.EncodeToString(signed.Bytes)},
		AlternativeTransactions: []rawTransactionPayload{},
	}
	if _, err := m.doJSON(
		ctx,
		http.MethodPut,
		"operations/"+strconv.FormatInt(*created.Operation.ID, 10)+"/raw-transactions",
		record.SessionToken,
		push,
		nil,
	); err != nil {
		return SendResult{}, fmt.Errorf("push outgoing transaction: %w", err)
	}

	return SendResult{
		OperationID:    *created.Operation.ID,
		TransactionID:  signed.Hash,
		AmountSatoshis: analysis.AmountInSat,
		FeeSatoshis:    analysis.FeeTotalInSat,
	}, nil
}

func (m *Manager) sendLightning(ctx context.Context, request SendRequest, invoice *libwallet.Invoice) (SendResult, error) {
	if invoice == nil {
		return SendResult{}, errors.New("payment destination has no Lightning invoice")
	}
	if invoice.Sats <= 0 {
		return SendResult{}, errors.New("Lightning invoice has no amount")
	}
	if invoice.Sats != request.AmountSatoshis {
		return SendResult{}, fmt.Errorf("Lightning invoice amount is %d satoshis, not %d", invoice.Sats, request.AmountSatoshis)
	}
	if invoice.Expiry <= time.Now().Unix() {
		return SendResult{}, errors.New("Lightning invoice has expired")
	}

	record, err := m.loadRecord()
	if err != nil {
		return SendResult{}, err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return SendResult{}, err
	}
	muunPublicKey, err := libwallet.NewHDPublicKeyFromString(record.MuunCosigningPublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		return SendResult{}, fmt.Errorf("decode Muun cosigning key: %w", err)
	}

	var remoteSwap submarineSwapJSON
	if _, err := m.doJSON(
		ctx,
		http.MethodPost,
		"operations/sswap/create",
		record.SessionToken,
		submarineSwapRequest{
			Invoice:                invoice.RawInvoice,
			SwapExpirationInBlocks: submarineSwapExpirationInBlocks,
			Origin:                 "manual",
			BackgroundTimes:        []any{},
		},
		&remoteSwap,
	); err != nil {
		return SendResult{}, fmt.Errorf("create Lightning swap: %w", err)
	}
	swapFees, err := verifySubmarineSwap(invoice, remoteSwap, basePrivateKey.PublicKey(), muunPublicKey)
	if err != nil {
		return SendResult{}, err
	}

	realtime, err := m.fetchRealtime(ctx, record.SessionToken)
	if err != nil {
		return SendResult{}, err
	}
	nextSize, err := m.fetchNextTransactionSize(ctx, record.SessionToken)
	if err != nil {
		return SendResult{}, err
	}
	analysis, outpoints, err := analyzeLightningPayment(invoice.Sats, realtime, nextSize, swapFees)
	if err != nil {
		return SendResult{}, err
	}
	if analysis.SwapFees == nil {
		return SendResult{}, errors.New("Lightning payment analysis has no swap fees")
	}

	isLend := analysis.SwapFees.DebtType == fees.DebtTypeLend
	maxAlternatives := 0
	if !isLend && analysis.SwapFees.ConfirmationsNeeded == 0 && remoteSwap.MaxAlternativeTransactionCount != nil {
		maxAlternatives = *remoteSwap.MaxAlternativeTransactionCount
		if maxAlternatives < 0 {
			return SendResult{}, errors.New("Houston returned a negative alternative transaction count")
		}
	}

	primaryNonces := libwallet.GenerateMusigNonces(len(outpoints))
	alternativeNonces := make([]*libwallet.MusigNonces, 0, maxAlternatives)
	allPublicNonces := musigPublicNonces(primaryNonces)
	for index := 0; index < maxAlternatives; index++ {
		nonces := libwallet.GenerateMusigNonces(len(outpoints))
		alternativeNonces = append(alternativeNonces, nonces)
		allPublicNonces = append(allPublicNonces, musigPublicNonces(nonces)...)
	}

	outputAmount := int64(0)
	if remoteSwap.FundingOutputData.OutputAmountInSatoshis != nil {
		outputAmount = *remoteSwap.FundingOutputData.OutputAmountInSatoshis
	}
	if isLend {
		outputAmount = invoice.Sats + int64(analysis.SwapFees.RoutingFee)
		if outputAmount < 546 {
			outputAmount = 546
		}
	}
	amount, err := m.bitcoinAmount(analysis.AmountInSat, realtime.ExchangeRateWindow)
	if err != nil {
		return SendResult{}, err
	}
	fee, err := m.bitcoinAmount(analysis.FeeTotalInSat, realtime.ExchangeRateWindow)
	if err != nil {
		return SendResult{}, err
	}
	requestPayload := outgoingOperationRequest{
		RequestID:                uuid.NewString(),
		Direction:                "OUTGOING",
		IsExternal:               true,
		SenderIsExternal:         false,
		ReceiverIsExternal:       true,
		ReceiverAddress:          remoteSwap.FundingOutputData.OutputAddress,
		Amount:                   amount,
		Fee:                      fee,
		OutputAmountInSatoshis:   outputAmount,
		ExchangeRatesWindowID:    realtime.ExchangeRateWindow.ID,
		Description:              strings.TrimSpace(request.Description),
		Status:                   "CREATED",
		Outpoints:                outpoints,
		MarkItAsReplaceableByFee: false,
		UserPublicNoncesHex:      allPublicNonces,
		SwapUUID:                 remoteSwap.SwapUUID,
	}

	var created operationCreatedJSON
	if _, err := m.doJSON(ctx, http.MethodPost, "operations", record.SessionToken, requestPayload, &created); err != nil {
		return SendResult{}, fmt.Errorf("create Lightning operation: %w", err)
	}
	if created.Operation.ID == nil {
		return SendResult{}, errors.New("Houston created a Lightning operation without an id")
	}
	if len(created.AlternativeTransactions) > maxAlternatives {
		return SendResult{}, errors.New("Houston returned more alternative transactions than requested")
	}

	var transactionID string
	push := pushTransactionsRequest{AlternativeTransactions: []rawTransactionPayload{}}
	if !isLend {
		if created.PartiallySignedTransaction == nil {
			return SendResult{}, errors.New("Houston created a Lightning operation without a partially signed transaction")
		}
		var change libwallet.MuunAddress
		if created.ChangeAddress != nil {
			change = walletAddress{
				version:        created.ChangeAddress.Version,
				derivationPath: created.ChangeAddress.DerivationPath,
				address:        created.ChangeAddress.Address,
			}
		}
		expectations := libwallet.NewSigningExpectations(
			remoteSwap.FundingOutputData.OutputAddress,
			*remoteSwap.FundingOutputData.OutputAmountInSatoshis,
			change,
			analysis.FeeTotalInSat,
			false,
		)
		signer := m.signOutgoingTransaction
		if signer == nil {
			signer = signPartiallySignedTransaction
		}
		signed, err := signer(basePrivateKey, muunPublicKey, *created.PartiallySignedTransaction, primaryNonces, expectations)
		if err != nil {
			return SendResult{}, fmt.Errorf("sign Lightning funding transaction: %w", err)
		}
		if signed == nil || len(signed.Bytes) == 0 || signed.Hash == "" {
			return SendResult{}, errors.New("transaction signer returned an empty Lightning funding transaction")
		}
		transactionID = signed.Hash
		push.RawTransaction = &rawTransactionPayload{Hex: hex.EncodeToString(signed.Bytes)}

		for index, alternative := range created.AlternativeTransactions {
			if index >= len(alternativeNonces) {
				return SendResult{}, errors.New("Houston returned an alternative transaction without matching nonces")
			}
			signedAlternative, err := signer(basePrivateKey, muunPublicKey, alternative, alternativeNonces[index], expectations.ForAlternativeTransaction())
			if err != nil {
				return SendResult{}, fmt.Errorf("sign Lightning alternative transaction %d: %w", index, err)
			}
			if signedAlternative == nil || len(signedAlternative.Bytes) == 0 {
				return SendResult{}, fmt.Errorf("transaction signer returned an empty Lightning alternative transaction %d", index)
			}
			push.AlternativeTransactions = append(push.AlternativeTransactions, rawTransactionPayload{Hex: hex.EncodeToString(signedAlternative.Bytes)})
		}
	}

	if _, err := m.doJSON(
		ctx,
		http.MethodPut,
		"operations/"+strconv.FormatInt(*created.Operation.ID, 10)+"/raw-transactions",
		record.SessionToken,
		push,
		nil,
	); err != nil {
		return SendResult{}, fmt.Errorf("push Lightning transaction: %w", err)
	}

	return SendResult{
		OperationID:    *created.Operation.ID,
		TransactionID:  transactionID,
		AmountSatoshis: analysis.AmountInSat,
		FeeSatoshis:    analysis.FeeTotalInSat,
	}, nil
}

func verifySubmarineSwap(
	invoice *libwallet.Invoice,
	remote submarineSwapJSON,
	userBaseKey, muunBaseKey *libwallet.HDPublicKey,
) (*fees.SwapFees, error) {
	if remote.SwapUUID == "" || !strings.EqualFold(remote.RawInvoice, invoice.RawInvoice) {
		return nil, errors.New("Houston returned a swap for a different Lightning invoice")
	}
	fundingOutput := remote.FundingOutputData
	if fundingOutput.ScriptVersion != int64(libwallet.AddressVersionSwapsV2) {
		return nil, fmt.Errorf("Houston returned unsupported submarine swap version %d", fundingOutput.ScriptVersion)
	}
	if fundingOutput.OutputAddress == "" || fundingOutput.ExpirationInBlocks == nil || *fundingOutput.ExpirationInBlocks != submarineSwapExpirationInBlocks || fundingOutput.UserPublicKey == nil || fundingOutput.MuunPublicKey == nil || fundingOutput.ServerPaymentHashInHex == "" || fundingOutput.ServerPublicKeyInHex == "" {
		return nil, errors.New("Houston returned an incomplete Lightning funding output")
	}
	if remote.ReceiverData.PublicKey == "" || remote.Fees == nil || fundingOutput.ConfirmationsNeeded == nil {
		return nil, errors.New("Houston returned incomplete Lightning swap payment data")
	}
	if *fundingOutput.ConfirmationsNeeded < 0 {
		return nil, errors.New("Houston returned a negative Lightning confirmation target")
	}

	debtType := fees.DebtType(fundingOutput.DebtType)
	switch debtType {
	case fees.DebtTypeNone, fees.DebtTypeCollect, fees.DebtTypeLend:
	default:
		return nil, fmt.Errorf("Houston returned an unknown Lightning debt type %q", fundingOutput.DebtType)
	}
	if debtType != fees.DebtTypeLend && fundingOutput.OutputAmountInSatoshis == nil {
		return nil, errors.New("Houston returned a Lightning swap without a funding amount")
	}
	if fundingOutput.OutputAmountInSatoshis != nil && *fundingOutput.OutputAmountInSatoshis <= 0 {
		return nil, errors.New("Houston returned a non-positive Lightning funding amount")
	}
	debtAmount := int64(0)
	if fundingOutput.DebtAmountInSats != nil {
		debtAmount = *fundingOutput.DebtAmountInSats
	}
	if debtAmount < 0 || remote.Fees.LightningInSats < 0 || remote.Fees.SweepInSats < 0 {
		return nil, errors.New("Houston returned negative Lightning swap fees")
	}

	userFundingKey, err := libwallet.NewHDPublicKeyFromString(fundingOutput.UserPublicKey.Key, fundingOutput.UserPublicKey.Path, libwallet.Mainnet())
	if err != nil {
		return nil, fmt.Errorf("decode Lightning user funding key: %w", err)
	}
	muunFundingKey, err := libwallet.NewHDPublicKeyFromString(fundingOutput.MuunPublicKey.Key, fundingOutput.MuunPublicKey.Path, libwallet.Mainnet())
	if err != nil {
		return nil, fmt.Errorf("decode Lightning Muun funding key: %w", err)
	}
	validatedSwap := verifiedSubmarineSwap{
		remote:         remote,
		userFundingKey: userFundingKey,
		muunFundingKey: muunFundingKey,
	}
	if err := libwallet.ValidateSubmarineSwap(
		invoice.RawInvoice,
		userBaseKey,
		muunBaseKey,
		validatedSwap,
		submarineSwapExpirationInBlocks,
		libwallet.Mainnet(),
	); err != nil {
		return nil, fmt.Errorf("validate Lightning swap: %w", err)
	}

	outputAmount := int64(0)
	if fundingOutput.OutputAmountInSatoshis != nil {
		outputAmount = *fundingOutput.OutputAmountInSatoshis
	}
	outputPadding := remote.Fees.SweepInSats
	if debtType == fees.DebtTypeLend {
		outputPadding = 0
	}
	return &fees.SwapFees{
		RoutingFee:          btcutil.Amount(remote.Fees.LightningInSats),
		DebtType:            debtType,
		DebtAmount:          btcutil.Amount(debtAmount),
		OutputAmount:        btcutil.Amount(outputAmount),
		OutputPadding:       btcutil.Amount(outputPadding),
		ConfirmationsNeeded: uint(*fundingOutput.ConfirmationsNeeded),
	}, nil
}

type verifiedSubmarineSwap struct {
	remote         submarineSwapJSON
	userFundingKey *libwallet.HDPublicKey
	muunFundingKey *libwallet.HDPublicKey
}

func (s verifiedSubmarineSwap) Invoice() string {
	return s.remote.RawInvoice
}

func (s verifiedSubmarineSwap) Receiver() libwallet.SubmarineSwapReceiver {
	return submarineSwapReceiver{receiver: s.remote.ReceiverData}
}

func (s verifiedSubmarineSwap) FundingOutput() libwallet.SubmarineSwapFundingOutput {
	return submarineSwapFundingOutput{
		fundingOutput:  s.remote.FundingOutputData,
		userFundingKey: s.userFundingKey,
		muunFundingKey: s.muunFundingKey,
	}
}

func (s verifiedSubmarineSwap) PreimageInHex() string {
	return ""
}

type submarineSwapReceiver struct {
	receiver submarineSwapReceiverJSON
}

func (s submarineSwapReceiver) Alias() string {
	return s.receiver.Alias
}

func (s submarineSwapReceiver) PublicKey() string {
	return s.receiver.PublicKey
}

type submarineSwapFundingOutput struct {
	fundingOutput  submarineSwapFundingOutputJSON
	userFundingKey *libwallet.HDPublicKey
	muunFundingKey *libwallet.HDPublicKey
}

func (s submarineSwapFundingOutput) ScriptVersion() int64 {
	return s.fundingOutput.ScriptVersion
}

func (s submarineSwapFundingOutput) OutputAddress() string {
	return s.fundingOutput.OutputAddress
}

func (s submarineSwapFundingOutput) OutputAmount() int64 {
	if s.fundingOutput.OutputAmountInSatoshis == nil {
		return 0
	}
	return *s.fundingOutput.OutputAmountInSatoshis
}

func (s submarineSwapFundingOutput) ConfirmationsNeeded() int {
	if s.fundingOutput.ConfirmationsNeeded == nil {
		return 0
	}
	return *s.fundingOutput.ConfirmationsNeeded
}

func (s submarineSwapFundingOutput) ServerPaymentHashInHex() string {
	return s.fundingOutput.ServerPaymentHashInHex
}

func (s submarineSwapFundingOutput) ServerPublicKeyInHex() string {
	return s.fundingOutput.ServerPublicKeyInHex
}

func (s submarineSwapFundingOutput) UserLockTime() int64 {
	if s.fundingOutput.UserLockTime == nil {
		return 0
	}
	return *s.fundingOutput.UserLockTime
}

func (s submarineSwapFundingOutput) UserRefundAddress() libwallet.MuunAddress {
	if s.fundingOutput.UserRefundAddress == nil {
		return walletAddress{}
	}
	return walletAddress{
		version:        s.fundingOutput.UserRefundAddress.Version,
		derivationPath: s.fundingOutput.UserRefundAddress.DerivationPath,
		address:        s.fundingOutput.UserRefundAddress.Address,
	}
}

func (s submarineSwapFundingOutput) ExpirationInBlocks() int64 {
	if s.fundingOutput.ExpirationInBlocks == nil {
		return 0
	}
	return *s.fundingOutput.ExpirationInBlocks
}

func (s submarineSwapFundingOutput) UserPublicKey() *libwallet.HDPublicKey {
	return s.userFundingKey
}

func (s submarineSwapFundingOutput) MuunPublicKey() *libwallet.HDPublicKey {
	return s.muunFundingKey
}

func (m *Manager) fetchRealtime(ctx context.Context, token string) (realtimeData, error) {
	var realtime realtimeData
	if _, err := m.doJSON(ctx, http.MethodGet, "realtime", token, nil, &realtime); err != nil {
		return realtimeData{}, fmt.Errorf("fetch realtime data: %w", err)
	}
	return realtime, nil
}

func (m *Manager) fetchNextTransactionSize(ctx context.Context, token string) (nextTransactionSize, error) {
	var nextSize nextTransactionSize
	if _, err := m.doJSON(ctx, http.MethodGet, "operations/next-transaction-size", token, nil, &nextSize); err != nil {
		return nextTransactionSize{}, fmt.Errorf("fetch next transaction size: %w", err)
	}
	return nextSize, nil
}

// analyzeOnchainPayment returns the payment analysis, the outpoints to spend, and the debt Houston
// settles out of the change output of this transaction.
func analyzeOnchainPayment(amount int64, realtime realtimeData, nextSize nextTransactionSize) (*operation.PaymentAnalysis, []string, int64, error) {
	if realtime.FeeWindow == nil || realtime.ExchangeRateWindow == nil {
		return nil, nil, 0, errors.New("Houston realtime data has no fee or exchange-rate window")
	}
	if realtime.FeeWindow.FastConfTarget <= 0 {
		return nil, nil, 0, errors.New("Houston realtime data has an invalid fast confirmation target")
	}
	feeRate, found := realtime.FeeWindow.TargetedFees[strconv.FormatInt(realtime.FeeWindow.FastConfTarget, 10)]
	if !found || feeRate <= 0 || math.IsNaN(feeRate) || math.IsInf(feeRate, 0) {
		return nil, nil, 0, errors.New("Houston realtime data has no valid fast fee rate")
	}
	expectedDebt := int64(0)
	if nextSize.ExpectedDebtInSat != nil {
		expectedDebt = *nextSize.ExpectedDebtInSat
	}
	if expectedDebt < 0 {
		return nil, nil, 0, errors.New("Houston returned a negative expected debt")
	}

	progression := make([]operation.SizeForAmount, 0, len(nextSize.SizeProgression))
	outpoints := make([]string, 0, len(nextSize.SizeProgression))
	for index, item := range nextSize.SizeProgression {
		if item.AmountInSatoshis < 0 || item.SizeInBytes <= 0 || item.Outpoint == "" {
			return nil, nil, 0, fmt.Errorf("Houston returned an invalid size progression item %d", index)
		}
		status, found := operation.MapUtxoStatus(item.Status)
		if !found {
			return nil, nil, 0, fmt.Errorf("Houston returned an unknown UTXO status %q", item.Status)
		}
		progression = append(progression, operation.SizeForAmount{
			AmountInSat: item.AmountInSatoshis,
			SizeInVByte: item.SizeInBytes / 4,
			Outpoint:    item.Outpoint,
			UtxoStatus:  status,
		})
		outpoints = append(outpoints, item.Outpoint)
	}
	if len(progression) == 0 {
		return nil, nil, 0, errors.New("wallet has no spendable UTXOs")
	}

	analyzer := operation.NewPaymentAnalyzer(
		&operation.FeeWindow{TargetedFees: map[uint]float64{uint(realtime.FeeWindow.FastConfTarget): feeRate}},
		&operation.NextTransactionSize{SizeProgression: progression, ExpectedDebtInSat: expectedDebt},
		nil,
	)
	analysis, err := analyzer.ToAddress(&operation.PaymentToAddress{
		AmountInSat:           amount,
		FeeRateInSatsPerVByte: feeRate,
	})
	if err != nil {
		return nil, nil, 0, fmt.Errorf("analyze on-chain payment: %w", err)
	}
	if analysis.Status != operation.AnalysisStatusOk {
		return nil, nil, 0, fmt.Errorf("on-chain payment is %s", analysis.Status)
	}
	return analysis, outpoints, expectedDebt, nil
}

func analyzeLightningPayment(
	amount int64,
	realtime realtimeData,
	nextSize nextTransactionSize,
	swapFees *fees.SwapFees,
) (*operation.PaymentAnalysis, []string, error) {
	if realtime.FeeWindow == nil || realtime.ExchangeRateWindow == nil {
		return nil, nil, errors.New("Houston realtime data has no fee or exchange-rate window")
	}
	if swapFees == nil {
		return nil, nil, errors.New("Lightning swap has no fee data")
	}

	targetedFees := make(map[uint]float64, len(realtime.FeeWindow.TargetedFees))
	for rawTarget, feeRate := range realtime.FeeWindow.TargetedFees {
		target, err := strconv.ParseUint(rawTarget, 10, 0)
		if err != nil || feeRate <= 0 || math.IsNaN(feeRate) || math.IsInf(feeRate, 0) {
			return nil, nil, fmt.Errorf("Houston returned an invalid Lightning fee target %q", rawTarget)
		}
		targetedFees[uint(target)] = feeRate
	}
	if len(targetedFees) == 0 {
		return nil, nil, errors.New("Houston returned no Lightning fee rates")
	}

	expectedDebt := int64(0)
	if nextSize.ExpectedDebtInSat != nil {
		expectedDebt = *nextSize.ExpectedDebtInSat
	}
	if expectedDebt < 0 {
		return nil, nil, errors.New("Houston returned a negative expected debt")
	}

	progression := make([]operation.SizeForAmount, 0, len(nextSize.SizeProgression))
	outpoints := make([]string, 0, len(nextSize.SizeProgression))
	for index, item := range nextSize.SizeProgression {
		if item.AmountInSatoshis < 0 || item.SizeInBytes < 4 || item.Outpoint == "" {
			return nil, nil, fmt.Errorf("Houston returned an invalid size progression item %d", index)
		}
		status, found := operation.MapUtxoStatus(item.Status)
		if !found {
			return nil, nil, fmt.Errorf("Houston returned an unknown UTXO status %q", item.Status)
		}
		progression = append(progression, operation.SizeForAmount{
			AmountInSat: item.AmountInSatoshis,
			SizeInVByte: item.SizeInBytes / 4,
			Outpoint:    item.Outpoint,
			UtxoStatus:  status,
		})
		outpoints = append(outpoints, item.Outpoint)
	}
	if len(progression) == 0 {
		return nil, nil, errors.New("wallet has no spendable UTXOs")
	}

	analyzer := operation.NewPaymentAnalyzer(
		&operation.FeeWindow{TargetedFees: targetedFees},
		&operation.NextTransactionSize{SizeProgression: progression, ExpectedDebtInSat: expectedDebt},
		nil,
	)
	analysis, err := analyzer.ToInvoice(&operation.PaymentToInvoice{
		AmountInSat: amount,
		SwapFees:    swapFees,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("analyze Lightning payment: %w", err)
	}
	if analysis.Status != operation.AnalysisStatusOk {
		return nil, nil, fmt.Errorf("Lightning payment is %s", analysis.Status)
	}
	return analysis, outpoints, nil
}

func (m *Manager) bitcoinAmount(satoshis int64, window *exchangeRateWindow) (satoshiAmount, error) {
	if window == nil {
		return satoshiAmount{}, errors.New("Houston realtime data has no exchange rate window")
	}
	primary := strings.ToUpper(strings.TrimSpace(m.config.PrimaryCurrency))
	if primary == "" {
		primary = "USD"
	}
	rate, ok := window.Rates[primary]
	if !ok || rate <= 0 {
		return satoshiAmount{}, fmt.Errorf("missing %s exchange rate", primary)
	}
	amount := satoshis
	btc := float64(satoshis) / 1e8
	return satoshiAmount{
		InSatoshis:        &amount,
		InInputCurrency:   moneyAmount{Amount: btc, Currency: "BTC"},
		InPrimaryCurrency: moneyAmount{Amount: btc * rate, Currency: primary},
	}, nil
}

func musigPublicNonces(nonces *libwallet.MusigNonces) []string {
	values := make([]string, 0, nonces.Length())
	for index := 0; index < nonces.Length(); index++ {
		values = append(values, nonces.GetPubnonceHex(index))
	}
	return values
}

func signPartiallySignedTransaction(
	basePrivateKey *libwallet.HDPrivateKey,
	muunPublicKey *libwallet.HDPublicKey,
	partial partiallySignedTransactionJSON,
	nonces *libwallet.MusigNonces,
	expectations *libwallet.SigningExpectations,
) (*libwallet.Transaction, error) {
	if partial.HexTransaction == "" {
		return nil, errors.New("Houston returned an empty partially signed transaction")
	}
	rawTransaction, err := hex.DecodeString(partial.HexTransaction)
	if err != nil || len(rawTransaction) == 0 {
		return nil, errors.New("Houston returned an invalid partially signed transaction")
	}
	inputs := &libwallet.InputList{}
	for index, rawInput := range partial.Inputs {
		input, err := inputFromJSON(rawInput)
		if err != nil {
			return nil, fmt.Errorf("decode partially signed input %d: %w", index, err)
		}
		inputs.Add(input)
	}
	if len(partial.Inputs) == 0 {
		return nil, errors.New("Houston returned a transaction without inputs")
	}
	transaction, err := libwallet.NewPartiallySignedTransaction(inputs, rawTransaction, nonces)
	if err != nil {
		return nil, fmt.Errorf("build partially signed transaction: %w", err)
	}
	if err := transaction.Verify(expectations, basePrivateKey.PublicKey(), muunPublicKey); err != nil {
		return nil, fmt.Errorf("verify partially signed transaction: %w", err)
	}
	signed, err := transaction.Sign(basePrivateKey, muunPublicKey)
	if err != nil {
		return nil, err
	}
	return signed, nil
}

func inputFromJSON(raw partiallySignedInputJSON) (libwallet.Input, error) {
	if raw.PrevOut.Index < 0 || raw.PrevOut.Amount <= 0 {
		return nil, errors.New("input has an invalid previous output")
	}
	txID, err := hex.DecodeString(raw.PrevOut.TxID)
	if err != nil || len(txID) != 32 {
		return nil, errors.New("input has an invalid previous transaction id")
	}
	if !supportedAddressVersion(raw.Address.Version) || raw.Address.DerivationPath == "" || raw.Address.Address == "" {
		return nil, errors.New("input has an invalid wallet address")
	}
	incomingSwapInput, err := parseIncomingSwapInput(raw)
	if err != nil {
		return nil, err
	}
	var userSignature, muunSignature, muunPublicNonce []byte
	if raw.UserSignature != nil {
		userSignature, err = hex.DecodeString(raw.UserSignature.Hex)
		if err != nil {
			return nil, errors.New("input has an invalid user signature")
		}
	}
	if raw.MuunSignature != nil {
		muunSignature, err = hex.DecodeString(raw.MuunSignature.Hex)
		if err != nil {
			return nil, errors.New("input has an invalid Muun signature")
		}
	}
	if raw.RawMuunPublicNonceHex != "" {
		muunPublicNonce, err = hex.DecodeString(raw.RawMuunPublicNonceHex)
		if err != nil {
			return nil, errors.New("input has an invalid Muun public nonce")
		}
	}
	return walletInput{
		outpoint: walletOutpoint{txID: txID, index: raw.PrevOut.Index, amount: raw.PrevOut.Amount},
		address: walletAddress{
			version:        raw.Address.Version,
			derivationPath: raw.Address.DerivationPath,
			address:        raw.Address.Address,
		},
		userSignature:   userSignature,
		muunSignature:   muunSignature,
		muunPublicNonce: muunPublicNonce,
		incomingSwap:    incomingSwapInput,
	}, nil
}

func supportedAddressVersion(version int) bool {
	switch version {
	case libwallet.AddressVersionV1, libwallet.AddressVersionV2, libwallet.AddressVersionV3, libwallet.AddressVersionV4, libwallet.AddressVersionV5, libwallet.AddressVersionSwapsV1, libwallet.AddressVersionSwapsV2, 201:
		return true
	default:
		return false
	}
}

func parseIncomingSwapInput(raw partiallySignedInputJSON) (*walletIncomingSwap, error) {
	if raw.Address.Version != 201 {
		return nil, nil
	}
	if raw.IncomingSwap == nil {
		return nil, errors.New("incoming swap input is missing swap data")
	}
	sphinx, err := decodeOptionalHex(raw.IncomingSwap.SphinxHex)
	if err != nil {
		return nil, errors.New("incoming swap has an invalid sphinx")
	}
	htlcTx, err := hex.DecodeString(raw.IncomingSwap.HtlcTxHex)
	if err != nil || len(htlcTx) == 0 {
		return nil, errors.New("incoming swap has an invalid HTLC transaction")
	}
	paymentHash, err := hex.DecodeString(raw.IncomingSwap.PaymentHash256Hex)
	if err != nil || len(paymentHash) != 32 {
		return nil, errors.New("incoming swap has an invalid payment hash")
	}
	preimage, err := decodeOptionalHex(raw.IncomingSwap.PreimageHex)
	if err != nil {
		return nil, errors.New("incoming swap has an invalid preimage")
	}
	if raw.IncomingSwap.HtlcOutputKeyPath == "" || raw.IncomingSwap.SwapServerPublicKeyHex == "" {
		return nil, errors.New("incoming swap is missing HTLC path or server key")
	}
	return &walletIncomingSwap{
		sphinx:              sphinx,
		htlcTx:              htlcTx,
		paymentHash:         paymentHash,
		swapServerPublicKey: raw.IncomingSwap.SwapServerPublicKeyHex,
		expirationHeight:    raw.IncomingSwap.ExpirationHeight,
		collectInSats:       raw.IncomingSwap.CollectInSats,
		preimage:            preimage,
		htlcOutputKeyPath:   raw.IncomingSwap.HtlcOutputKeyPath,
	}, nil
}

func decodeOptionalHex(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	return hex.DecodeString(value)
}
