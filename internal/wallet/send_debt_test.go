package wallet

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	libwallet "github.com/muun/libwallet"
)

// Debt affects affordability, not the transaction fee supplied to Apollo's signer.
func TestSendOnchainUsesOperationFeeInSigningExpectations(t *testing.T) {
	const expectedDebt = 2_354

	muunKey := mustTestBaseKey(t, bytes.Repeat([]byte{51}, 32))
	receiverUserKey := mustTestBaseKey(t, bytes.Repeat([]byte{52}, 32))
	receiverMuunKey := mustTestBaseKey(t, bytes.Repeat([]byte{53}, 32))
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
					"outpoint":         "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd:0",
					"status":           "CONFIRMED",
				}},
				"expectedDebtInSat": expectedDebt,
			})
		case "/houston/operations":
			writeTestJSON(t, w, map[string]any{
				"operation": map[string]any{"id": 51},
				"partiallySignedTransaction": map[string]any{
					"hexTransaction": "00",
					"inputs":         []any{},
				},
				"alternativeTransactions": []any{},
			})
		case "/houston/operations/51/raw-transactions":
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

	var signedWith *libwallet.SigningExpectations
	manager.signOutgoingTransaction = func(
		_ *libwallet.HDPrivateKey,
		_ *libwallet.HDPublicKey,
		_ partiallySignedTransactionJSON,
		_ *libwallet.MusigNonces,
		expectations *libwallet.SigningExpectations,
	) (*libwallet.Transaction, error) {
		signedWith = expectations
		return &libwallet.Transaction{Hash: "debt-hash", Bytes: []byte{1}}, nil
	}

	result, err := manager.Send(context.Background(), SendRequest{
		Destination:    receiverAddress.Address(),
		AmountSatoshis: 10_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if signedWith == nil {
		t.Fatal("outgoing transaction was never signed")
	}

	want := libwallet.NewSigningExpectations(
		receiverAddress.Address(),
		result.AmountSatoshis,
		nil,
		result.FeeSatoshis,
		false,
	)
	if !reflect.DeepEqual(signedWith, want) {
		t.Fatalf("signing expectations = %+v, want %+v", signedWith, want)
	}
}

func TestOnchainExpectedDebtStillLimitsSpendableCapacity(t *testing.T) {
	realtime := realtimeData{
		FeeWindow:          &feeWindow{FastConfTarget: 1, TargetedFees: map[string]float64{"1": 5}},
		ExchangeRateWindow: &exchangeRateWindow{ID: 1, Rates: map[string]float64{"BTC": 1}},
	}
	next := nextTransactionSize{SizeProgression: []sizeForAmount{{
		AmountInSatoshis: 20000, SizeInBytes: 400,
		Outpoint: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd:0", Status: "CONFIRMED",
	}}}
	if _, _, err := analyzeOnchainPayment(11000, realtime, next); err != nil {
		t.Fatalf("payment is affordable without debt: %v", err)
	}
	debt := int64(8650)
	next.ExpectedDebtInSat = &debt
	if _, _, err := analyzeOnchainPayment(11000, realtime, next); err == nil {
		t.Fatal("payment plus 500-sat fee exceeds 11350 spendable sats after debt")
	}
}
