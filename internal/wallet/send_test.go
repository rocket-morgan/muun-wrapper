package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/lnwire"
	"github.com/lightningnetwork/lnd/zpay32"
	libwallet "github.com/muun/libwallet"
	"github.com/muun/libwallet/swaps"
)

func TestSendOnchainCreatesSignsAndPushesOperation(t *testing.T) {
	muunKey := mustTestBaseKey(t, bytes.Repeat([]byte{21}, 32))
	receiverUserKey := mustTestBaseKey(t, bytes.Repeat([]byte{22}, 32))
	receiverMuunKey := mustTestBaseKey(t, bytes.Repeat([]byte{23}, 32))
	receiverUserAddressKey, err := receiverUserKey.PublicKey().DeriveTo(basePath + "/" + addressDir + "/0")
	if err != nil {
		t.Fatal(err)
	}
	receiverMuunAddressKey, err := receiverMuunKey.PublicKey().DeriveTo(basePath + "/" + addressDir + "/0")
	if err != nil {
		t.Fatal(err)
	}
	receiverAddress, err := libwallet.CreateAddressV5(receiverUserAddressKey, receiverMuunAddressKey)
	if err != nil {
		t.Fatal(err)
	}

	var createdOperation bool
	var pushedTransaction bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			w.Header().Set("Authorization", "Bearer session-token")
			writeTestJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/realtime":
			writeTestJSON(t, w, map[string]any{
				"feeWindow": map[string]any{
					"id":               7,
					"targetedFees":     map[string]float64{"1": 5},
					"fastConfTarget":   1,
					"mediumConfTarget": 3,
					"slowConfTarget":   6,
				},
				"exchangeRateWindow": map[string]any{"id": 9, "rates": map[string]float64{"BTC": 1, "USD": 50000}},
			})
		case "/houston/operations/next-transaction-size":
			writeTestJSON(t, w, map[string]any{
				"sizeProgression": []map[string]any{{
					"amountInSatoshis": 20_000,
					"sizeInBytes":      400,
					"outpoint":         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0",
					"status":           "CONFIRMED",
				}},
				"expectedDebtInSat": 0,
			})
		case "/houston/operations":
			if r.Method != http.MethodPost {
				t.Fatalf("new operation method = %s, want POST", r.Method)
			}
			var operation struct {
				ReceiverAddress string `json:"receiverAddress"`
				Amount          struct {
					InSatoshis      int64 `json:"inSatoshis"`
					InInputCurrency struct {
						Amount   float64 `json:"amount"`
						Currency string  `json:"currency"`
					} `json:"inInputCurrency"`
					InPrimaryCurrency struct {
						Amount   float64 `json:"amount"`
						Currency string  `json:"currency"`
					} `json:"inPrimaryCurrency"`
				} `json:"amount"`
				Fee struct {
					InSatoshis        int64 `json:"inSatoshis"`
					InPrimaryCurrency struct {
						Currency string `json:"currency"`
					} `json:"inPrimaryCurrency"`
				} `json:"fee"`
				ExchangeRatesWindowID int64    `json:"exchangeRatesWindowId"`
				Outpoints             []string `json:"outpoints"`
				UserPublicNoncesHex   []string `json:"userPublicNoncesHex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&operation); err != nil {
				t.Fatal(err)
			}
			if operation.ReceiverAddress != receiverAddress.Address() || operation.Amount.InSatoshis != 10_000 {
				t.Fatalf("operation receiver=%q amount=%d", operation.ReceiverAddress, operation.Amount.InSatoshis)
			}
			if operation.Amount.InInputCurrency.Currency != "BTC" || operation.Amount.InInputCurrency.Amount != 0.0001 {
				t.Fatalf("amount input currency = %+v", operation.Amount.InInputCurrency)
			}
			if operation.Amount.InPrimaryCurrency.Currency != "USD" || operation.Amount.InPrimaryCurrency.Amount <= 0 {
				t.Fatalf("amount primary currency = %+v", operation.Amount.InPrimaryCurrency)
			}
			if operation.Fee.InSatoshis <= 0 || operation.Fee.InPrimaryCurrency.Currency != "USD" || operation.ExchangeRatesWindowID != 9 {
				t.Fatalf("operation fee=%d feeCurrency=%s rateWindow=%d", operation.Fee.InSatoshis, operation.Fee.InPrimaryCurrency.Currency, operation.ExchangeRatesWindowID)
			}
			if len(operation.Outpoints) != 1 || len(operation.UserPublicNoncesHex) != 1 {
				t.Fatalf("operation outpoints=%v nonces=%v", operation.Outpoints, operation.UserPublicNoncesHex)
			}
			createdOperation = true
			writeTestJSON(t, w, map[string]any{
				"operation": map[string]any{"id": 42},
				"partiallySignedTransaction": map[string]any{
					"hexTransaction": "00",
					"inputs":         []any{},
				},
				"alternativeTransactions": []any{},
			})
		case "/houston/operations/42/raw-transactions":
			if r.Method != http.MethodPut {
				t.Fatalf("push method = %s, want PUT", r.Method)
			}
			var pushed struct {
				RawTransaction struct {
					Hex string `json:"hex"`
				} `json:"rawTransaction"`
				AlternativeTransactions []any `json:"alternativeTransactions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
				t.Fatal(err)
			}
			if pushed.RawTransaction.Hex != "cafebabe" || len(pushed.AlternativeTransactions) != 0 {
				t.Fatalf("pushed transaction = %#v", pushed)
			}
			pushedTransaction = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := NewManager(Config{
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

	var signed bool
	manager.signOutgoingTransaction = func(
		_ *libwallet.HDPrivateKey,
		_ *libwallet.HDPublicKey,
		_ partiallySignedTransactionJSON,
		_ *libwallet.MusigNonces,
		_ *libwallet.SigningExpectations,
	) (*libwallet.Transaction, error) {
		signed = true
		return &libwallet.Transaction{Hash: "broadcast-hash", Bytes: []byte{0xca, 0xfe, 0xba, 0xbe}}, nil
	}

	result, err := manager.Send(context.Background(), SendRequest{
		Destination:    receiverAddress.Address(),
		AmountSatoshis: 10_000,
		Description:    "test payment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (SendResult{OperationID: 42, TransactionID: "broadcast-hash", AmountSatoshis: 10_000, FeeSatoshis: 500}) {
		t.Fatalf("result = %#v", result)
	}
	if !createdOperation || !signed || !pushedTransaction {
		t.Fatalf("created=%t signed=%t pushed=%t", createdOperation, signed, pushedTransaction)
	}
}

func TestSendLightningCreatesValidatedSwapAndPushesAlternatives(t *testing.T) {
	invoice, parsedInvoice := mustTestLightningInvoice(t, 250_000)
	muunKey := mustTestBaseKey(t, bytes.Repeat([]byte{31}, 32))
	swapServerKey := mustTestBaseKey(t, bytes.Repeat([]byte{32}, 32))

	var walletBaseKey *libwallet.HDPublicKey
	var expectedSwapAddress string
	var createdOperation bool
	var pushedTransactions bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			var request createSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			var err error
			walletBaseKey, err = libwallet.NewHDPublicKeyFromString(request.BasePublicKey.Key, request.BasePublicKey.Path, libwallet.Mainnet())
			if err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Authorization", "Bearer session-token")
			writeTestJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": swapServerKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/operations/sswap/create":
			if r.Method != http.MethodPost {
				t.Fatalf("create swap method = %s, want POST", r.Method)
			}
			var request struct {
				Invoice                string `json:"invoice"`
				SwapExpirationInBlocks int64  `json:"swapExpirationInBlocks"`
				Origin                 string `json:"origin"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Invoice != invoice || request.SwapExpirationInBlocks != 1008 || request.Origin != "manual" {
				t.Fatalf("swap request = %#v", request)
			}
			swap := testV2Swap(t, parsedInvoice, walletBaseKey, muunKey.PublicKey(), swapServerKey.PublicKey(), "NONE", parsedInvoice.Sats+5)
			expectedSwapAddress = swap["fundingOutput"].(map[string]any)["outputAddress"].(string)
			writeTestJSON(t, w, swap)
		case "/houston/realtime":
			writeTestJSON(t, w, map[string]any{
				"feeWindow": map[string]any{
					"id":               7,
					"targetedFees":     map[string]float64{"0": 5, "1": 5},
					"fastConfTarget":   1,
					"mediumConfTarget": 3,
					"slowConfTarget":   6,
				},
				"exchangeRateWindow": map[string]any{"id": 9, "rates": map[string]float64{"BTC": 1, "USD": 50000}},
			})
		case "/houston/operations/next-transaction-size":
			writeTestJSON(t, w, map[string]any{
				"sizeProgression": []map[string]any{{
					"amountInSatoshis": 500_000,
					"sizeInBytes":      400,
					"outpoint":         "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:0",
					"status":           "CONFIRMED",
				}},
				"expectedDebtInSat": 0,
			})
		case "/houston/operations":
			if r.Method != http.MethodPost {
				t.Fatalf("new operation method = %s, want POST", r.Method)
			}
			var operation struct {
				Direction       string `json:"direction"`
				ReceiverAddress string `json:"receiverAddress"`
				Amount          struct {
					InSatoshis int64 `json:"inSatoshis"`
				} `json:"amount"`
				Fee struct {
					InSatoshis int64 `json:"inSatoshis"`
				} `json:"fee"`
				OutputAmountInSatoshis int64    `json:"outputAmountInSatoshis"`
				SwapUUID               string   `json:"swapUuid"`
				UserPublicNoncesHex    []string `json:"userPublicNoncesHex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&operation); err != nil {
				t.Fatal(err)
			}
			if operation.Direction != "OUTGOING" || operation.ReceiverAddress != expectedSwapAddress || operation.Amount.InSatoshis != parsedInvoice.Sats || operation.Fee.InSatoshis != 500 || operation.OutputAmountInSatoshis != parsedInvoice.Sats+5 || operation.SwapUUID != "test-swap" || len(operation.UserPublicNoncesHex) != 2 {
				t.Fatalf("operation = %#v", operation)
			}
			createdOperation = true
			writeTestJSON(t, w, map[string]any{
				"operation": map[string]any{"id": 43},
				"partiallySignedTransaction": map[string]any{
					"hexTransaction": "00",
					"inputs":         []any{},
				},
				"alternativeTransactions": []map[string]any{{
					"hexTransaction": "01",
					"inputs":         []any{},
				}},
			})
		case "/houston/operations/43/raw-transactions":
			if r.Method != http.MethodPut {
				t.Fatalf("push method = %s, want PUT", r.Method)
			}
			var pushed struct {
				RawTransaction struct {
					Hex string `json:"hex"`
				} `json:"rawTransaction"`
				AlternativeTransactions []struct {
					Hex string `json:"hex"`
				} `json:"alternativeTransactions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
				t.Fatal(err)
			}
			if pushed.RawTransaction.Hex != "010203" || len(pushed.AlternativeTransactions) != 1 || pushed.AlternativeTransactions[0].Hex != "040506" {
				t.Fatalf("pushed transactions = %#v", pushed)
			}
			pushedTransactions = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := NewManager(Config{
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

	signCount := 0
	manager.signOutgoingTransaction = func(
		_ *libwallet.HDPrivateKey,
		_ *libwallet.HDPublicKey,
		_ partiallySignedTransactionJSON,
		_ *libwallet.MusigNonces,
		_ *libwallet.SigningExpectations,
	) (*libwallet.Transaction, error) {
		signCount++
		if signCount == 1 {
			return &libwallet.Transaction{Hash: "primary-hash", Bytes: []byte{1, 2, 3}}, nil
		}
		return &libwallet.Transaction{Hash: "alternative-hash", Bytes: []byte{4, 5, 6}}, nil
	}

	result, err := manager.Send(context.Background(), SendRequest{
		Destination:    invoice,
		AmountSatoshis: parsedInvoice.Sats,
		Description:    "lightning test payment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (SendResult{OperationID: 43, TransactionID: "primary-hash", AmountSatoshis: parsedInvoice.Sats, FeeSatoshis: 500}) {
		t.Fatalf("result = %#v", result)
	}
	if !createdOperation || !pushedTransactions || signCount != 2 {
		t.Fatalf("created=%t pushed=%t signCount=%d", createdOperation, pushedTransactions, signCount)
	}
}

func TestSendLightningLendSwapPushesWithoutOnchainTransaction(t *testing.T) {
	invoice, parsedInvoice := mustTestLightningInvoice(t, 1_000)
	muunKey := mustTestBaseKey(t, bytes.Repeat([]byte{41}, 32))
	swapServerKey := mustTestBaseKey(t, bytes.Repeat([]byte{42}, 32))

	var walletBaseKey *libwallet.HDPublicKey
	var createdOperation bool
	var pushedWithoutTransaction bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			var request createSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			var err error
			walletBaseKey, err = libwallet.NewHDPublicKeyFromString(request.BasePublicKey.Key, request.BasePublicKey.Path, libwallet.Mainnet())
			if err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Authorization", "Bearer session-token")
			writeTestJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": swapServerKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeTestJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/operations/sswap/create":
			swap := testV2Swap(t, parsedInvoice, walletBaseKey, muunKey.PublicKey(), swapServerKey.PublicKey(), "LEND", 0)
			swap["fundingOutput"].(map[string]any)["outputAmountInSatoshis"] = nil
			swap["fundingOutput"].(map[string]any)["debtAmountInSats"] = parsedInvoice.Sats + 5
			writeTestJSON(t, w, swap)
		case "/houston/realtime":
			writeTestJSON(t, w, map[string]any{
				"feeWindow": map[string]any{
					"id":               7,
					"targetedFees":     map[string]float64{"1": 5, "250": 1},
					"fastConfTarget":   1,
					"mediumConfTarget": 3,
					"slowConfTarget":   6,
				},
				"exchangeRateWindow": map[string]any{"id": 9, "rates": map[string]float64{"BTC": 1, "USD": 50000}},
			})
		case "/houston/operations/next-transaction-size":
			writeTestJSON(t, w, map[string]any{
				"sizeProgression": []map[string]any{{
					"amountInSatoshis": 20_000,
					"sizeInBytes":      400,
					"outpoint":         "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc:0",
					"status":           "CONFIRMED",
				}},
				"expectedDebtInSat": 0,
			})
		case "/houston/operations":
			var operation struct {
				OutputAmountInSatoshis int64    `json:"outputAmountInSatoshis"`
				SwapUUID               string   `json:"swapUuid"`
				UserPublicNoncesHex    []string `json:"userPublicNoncesHex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&operation); err != nil {
				t.Fatal(err)
			}
			if operation.OutputAmountInSatoshis != parsedInvoice.Sats+5 || operation.SwapUUID != "test-swap" || len(operation.UserPublicNoncesHex) != 1 {
				t.Fatalf("lend operation = %#v", operation)
			}
			createdOperation = true
			writeTestJSON(t, w, map[string]any{
				"operation": map[string]any{"id": 44},
				"partiallySignedTransaction": map[string]any{
					"hexTransaction": "00",
					"inputs":         []any{},
				},
				"alternativeTransactions": []any{},
			})
		case "/houston/operations/44/raw-transactions":
			var pushed struct {
				RawTransaction *struct {
					Hex string `json:"hex"`
				} `json:"rawTransaction"`
				AlternativeTransactions []any `json:"alternativeTransactions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
				t.Fatal(err)
			}
			if pushed.RawTransaction != nil || len(pushed.AlternativeTransactions) != 0 {
				t.Fatalf("lend push = %#v", pushed)
			}
			pushedWithoutTransaction = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := NewManager(Config{
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
	manager.signOutgoingTransaction = func(
		_ *libwallet.HDPrivateKey,
		_ *libwallet.HDPublicKey,
		_ partiallySignedTransactionJSON,
		_ *libwallet.MusigNonces,
		_ *libwallet.SigningExpectations,
	) (*libwallet.Transaction, error) {
		t.Fatal("lend swaps must not sign an on-chain transaction")
		return nil, nil
	}

	result, err := manager.Send(context.Background(), SendRequest{
		Destination:    invoice,
		AmountSatoshis: parsedInvoice.Sats,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (SendResult{OperationID: 44, AmountSatoshis: parsedInvoice.Sats, FeeSatoshis: 0}) {
		t.Fatalf("result = %#v", result)
	}
	if !createdOperation || !pushedWithoutTransaction {
		t.Fatalf("created=%t pushed=%t", createdOperation, pushedWithoutTransaction)
	}
}

func testV2Swap(
	t *testing.T,
	invoice *libwallet.Invoice,
	userBaseKey, muunBaseKey, swapServerKey *libwallet.HDPublicKey,
	debtType string,
	outputAmount int64,
) map[string]any {
	t.Helper()
	const swapPath = basePath + "/" + addressDir + "/0"
	userKey, err := userBaseKey.DeriveTo(swapPath)
	if err != nil {
		t.Fatal(err)
	}
	muunKey, err := muunBaseKey.DeriveTo(swapPath)
	if err != nil {
		t.Fatal(err)
	}
	witnessScript, err := swaps.CreateWitnessScriptSubmarineSwapV2(
		invoice.PaymentHash,
		userKey.Raw(),
		muunKey.Raw(),
		swapServerKey.Raw(),
		1008,
	)
	if err != nil {
		t.Fatal(err)
	}
	witnessHash := sha256.Sum256(witnessScript)
	address, err := btcutil.NewAddressWitnessScriptHash(witnessHash[:], &chaincfg.MainNetParams)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"swapUuid": "test-swap",
		"invoice":  invoice.RawInvoice,
		"receiver": map[string]any{
			"alias":            "test receiver",
			"networkAddresses": []string{},
			"publicKey":        hex.EncodeToString(invoice.Destination),
		},
		"fundingOutput": map[string]any{
			"outputAddress":          address.EncodeAddress(),
			"outputAmountInSatoshis": outputAmount,
			"confirmationsNeeded":    0,
			"expirationInBlocks":     1008,
			"userPublicKey":          map[string]string{"key": userKey.String(), "path": swapPath},
			"muunPublicKey":          map[string]string{"key": muunKey.String(), "path": swapPath},
			"serverPaymentHashInHex": hex.EncodeToString(invoice.PaymentHash),
			"serverPublicKeyInHex":   hex.EncodeToString(swapServerKey.Raw()),
			"scriptVersion":          libwallet.AddressVersionSwapsV2,
			"debtType":               debtType,
			"debtAmountInSats":       0,
		},
		"fees": map[string]any{
			"lightningInSats": 5,
			"sweepInSats":     0,
		},
		"maxAlternativeTransactionCount": 1,
	}
}

func mustTestLightningInvoice(t *testing.T, amountSatoshis int64) (string, *libwallet.Invoice) {
	t.Helper()
	privateKey, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	var paymentHash [32]byte
	copy(paymentHash[:], bytes.Repeat([]byte{0x11}, 32))
	invoice, err := zpay32.NewInvoice(
		libwallet.Mainnet().ToParams(),
		paymentHash,
		time.Now(),
		zpay32.Amount(lnwire.MilliSatoshi(amountSatoshis*1000)),
		zpay32.Description("lightning test invoice"),
		zpay32.Expiry(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	rawInvoice, err := invoice.Encode(zpay32.MessageSigner{
		SignCompact: func(msg []byte) ([]byte, error) {
			hash := chainhash.HashB(msg)
			return ecdsa.SignCompact(privateKey, hash, true)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsedInvoice, err := libwallet.ParseInvoice(rawInvoice, libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	return rawInvoice, parsedInvoice
}

func mustTestBaseKey(t *testing.T, seed []byte) *libwallet.HDPrivateKey {
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

func writeTestJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}
