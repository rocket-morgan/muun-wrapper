package wallet

import (
	"strings"
	"testing"
)

func TestInputFromJSONAcceptsIncomingSwapAddress(t *testing.T) {
	t.Parallel()

	raw := partiallySignedInputJSON{
		PrevOut: previousOutputJSON{
			TxID:   strings.Repeat("aa", 32),
			Index:  0,
			Amount: 10_000,
		},
		Address: muunAddressJSON{
			Version:        201,
			DerivationPath: "m/schema:1'/recovery:1'/incoming:1/0",
			Address:        "bc1ptest",
		},
		IncomingSwap: &incomingSwapInputJSON{
			SphinxHex:              "",
			HtlcTxHex:              "00",
			SwapServerPublicKeyHex: "11",
			PaymentHash256Hex:      strings.Repeat("bb", 32),
			ExpirationHeight:       800000,
			CollectInSats:          0,
			PreimageHex:            strings.Repeat("cc", 32),
			HtlcOutputKeyPath:      "m/schema:1'/recovery:1'/incoming:1/0",
		},
	}
	input, err := inputFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if input.Address().Version() != 201 {
		t.Fatalf("version = %d, want 201", input.Address().Version())
	}
	if input.IncomingSwap() == nil {
		t.Fatal("incoming swap data was not attached to the input")
	}
	if got := input.IncomingSwap().HtlcOutputKeyPath(); got != raw.IncomingSwap.HtlcOutputKeyPath {
		t.Fatalf("htlc path = %q", got)
	}
}

func TestInputFromJSONRejectsIncomingSwapWithoutSwapData(t *testing.T) {
	t.Parallel()

	_, err := inputFromJSON(partiallySignedInputJSON{
		PrevOut: previousOutputJSON{
			TxID:   strings.Repeat("aa", 32),
			Index:  0,
			Amount: 10_000,
		},
		Address: muunAddressJSON{
			Version:        201,
			DerivationPath: "m/schema:1'/recovery:1'/incoming:1/0",
			Address:        "bc1ptest",
		},
	})
	if err == nil {
		t.Fatal("expected incoming swap inputs without swap data to fail")
	}
}
