package wallet_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
	libwallet "github.com/muun/libwallet"
	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestSetupRecoveryPersistsAndExportsEmergencyKit(t *testing.T) {
	recoveryCode := libwallet.GenerateRecoveryCode()

	muunRoot, err := libwallet.NewHDPrivateKey(bytes.Repeat([]byte{71}, 32), libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	muunKey, err := muunRoot.DeriveTo(basePath)
	if err != nil {
		t.Fatal(err)
	}
	kitMuunKey := muunRoot
	var expectedBasePublicKey string
	var encryptedMuunKey string
	var recoveryCodePublicKey string
	var recoveryCodeChecksum string
	var started bool
	var finished bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/houston/sessions-v2/first":
			var request struct {
				BasePublicKey struct {
					Key string `json:"key"`
				} `json:"basePublicKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			expectedBasePublicKey = request.BasePublicKey.Key
			w.Header().Set("Authorization", "Bearer session-token")
			writeJSON(t, w, map[string]any{
				"cosigningPublicKey":  map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
				"swapServerPublicKey": map[string]string{"key": muunKey.PublicKey().String(), "path": basePath},
			})
		case "/houston/user/external-addresses-record":
			switch r.Method {
			case http.MethodGet:
				writeJSON(t, w, map[string]int{"maxUsedIndex": -1, "maxWatchingIndex": 100})
			case http.MethodPut:
				writeJSON(t, w, map[string]int{"maxUsedIndex": 0, "maxWatchingIndex": 100})
			default:
				t.Fatalf("external address method = %s", r.Method)
			}
		case "/houston/user/challenge/setup/start":
			if r.Method != http.MethodPost {
				t.Fatalf("recovery setup method = %s, want POST", r.Method)
			}
			var request struct {
				Type                string `json:"type"`
				PublicKey           string `json:"passwordSecretPublicKey"`
				Salt                string `json:"passwordSecretSalt"`
				EncryptedPrivateKey string `json:"encryptedPrivateKey"`
				Version             int    `json:"version"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Type != "RECOVERY_CODE" || request.Version != 2 || request.PublicKey == "" {
				t.Fatalf("unexpected recovery setup: %#v", request)
			}
			if _, err := hex.DecodeString(request.Salt); err != nil || len(request.Salt) != 16 {
				t.Fatalf("invalid recovery salt %q: %v", request.Salt, err)
			}
			decryptedUserKey, err := libwallet.KeyDecrypt(request.EncryptedPrivateKey, recoveryCode, libwallet.Mainnet())
			if err != nil {
				t.Fatalf("encryptedPrivateKey is not decryptable with the recovery code: %v", err)
			}
			if decryptedUserKey.Key.PublicKey().Raw() == nil {
				t.Fatal("decrypted setup key has no public key")
			}

			recoveryKey, err := libwallet.RecoveryCodeToKey(recoveryCode, "")
			if err != nil {
				t.Fatal(err)
			}
			if request.PublicKey != recoveryKey.PubKeyHex() {
				t.Fatalf("recovery public key = %q, want %q", request.PublicKey, recoveryKey.PubKeyHex())
			}
			salt, _ := hex.DecodeString(request.Salt)
			encryptedMuunKey, err = recoveryKey.PubKey().EncryptKey(
				kitMuunKey,
				salt,
				0,
				base58.Encode([]byte{libwallet.KeySerializationVersion3}),
			)
			if err != nil {
				t.Fatal(err)
			}
			recoveryCodePublicKey = request.PublicKey
			recoveryCodeChecksum = recoveryKey.PubKey().GetChecksum()
			started = true
			writeJSON(t, w, map[string]string{
				"muunKey":            encryptedMuunKey,
				"muunKeyFingerprint": hex.EncodeToString(kitMuunKey.PublicKey().Fingerprint()),
			})
		case "/houston/user/challenge/setup/finish":
			if r.Method != http.MethodPost {
				t.Fatalf("recovery finish method = %s, want POST", r.Method)
			}
			var request struct {
				Type      string `json:"type"`
				PublicKey string `json:"publicKey"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Type != "RECOVERY_CODE" || request.PublicKey != recoveryCodePublicKey {
				t.Fatalf("unexpected recovery finish: %#v", request)
			}
			finished = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	manager, err := wallet.NewManager(wallet.Config{
		DataDir:           t.TempDir(),
		Passphrase:        "test passphrase",
		HoustonURL:        server.URL + "/houston/",
		PrimaryCurrency:   "USD",
		ClientVersion:     19020002,
		ClientVersionName: "2.16.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(context.Background()); err != nil {
		t.Fatal(err)
	}

	setup, err := manager.SetupRecovery(context.Background(), recoveryCode)
	if err != nil {
		t.Fatal(err)
	}
	if !started || !finished {
		t.Fatalf("started=%t finished=%t", started, finished)
	}
	if setup.RecoveryCodeChecksum != recoveryCodeChecksum || setup.VerificationCode == "" {
		t.Fatalf("unexpected recovery setup result: %#v", setup)
	}

	kit, err := manager.EmergencyKit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if kit.SecondEncryptedKey != encryptedMuunKey || kit.RecoveryCodeChecksum != recoveryCodeChecksum {
		t.Fatalf("unexpected emergency kit: %#v", kit)
	}
	if kit.FirstEncryptedKey == "" || kit.FirstFingerprint == "" || kit.SecondFingerprint == "" || kit.HTML == "" || kit.Metadata == "" {
		t.Fatalf("incomplete emergency kit: %#v", kit)
	}
	if kit.VerificationCode != setup.VerificationCode {
		t.Fatalf("verification code = %q, want %q", kit.VerificationCode, setup.VerificationCode)
	}

	recoveryKey, err := libwallet.RecoveryCodeToKey(recoveryCode, "")
	if err != nil {
		t.Fatal(err)
	}
	decryptedUserKey, err := recoveryKey.DecryptRawKey(kit.FirstEncryptedKey, libwallet.Mainnet())
	if err != nil {
		t.Fatalf("decrypt emergency-kit user key: %v", err)
	}
	expectedKey, err := libwallet.NewHDPublicKeyFromString(expectedBasePublicKey, basePath, libwallet.Mainnet())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decryptedUserKey.Key.PublicKey().Raw(), expectedKey.Raw()) || !bytes.Equal(decryptedUserKey.Key.ChainCode(), expectedKey.ChainCode()) {
		t.Fatal("emergency-kit user key does not match the wallet base key")
	}

	persisted, err := os.ReadFile(filepath.Join(manager.DataDir(), "wallet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), recoveryCode) {
		t.Fatal("wallet record must not persist the recovery code")
	}
	if !strings.Contains(string(persisted), encryptedMuunKey) {
		t.Fatal("wallet record does not persist the encrypted Muun key")
	}
}

func TestSetupRecoveryRejectsLegacyVersionOneCode(t *testing.T) {
	manager, err := wallet.NewManager(wallet.Config{
		DataDir:         t.TempDir(),
		Passphrase:      "test passphrase",
		HoustonURL:      "https://houston.example/",
		PrimaryCurrency: "USD",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.SetupRecovery(context.Background(), "ABCD-EFHJ-KMNP-QRST-UVWX-YZ23-4578-9ABC")
	if err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("SetupRecovery legacy code error = %v, want version 2 rejection", err)
	}
}
