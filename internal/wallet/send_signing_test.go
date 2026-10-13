package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	libwallet "github.com/muun/libwallet"
)

// These synthetic transactions go through Apollo's real Verify and Sign methods.
// No live wallet data or signature-accepting stub is used.
func TestSendOnchainVerifiesChangeWithoutChargingExpectedDebtTwice(t *testing.T) {
	for _, test := range []struct {
		name   string
		debt   int64
		inputs []int64
	}{
		{"incident_debt_one_input", 8650, []int64{1017133}},
		{"incident_debt_multiple_inputs", 8650, []int64{600000, 417133}},
		{"zero_debt_multiple_inputs", 0, []int64{600000, 417133}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, destination, pushes := signingOnchainManager(t, test.debt, test.inputs, 16759)
			request := SendRequest{Destination: destination, AmountSatoshis: 1000000, IdempotencyKey: "signing-change"}
			result, err := manager.Send(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.AmountSatoshis != 1000000 || result.FeeSatoshis != 374 || result.TransactionID == "" {
				t.Fatalf("unexpected signed payment: %+v", result)
			}
			replay, err := manager.Send(context.Background(), request)
			if err != nil || replay != result || *pushes != 1 {
				t.Fatalf("replay=%+v err=%v pushes=%d", replay, err, *pushes)
			}
		})
	}
}

func TestSendOnchainRejectsTamperedChangeBeforePush(t *testing.T) {
	for _, change := range []int64{8109, 16758, 16760} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			manager, destination, pushes := signingOnchainManager(t, 8650, []int64{1017133}, change)
			_, err := manager.Send(context.Background(), SendRequest{Destination: destination, AmountSatoshis: 1000000, IdempotencyKey: "tampered-change"})
			if err == nil || !strings.Contains(err.Error(), "change amount is mismatched") {
				t.Fatalf("expected real change verification rejection, got %v", err)
			}
			if *pushes != 0 {
				t.Fatalf("invalid transaction was pushed %d times", *pushes)
			}
			status, err := manager.SendStatus(context.Background(), "tampered-change")
			if err != nil || status.State != SendInFlight {
				t.Fatalf("created operation must remain ambiguous: status=%+v err=%v", status, err)
			}
		})
	}
}

func signingOnchainManager(t *testing.T, debt int64, amounts []int64, changeAmount int64, mutations ...func(*partiallySignedTransactionJSON, *muunAddressJSON)) (*Manager, string, *int) {
	t.Helper()
	muun := mustTestBaseKey(t, bytes.Repeat([]byte{81}, 32))
	receiver := mustTestBaseKey(t, bytes.Repeat([]byte{82}, 32))
	destination, err := libwallet.CreateAddressV1(receiver.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	var user *libwallet.HDPublicKey
	pushes := new(int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			var request createSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			user, err = libwallet.NewHDPublicKeyFromString(request.BasePublicKey.Key, request.BasePublicKey.Path, libwallet.Mainnet())
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Authorization", "Bearer synthetic-session")
			writeTestJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muun.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muun.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			writeTestJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
		case "/houston/realtime":
			writeTestJSON(t, w, map[string]any{
				"feeWindow":          map[string]any{"id": 7, "targetedFees": map[string]float64{"1": 0.2885}, "fastConfTarget": 1},
				"exchangeRateWindow": map[string]any{"id": 9, "rates": map[string]float64{"BTC": 1, "USD": 50000}},
			})
		case "/houston/operations/next-transaction-size":
			progression := make([]sizeForAmount, len(amounts))
			var total int64
			for index, amount := range amounts {
				total += amount
				progression[index] = sizeForAmount{AmountInSatoshis: total, SizeInBytes: 5178, Outpoint: fmt.Sprintf("%064x:0", index+1), Status: "CONFIRMED"}
			}
			writeTestJSON(t, w, nextTransactionSize{SizeProgression: progression, ExpectedDebtInSat: &debt})
		case "/houston/operations":
			var request outgoingOperationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if request.Amount.InSatoshis == nil || *request.Amount.InSatoshis != 1000000 || request.Fee.InSatoshis == nil || *request.Fee.InSatoshis != 374 || len(request.Outpoints) != len(amounts) {
				t.Errorf("unexpected payment analysis: %+v", request)
			}
			change := syntheticWalletAddress(t, user, muun.PublicKey(), basePath+"/change:0/1")
			partial := syntheticPartial(t, user, amounts, destination.Address(), 1000000, change.Address, changeAmount)
			for _, mutate := range mutations {
				mutate(&partial, &change)
			}
			id := int64(91)
			writeTestJSON(t, w, operationCreatedJSON{Operation: operationJSON{ID: &id}, PartiallySignedTransaction: &partial, ChangeAddress: &change})
		case "/houston/operations/91/raw-transactions":
			*pushes++
			var pushed pushTransactionsRequest
			if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil || pushed.RawTransaction == nil || pushed.RawTransaction.Hex == "" {
				t.Errorf("missing signed transaction: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	manager, err := NewManager(Config{DataDir: t.TempDir(), Passphrase: "synthetic passphrase", HoustonURL: server.URL + "/houston/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	return manager, destination.Address(), pushes
}

func TestSendOnchainRejectsTamperedDestinationAmountAndChangeIdentity(t *testing.T) {
	for _, name := range []string{"destination", "amount", "change_identity"} {
		t.Run(name, func(t *testing.T) {
			mutate := func(partial *partiallySignedTransactionJSON, change *muunAddressJSON) {
				if name == "change_identity" {
					change.DerivationPath = basePath + "/change:0/2"
					return
				}
				raw, err := hex.DecodeString(partial.HexTransaction)
				if err != nil {
					t.Fatal(err)
				}
				tx := wire.NewMsgTx(2)
				if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
					t.Fatal(err)
				}
				if name == "destination" {
					tx.TxOut[0].PkScript = []byte{txscript.OP_TRUE}
				} else {
					tx.TxOut[0].Value = 999999
				}
				var changed bytes.Buffer
				if err := tx.Serialize(&changed); err != nil {
					t.Fatal(err)
				}
				partial.HexTransaction = hex.EncodeToString(changed.Bytes())
			}
			manager, destination, pushes := signingOnchainManager(t, 8650, []int64{1017133}, 16759, mutate)
			_, err := manager.Send(context.Background(), SendRequest{Destination: destination, AmountSatoshis: 1000000, IdempotencyKey: "tampered-identity"})
			if err == nil || !strings.Contains(err.Error(), "verify partially signed transaction") || *pushes != 0 {
				t.Fatalf("tampered %s must fail real verification without push: err=%v pushes=%d", name, err, *pushes)
			}
		})
	}
}

func syntheticWalletAddress(t *testing.T, user, muun *libwallet.HDPublicKey, path string) muunAddressJSON {
	t.Helper()
	userChild, err := user.DeriveTo(path)
	if err != nil {
		t.Fatal(err)
	}
	muunChild, err := muun.DeriveTo(path)
	if err != nil {
		t.Fatal(err)
	}
	address, err := libwallet.CreateAddressV5(userChild, muunChild)
	if err != nil {
		t.Fatal(err)
	}
	return muunAddressJSON{Version: address.Version(), DerivationPath: address.DerivationPath(), Address: address.Address()}
}

func syntheticPartial(t *testing.T, user *libwallet.HDPublicKey, amounts []int64, destination string, amount int64, change string, changeAmount int64) partiallySignedTransactionJSON {
	t.Helper()
	tx := wire.NewMsgTx(2)
	partial := partiallySignedTransactionJSON{}
	for index, inputAmount := range amounts {
		path := fmt.Sprintf("%s/external:1/%d", basePath, index)
		key, err := user.DeriveTo(path)
		if err != nil {
			t.Fatal(err)
		}
		address, err := libwallet.CreateAddressV1(key)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := chainhash.NewHashFromStr(fmt.Sprintf("%064x", index+1))
		if err != nil {
			t.Fatal(err)
		}
		tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(hash, 0), nil, nil))
		partial.Inputs = append(partial.Inputs, partiallySignedInputJSON{
			PrevOut: previousOutputJSON{TxID: hex.EncodeToString(hash[:]), Amount: inputAmount},
			Address: muunAddressJSON{Version: address.Version(), DerivationPath: path, Address: address.Address()},
		})
	}
	for _, output := range []struct {
		address string
		amount  int64
	}{{destination, amount}, {change, changeAmount}} {
		if output.address == "" {
			continue
		}
		address, err := btcutil.DecodeAddress(output.address, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatal(err)
		}
		script, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatal(err)
		}
		tx.AddTxOut(wire.NewTxOut(output.amount, script))
	}
	var raw bytes.Buffer
	if err := tx.Serialize(&raw); err != nil {
		t.Fatal(err)
	}
	partial.HexTransaction = hex.EncodeToString(raw.Bytes())
	return partial
}
