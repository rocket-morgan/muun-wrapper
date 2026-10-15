package wallet

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	libwallet "github.com/muun/libwallet"
	"github.com/muun/libwallet/btcsuitew/txscriptw"
	"github.com/muun/libwallet/musig"
)

func TestLightningAlternativeVerificationRequiresReducedDestination(t *testing.T) {
	user := mustTestBaseKey(t, bytes.Repeat([]byte{83}, 32))
	muun := mustTestBaseKey(t, bytes.Repeat([]byte{84}, 32))
	receiver := mustTestBaseKey(t, bytes.Repeat([]byte{85}, 32))
	destination, err := libwallet.CreateAddressV1(receiver.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	change := syntheticWalletAddress(t, user.PublicKey(), muun.PublicKey(), basePath+"/change:0/1")
	for _, test := range []struct {
		name       string
		funding    int64
		actual     int64
		omit       bool
		wantReject bool
	}{
		{"minimum_equal", 546, 546, false, true},
		{"minimum_reduced", 546, 545, false, false},
		{"destination_omitted", 546, 0, true, false},
		{"larger_payment_reduced", 1005, 900, false, false},
		{"larger_payment_equal", 1005, 1005, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			to := destination.Address()
			if test.omit {
				to = ""
			}
			partial := syntheticPartial(t, user.PublicKey(), []int64{10000}, to, test.actual, change.Address, 10000-test.funding-29)
			expectations := libwallet.NewSigningExpectations(destination.Address(), test.funding, walletAddress{
				version: change.Version, derivationPath: change.DerivationPath, address: change.Address,
			}, 29, true)
			_, err := signPartiallySignedTransaction(user, muun.PublicKey(), partial, libwallet.GenerateMusigNonces(1), expectations)
			if test.wantReject {
				if err == nil || !strings.Contains(err.Error(), "destination amount is mismatched") {
					t.Fatalf("expected strict Apollo alternative rejection, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Alternative check log fields: phase suffix, then index, outputs and deltas.
const identicalAlternativeLog = "fallback index=0 outputs=2 destination_delta=0 change_delta=0 fee_delta=0"

// The transport is synthetic, but swap validation, transaction Verify/Sign and
// durable replay are real. This does not establish Houston's live acceptance.
func TestSendLightningNegotiatesAlternativesWithRealSigner(t *testing.T) {
	for _, test := range []struct {
		name                 string
		amount, funding      int64
		debtType             string
		debt                 int64
		confirmations        int
		advertised, wantAlts int
		inputs               int
		unsolicited          bool
		mutation, wantError  string
		wantLog              string
	}{
		// Houston pads sub-dust swaps to a 546 sat funding output and still asks
		// for alternatives, which repeat the principal's outputs and fee.
		{name: "ramp_on_93", amount: 93, funding: 546, advertised: 1, wantAlts: 1, wantLog: identicalAlternativeLog},
		{name: "probe_100", amount: 100, funding: 546, advertised: 2, wantAlts: 2, wantLog: identicalAlternativeLog},
		{name: "ramp_on_228", amount: 228, funding: 546, advertised: 1, wantAlts: 1, wantLog: identicalAlternativeLog},
		{name: "minimum_without_padding", amount: 541, funding: 546, advertised: 1, wantAlts: 1, wantLog: identicalAlternativeLog},
		{name: "above_minimum", amount: 542, funding: 547, advertised: 1, wantAlts: 1},
		{name: "larger_payment", amount: 10000, funding: 10005, advertised: 2, wantAlts: 2},
		{name: "minimum_multiple_inputs", amount: 100, funding: 546, advertised: 1, wantAlts: 1, inputs: 2, wantLog: identicalAlternativeLog},
		{name: "collect_at_minimum", amount: 93, funding: 546, debtType: "COLLECT", debt: 100, advertised: 1, wantAlts: 1, wantLog: identicalAlternativeLog},
		{name: "small_invoice_larger_collect_funding", amount: 93, funding: 900, debtType: "COLLECT", debt: 800, advertised: 1, wantAlts: 1},
		{name: "confirmed_minimum", amount: 100, funding: 546, confirmations: 1, advertised: 1},
		{name: "lend", amount: 100, debtType: "LEND", debt: 105, advertised: 1},
		{name: "server_requests_none", amount: 10000, funding: 10005},
		{name: "negative_advertised_count", amount: 100, funding: 546, advertised: -1, wantError: "negative alternative transaction count"},
		{name: "unsolicited_alternative", amount: 100, funding: 546, unsolicited: true, wantError: "more alternative transactions than requested"},
		{name: "wrong_primary_amount", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "amount", wantError: "destination amount is mismatched"},
		{name: "wrong_primary_change", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "change", wantError: "change amount is mismatched"},
		{name: "wrong_change_identity", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "change_identity", wantError: "mismatched change address"},
		{name: "wrong_primary_nonce", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "primary_nonce", wantError: "sign Lightning funding transaction"},
		// A signing failure is not a verification failure: no fallback, even at dust.
		{name: "wrong_alternative_nonce_group", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "alternative_nonce", wantError: "sign Lightning alternative transaction"},
		// The principal's strict check bounds the fallback: no extra amount, fee or output.
		{name: "dust_alternative_pays_more", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "alternative_more", wantError: "found 547 expected 546", wantLog: "rejected index=0 outputs=2 destination_delta=1 change_delta=-1 fee_delta=0"},
		{name: "dust_alternative_fee_bump", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "alternative_fee_bump", wantError: "change amount is mismatched", wantLog: "rejected index=0 outputs=2 destination_delta=0 change_delta=-50 fee_delta=50"},
		{name: "dust_alternative_extra_output", amount: 100, funding: 546, advertised: 1, wantAlts: 1, mutation: "alternative_extra_output", wantError: "expected destination and change outputs but found 3", wantLog: "rejected index=0 outputs=3 destination_delta=0 change_delta=-1000 fee_delta=0"},
		// Above the dust floor an equal alternative stays rejected, as in Falcon 2.16.0.
		{name: "just_above_dust_equal_alternative", amount: 542, funding: 547, advertised: 1, wantAlts: 1, mutation: "alternative_equal", wantError: "found 547 expected at most 547"},
		{name: "equal_larger_alternative", amount: 10000, funding: 10005, advertised: 1, wantAlts: 1, mutation: "alternative_equal", wantError: "found 10005 expected at most 10005"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.debtType == "" {
				test.debtType = "NONE"
			}
			if test.inputs == 0 {
				test.inputs = 1
			}
			invoice, parsed := mustTestLightningInvoice(t, test.amount)
			muun := mustTestBaseKey(t, bytes.Repeat([]byte{86}, 32))
			swapServer := mustTestBaseKey(t, bytes.Repeat([]byte{87}, 32))
			var user *libwallet.HDPublicKey
			var swapAddress string
			var createdInputs []partiallySignedInputJSON
			var private []string
			var pushes, creations int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/houston/sessions-v2/first":
					var request createSessionRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					var err error
					user, err = libwallet.NewHDPublicKeyFromString(request.BasePublicKey.Key, request.BasePublicKey.Path, libwallet.Mainnet())
					if err != nil {
						t.Error(err)
						return
					}
					w.Header().Set("Authorization", "Bearer synthetic-session")
					writeTestJSON(t, w, map[string]any{
						"cosigningPublicKey":  map[string]string{"key": muun.PublicKey().String(), "path": basePath},
						"swapServerPublicKey": map[string]string{"key": swapServer.PublicKey().String(), "path": basePath},
					})
				case "/houston/user/external-addresses-record":
					writeTestJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
				case "/houston/operations/sswap/create":
					swap := testV2Swap(t, parsed, user, muun.PublicKey(), swapServer.PublicKey(), test.debtType, test.funding)
					funding := swap["fundingOutput"].(map[string]any)
					funding["debtAmountInSats"] = test.debt
					funding["confirmationsNeeded"] = test.confirmations
					if test.debtType == "LEND" {
						funding["outputAmountInSatoshis"] = nil
					} else {
						swap["fees"].(map[string]any)["sweepInSats"] = test.funding - test.amount - 5 - test.debt
					}
					swap["maxAlternativeTransactionCount"] = test.advertised
					swapAddress = funding["outputAddress"].(string)
					private = append(private, swapAddress)
					writeTestJSON(t, w, swap)
				case "/houston/realtime":
					realtime := previewRealtime()
					realtime["feeWindow"].(map[string]any)["targetedFees"] = map[string]float64{"0": 5, "1": 5}
					writeTestJSON(t, w, realtime)
				case "/houston/operations/next-transaction-size":
					progression := make([]sizeForAmount, test.inputs)
					for i := range progression {
						progression[i] = sizeForAmount{AmountInSatoshis: int64(i+1) * 20000, SizeInBytes: 400, Outpoint: fmt.Sprintf("%064x:0", i+1), Status: "CONFIRMED"}
					}
					writeTestJSON(t, w, nextTransactionSize{SizeProgression: progression})
				case "/houston/operations":
					var request outgoingOperationRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						return
					}
					creations++
					fee := int64(500)
					if test.debtType == "LEND" {
						fee = 0
					}
					if request.Amount.InSatoshis == nil || *request.Amount.InSatoshis != test.amount || request.Fee.InSatoshis == nil || *request.Fee.InSatoshis != fee {
						t.Errorf("unexpected amount or network fee: %+v", request)
					}
					if len(request.Outpoints) != test.inputs || len(request.UserPublicNoncesHex) != test.inputs*(1+test.wantAlts) {
						t.Errorf("unexpected outpoints/nonces: %d/%d", len(request.Outpoints), len(request.UserPublicNoncesHex))
					}
					id := int64(92)
					created := operationCreatedJSON{Operation: operationJSON{ID: &id}}
					if test.debtType != "LEND" {
						change := syntheticWalletAddress(t, user, muun.PublicKey(), basePath+"/change:0/1")
						amounts := make([]int64, test.inputs)
						for i := range amounts {
							amounts[i] = 20000
						}
						changeAmount := int64(test.inputs)*20000 - test.funding - fee
						primaryAmount := test.funding
						if test.mutation == "amount" {
							primaryAmount--
						} else if test.mutation == "change" {
							changeAmount++
						}
						primaryNonces := request.UserPublicNoncesHex[:test.inputs]
						if test.mutation == "primary_nonce" {
							primaryNonces = musigPublicNonces(libwallet.GenerateMusigNonces(test.inputs))
						}
						partial := syntheticTaprootPartial(t, user, muun, amounts, swapAddress, primaryAmount, change.Address, changeAmount, primaryNonces)
						createdInputs = partial.Inputs
						private = append(private, change.Address, partial.HexTransaction)
						created.PartiallySignedTransaction, created.ChangeAddress = &partial, &change
						if test.mutation == "change_identity" {
							change.DerivationPath = basePath + "/change:0/2"
						}
						count := len(request.UserPublicNoncesHex)/test.inputs - 1
						if test.unsolicited {
							count = 1
						}
						for i := 0; i < count; i++ {
							alternativeAmount, alternativeChange := test.funding, changeAmount
							var extra []*wire.TxOut
							switch test.mutation {
							case "alternative_equal":
							case "alternative_nonce":
								alternativeAmount--
							case "alternative_more":
								alternativeAmount, alternativeChange = test.funding+1, changeAmount-1
							case "alternative_fee_bump":
								alternativeChange -= 50
							case "alternative_extra_output":
								alternativeChange -= 1000
								extra = append(extra, wire.NewTxOut(1000, []byte{txscript.OP_TRUE}))
							default:
								if test.funding > 546 {
									alternativeAmount--
								}
							}
							nonceGroup := (i + 1) * test.inputs
							if test.unsolicited || test.mutation == "alternative_nonce" {
								nonceGroup = 0
							}
							alternative := syntheticTaprootPartial(t, user, muun, amounts, swapAddress, alternativeAmount, change.Address, alternativeChange, request.UserPublicNoncesHex[nonceGroup:nonceGroup+test.inputs], extra...)
							created.AlternativeTransactions = append(created.AlternativeTransactions, alternative)
							private = append(private, alternative.HexTransaction)
						}
					}
					writeTestJSON(t, w, created)
				case "/houston/operations/92/raw-transactions":
					pushes++
					var pushed pushTransactionsRequest
					if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
						t.Error(err)
					}
					if (pushed.RawTransaction == nil) != (test.debtType == "LEND") || len(pushed.AlternativeTransactions) != test.wantAlts {
						t.Errorf("unexpected pushed transactions: %+v", pushed)
					}
					if pushed.RawTransaction != nil {
						for _, payload := range append([]rawTransactionPayload{*pushed.RawTransaction}, pushed.AlternativeTransactions...) {
							assertSyntheticTaprootWitness(t, payload.Hex, createdInputs)
							private = append(private, payload.Hex)
						}
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			manager, err := NewManager(Config{DataDir: t.TempDir(), Passphrase: "synthetic passphrase", HoustonURL: server.URL + "/houston/"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Create(context.Background()); err != nil {
				t.Fatal(err)
			}
			var captured bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&captured)
			t.Cleanup(func() { log.SetOutput(previous) })
			request := SendRequest{Destination: invoice, AmountSatoshis: test.amount, IdempotencyKey: "negotiated-lightning"}
			result, err := manager.Send(context.Background(), request)
			logged := captured.String()
			if test.wantLog == "" {
				if strings.Contains(logged, "send_alternative_check") {
					t.Errorf("unexpected alternative check log %q", logged)
				}
			} else {
				phase, fields, _ := strings.Cut(test.wantLog, " ")
				want := fmt.Sprintf("send_alternative_check phase=sign_lightning_alternative_%s request_id=%s operation_id=92 %s\n", phase, requestIDForIdempotencyKey(request.IdempotencyKey), fields)
				if !strings.Contains(logged, want) {
					t.Errorf("missing %q in alternative check log %q", want, logged)
				}
			}
			for _, secret := range append(private, invoice, request.IdempotencyKey, "synthetic-session") {
				if strings.Contains(logged, secret) {
					t.Error("send log contains private payment data")
				}
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || pushes != 0 {
					t.Fatalf("expected %q without a push: error=%v pushes=%d", test.wantError, err, pushes)
				}
				status, statusErr := manager.SendStatus(context.Background(), request.IdempotencyKey)
				wantState := SendInFlight
				if test.advertised < 0 {
					wantState = SendFailedPreSubmit
				}
				if statusErr != nil || status.State != wantState {
					t.Fatalf("state=%+v error=%v want=%s", status, statusErr, wantState)
				}
				if wantState == SendInFlight {
					_, replayErr := manager.Send(context.Background(), request)
					if !errors.Is(replayErr, ErrSendInProgress) || creations != 1 || pushes != 0 {
						t.Fatalf("ambiguous attempt was retried: error=%v creations=%d pushes=%d", replayErr, creations, pushes)
					}
				} else if creations != 0 {
					t.Fatalf("preflight rejection created %d operations", creations)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.OperationID != 92 || result.AmountSatoshis != test.amount || result.PaymentHash == "" || (result.TransactionID == "") != (test.debtType == "LEND") {
				t.Fatalf("invalid result: %+v", result)
			}
			replay, err := manager.SendWithOutcome(context.Background(), request)
			if err != nil || !replay.Replay || replay.Result != result || pushes != 1 || creations != 1 {
				t.Fatalf("replay=%+v error=%v pushes=%d creations=%d", replay, err, pushes, creations)
			}
		})
	}
}

// Co-sign each synthetic V5 input against the exact advertised nonce group.
// Unlike a V1 fixture, a mismatched primary/alternative group cannot sign.
func syntheticTaprootPartial(t *testing.T, user *libwallet.HDPublicKey, muun *libwallet.HDPrivateKey, amounts []int64, destination string, amount int64, change string, changeAmount int64, userNonces []string, extra ...*wire.TxOut) partiallySignedTransactionJSON {
	t.Helper()
	partial := syntheticPartial(t, user, amounts, destination, amount, change, changeAmount)
	raw, err := hex.DecodeString(partial.HexTransaction)
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(2)
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if len(extra) > 0 {
		tx.TxOut = append(tx.TxOut, extra...)
		var extended bytes.Buffer
		if err := tx.Serialize(&extended); err != nil {
			t.Fatal(err)
		}
		partial.HexTransaction = hex.EncodeToString(extended.Bytes())
	}
	prevOuts := make([]*wire.TxOut, len(partial.Inputs))
	for i := range partial.Inputs {
		input := &partial.Inputs[i]
		input.Address = syntheticWalletAddress(t, user, muun.PublicKey(), input.Address.DerivationPath)
		address, err := btcutil.DecodeAddress(input.Address.Address, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatal(err)
		}
		script, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatal(err)
		}
		prevOuts[i] = wire.NewTxOut(input.PrevOut.Amount, script)
	}
	hashes := txscriptw.NewTaprootSigHashes(tx, prevOuts)
	for i := range partial.Inputs {
		input := &partial.Inputs[i]
		userKey, err := user.DeriveTo(input.Address.DerivationPath)
		if err != nil {
			t.Fatal(err)
		}
		muunKey, err := muun.DeriveTo(input.Address.DerivationPath)
		if err != nil {
			t.Fatal(err)
		}
		privateKey, err := muunKey.ECPrivateKey()
		if err != nil {
			t.Fatal(err)
		}
		digest, err := txscriptw.CalcTaprootSigHash(tx, hashes, i, txscript.SigHashAll)
		if err != nil {
			t.Fatal(err)
		}
		userNonce, err := hex.DecodeString(userNonces[i])
		if err != nil || len(userNonce) != 66 {
			t.Fatalf("invalid synthetic user nonce: %v", err)
		}
		sessionID := musig.RandomSessionID()
		nonce, err := musig.MuSig2GenerateNonce(musig.Musig2v040Muun, sessionID[:], nil)
		if err != nil {
			t.Fatal(err)
		}
		signature, err := musig.ComputeMuunPartialSignature(musig.Musig2v040Muun, digest, userKey.Raw(), privateKey.Serialize(), userNonce, sessionID[:], musig.KeySpendOnlyTweak())
		if err != nil {
			t.Fatal(err)
		}
		input.RawMuunPublicNonceHex = hex.EncodeToString(nonce.PubNonce[:])
		input.MuunSignature = &signatureJSON{Hex: hex.EncodeToString(signature)}
	}
	return partial
}

// Independently verify the final witness with btcd's Bitcoin script engine.
func assertSyntheticTaprootWitness(t *testing.T, rawHex string, inputs []partiallySignedInputJSON) {
	t.Helper()
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		t.Fatal(err)
	}
	tx := wire.NewMsgTx(2)
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	fetcher := txscript.NewMultiPrevOutFetcher(nil)
	for i, input := range inputs {
		address, err := btcutil.DecodeAddress(input.Address.Address, &chaincfg.MainNetParams)
		if err != nil {
			t.Fatal(err)
		}
		script, err := txscript.PayToAddrScript(address)
		if err != nil {
			t.Fatal(err)
		}
		fetcher.AddPrevOut(tx.TxIn[i].PreviousOutPoint, wire.NewTxOut(input.PrevOut.Amount, script))
	}
	hashes := txscript.NewTxSigHashes(tx, fetcher)
	for i, input := range inputs {
		previous := fetcher.FetchPrevOutput(tx.TxIn[i].PreviousOutPoint)
		engine, err := txscript.NewEngine(previous.PkScript, tx, i, txscript.StandardVerifyFlags, nil, hashes, input.PrevOut.Amount, fetcher)
		if err != nil {
			t.Fatal(err)
		}
		if err := engine.Execute(); err != nil {
			t.Fatalf("invalid collaborative Taproot witness: %v", err)
		}
	}
}

func TestLightningSwapCreationFailureLogsCorrelatedSafeMetadata(t *testing.T) {
	var captured bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/houston/operations/sswap/create" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
		writeTestJSON(t, w, map[string]string{"message": "private-invoice private-session private-provider-body"})
	}))
	defer server.Close()
	user := mustTestBaseKey(t, bytes.Repeat([]byte{88}, 32))
	muun := mustTestBaseKey(t, bytes.Repeat([]byte{89}, 32))
	encrypted, err := libwallet.KeyEncrypt(user, "synthetic passphrase")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Config{DataDir: t.TempDir(), Passphrase: "synthetic passphrase", HoustonURL: server.URL + "/houston/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.saveRecord(walletRecord{Version: 1, EncryptedBasePrivateKey: encrypted, MuunCosigningPublicKey: muun.PublicKey().String(), SessionToken: "private-session"}); err != nil {
		t.Fatal(err)
	}
	invoice, _ := mustTestLightningInvoice(t, 100)
	_, err = manager.Send(context.Background(), SendRequest{Destination: invoice, AmountSatoshis: 100, IdempotencyKey: "preflight-key"})
	if err == nil {
		t.Fatal("expected Houston swap creation failure")
	}
	for _, want := range []string{"phase=create_lightning_swap", "request_id=" + requestIDForIdempotencyKey("preflight-key"), "operation_id=0", "houston_status=500"} {
		if !strings.Contains(captured.String(), want) {
			t.Errorf("missing %q from correlated preflight log %q", want, captured.String())
		}
	}
	for _, secret := range []string{invoice, "private-session", "private-provider-body", "preflight-key", encrypted} {
		if strings.Contains(captured.String(), secret) {
			t.Error("preflight log contains private payment data")
		}
	}
	status, err := manager.SendStatus(context.Background(), "preflight-key")
	if err != nil || status.State != SendFailedPreSubmit {
		t.Fatalf("no outgoing operation was created: state=%+v error=%v", status, err)
	}
}
