package wallet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	libwallet "github.com/muun/libwallet"
)

// RecoverySetup confirms that Houston accepted a recovery code and that an emergency kit was
// created locally. The recovery code itself is never persisted or returned.
type RecoverySetup struct {
	RecoveryCodeChecksum string `json:"recoveryCodeChecksum"`
	VerificationCode     string `json:"verificationCode"`
}

// EmergencyKit contains the encrypted material needed by Muun's recovery tooling. It can be
// decrypted only with the recovery code used during SetupRecovery.
type EmergencyKit struct {
	Version              int    `json:"version"`
	FirstEncryptedKey    string `json:"firstEncryptedKey"`
	FirstFingerprint     string `json:"firstFingerprint"`
	SecondEncryptedKey   string `json:"secondEncryptedKey"`
	SecondFingerprint    string `json:"secondFingerprint"`
	RecoveryCodeChecksum string `json:"recoveryCodeChecksum"`
	VerificationCode     string `json:"verificationCode"`
	HTML                 string `json:"html"`
	Metadata             string `json:"metadata"`
}

type recoverySetupRequest struct {
	Type                string `json:"type"`
	PublicKey           string `json:"passwordSecretPublicKey"`
	Salt                string `json:"passwordSecretSalt"`
	EncryptedPrivateKey string `json:"encryptedPrivateKey"`
	Version             int    `json:"version"`
}

type recoverySetupResponse struct {
	MuunKey            string `json:"muunKey"`
	MuunKeyFingerprint string `json:"muunKeyFingerprint"`
}

type recoverySetupFinishRequest struct {
	Type      string `json:"type"`
	PublicKey string `json:"publicKey"`
}

// SetupRecovery registers a recovery-code challenge key with Houston and builds the material for
// the emergency kit. Callers must save the supplied recovery code outside this wallet.
func (m *Manager) SetupRecovery(ctx context.Context, recoveryCode string) (RecoverySetup, error) {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()

	recoveryCode = strings.TrimSpace(recoveryCode)
	if err := libwallet.ValidateRecoveryCode(recoveryCode); err != nil {
		return RecoverySetup{}, fmt.Errorf("validate recovery code: %w", err)
	}

	record, err := m.loadRecord()
	if err != nil {
		return RecoverySetup{}, err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return RecoverySetup{}, err
	}

	recoveryKey, err := libwallet.RecoveryCodeToKey(recoveryCode, "")
	if err != nil {
		return RecoverySetup{}, fmt.Errorf("derive recovery challenge key: %w", err)
	}
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return RecoverySetup{}, fmt.Errorf("generate recovery challenge salt: %w", err)
	}
	encryptedPrivateKey, err := libwallet.KeyEncrypt(basePrivateKey, recoveryCode)
	if err != nil {
		return RecoverySetup{}, fmt.Errorf("encrypt wallet key for recovery setup: %w", err)
	}

	setupPayload := recoverySetupRequest{
		Type:                "RECOVERY_CODE",
		PublicKey:           recoveryKey.PubKeyHex(),
		Salt:                hex.EncodeToString(salt),
		EncryptedPrivateKey: encryptedPrivateKey,
		Version:             2,
	}
	var setupResponse recoverySetupResponse
	if _, err := m.doJSON(
		ctx,
		http.MethodPost,
		"user/challenge/setup/start",
		record.SessionToken,
		setupPayload,
		&setupResponse,
	); err != nil {
		return RecoverySetup{}, fmt.Errorf("start recovery-code setup: %w", err)
	}
	if setupResponse.MuunKey == "" || setupResponse.MuunKeyFingerprint == "" {
		return RecoverySetup{}, errors.New("Houston recovery setup response has no encrypted Muun key")
	}

	if err := m.verifyEncryptedMuunKey(recoveryKey, setupResponse.MuunKey, record); err != nil {
		return RecoverySetup{}, err
	}
	encryptedUserKey, err := recoveryKey.PubKey().EncryptKey(
		basePrivateKey,
		salt,
		0,
		setupResponse.MuunKey,
	)
	if err != nil {
		return RecoverySetup{}, fmt.Errorf("encrypt wallet key for emergency kit: %w", err)
	}
	if _, err := m.doJSON(
		ctx,
		http.MethodPost,
		"user/challenge/setup/finish",
		record.SessionToken,
		recoverySetupFinishRequest{Type: "RECOVERY_CODE", PublicKey: recoveryKey.PubKeyHex()},
		nil,
	); err != nil {
		return RecoverySetup{}, fmt.Errorf("finish recovery-code setup: %w", err)
	}

	record.RecoveryCodePublicKey = recoveryKey.PubKeyHex()
	record.RecoveryCodeSalt = hex.EncodeToString(salt)
	record.EmergencyKitUserKey = encryptedUserKey
	record.EmergencyKitMuunKey = setupResponse.MuunKey
	record.EmergencyKitMuunKeyFP = setupResponse.MuunKeyFingerprint
	kit, err := emergencyKitFromRecord(record, basePrivateKey)
	if err != nil {
		return RecoverySetup{}, err
	}
	if err := m.saveRecord(record); err != nil {
		return RecoverySetup{}, err
	}

	return RecoverySetup{
		RecoveryCodeChecksum: kit.RecoveryCodeChecksum,
		VerificationCode:     kit.VerificationCode,
	}, nil
}

// EmergencyKit rebuilds the portable emergency-kit material from the encrypted keys stored in the
// wallet record. The recovery code is intentionally not part of the result.
func (m *Manager) EmergencyKit(ctx context.Context) (EmergencyKit, error) {
	if err := ctx.Err(); err != nil {
		return EmergencyKit{}, err
	}
	record, err := m.loadRecord()
	if err != nil {
		return EmergencyKit{}, err
	}
	basePrivateKey, err := m.decryptBasePrivateKey(record)
	if err != nil {
		return EmergencyKit{}, err
	}
	return emergencyKitFromRecord(record, basePrivateKey)
}

func (m *Manager) verifyEncryptedMuunKey(
	recoveryKey *libwallet.ChallengePrivateKey,
	encryptedMuunKey string,
	record walletRecord,
) error {
	decryptedMuunKey, err := recoveryKey.DecryptRawKey(encryptedMuunKey, libwallet.Mainnet())
	if err != nil {
		return fmt.Errorf("decrypt Houston encrypted Muun key: %w", err)
	}
	expectedMuunKey, err := libwallet.NewHDPublicKeyFromString(
		record.MuunCosigningPublicKey,
		basePath,
		libwallet.Mainnet(),
	)
	if err != nil {
		return fmt.Errorf("decode Muun cosigning key: %w", err)
	}
	if sameHDPublicKey(decryptedMuunKey.Key.PublicKey(), expectedMuunKey) {
		return nil
	}
	derivedMuunKey, err := decryptedMuunKey.Key.DeriveTo(basePath)
	if err != nil {
		return fmt.Errorf("derive decrypted Muun key to wallet base path: %w", err)
	}
	if !sameHDPublicKey(derivedMuunKey.PublicKey(), expectedMuunKey) {
		return errors.New("Houston encrypted Muun key does not match this wallet")
	}
	return nil
}

func sameHDPublicKey(first, second *libwallet.HDPublicKey) bool {
	return bytes.Equal(first.Raw(), second.Raw()) && bytes.Equal(first.ChainCode(), second.ChainCode())
}

func emergencyKitFromRecord(record walletRecord, basePrivateKey *libwallet.HDPrivateKey) (EmergencyKit, error) {
	if record.RecoveryCodePublicKey == "" || record.EmergencyKitUserKey == "" ||
		record.EmergencyKitMuunKey == "" || record.EmergencyKitMuunKeyFP == "" {
		return EmergencyKit{}, errors.New("recovery setup has not been completed")
	}
	recoveryCodePublicKey, err := hex.DecodeString(record.RecoveryCodePublicKey)
	if err != nil || len(recoveryCodePublicKey) == 0 {
		return EmergencyKit{}, errors.New("wallet record has an invalid recovery-code public key")
	}
	challengePublicKey, err := libwallet.NewChallengePublicKeyFromSerialized(recoveryCodePublicKey)
	if err != nil {
		return EmergencyKit{}, fmt.Errorf("decode recovery-code public key: %w", err)
	}

	params := &libwallet.EKInput{
		FirstEncryptedKey:  record.EmergencyKitUserKey,
		FirstFingerprint:   hex.EncodeToString(basePrivateKey.PublicKey().Fingerprint()),
		SecondEncryptedKey: record.EmergencyKitMuunKey,
		SecondFingerprint:  record.EmergencyKitMuunKeyFP,
		RcChecksum:         challengePublicKey.GetChecksum(),
	}
	generated, err := libwallet.GenerateEmergencyKitHTML(params, "en")
	if err != nil {
		return EmergencyKit{}, fmt.Errorf("generate emergency kit: %w", err)
	}
	return EmergencyKit{
		Version:              generated.Version,
		FirstEncryptedKey:    params.FirstEncryptedKey,
		FirstFingerprint:     params.FirstFingerprint,
		SecondEncryptedKey:   params.SecondEncryptedKey,
		SecondFingerprint:    params.SecondFingerprint,
		RecoveryCodeChecksum: params.RcChecksum,
		VerificationCode:     generated.VerificationCode,
		HTML:                 generated.HTML,
		Metadata:             generated.Metadata,
	}, nil
}
