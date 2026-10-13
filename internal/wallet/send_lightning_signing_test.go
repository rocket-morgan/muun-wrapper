package wallet

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	libwallet "github.com/muun/libwallet"
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
