package api

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wapu/muun-wrapper/internal/wallet"
)

func TestSendErrorsLogCodeWithoutPrivateProviderBody(t *testing.T) {
	var captured bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	err := &wallet.SendError{Submitted: true, Cause: errors.New("provider failure: private-invoice private-token private-preimage private-destination")}
	response := httptest.NewRecorder()
	writeSendError(response, err)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"submit_failed"`) {
		t.Fatalf("existing ambiguous HTTP contract changed: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(captured.String(), "send_rejected code=submit_failed status=502") {
		t.Fatalf("missing safe send rejection log: %q", captured.String())
	}
	for _, secret := range []string{"private-invoice", "private-token", "private-preimage", "private-destination"} {
		if strings.Contains(captured.String(), secret) {
			t.Error("private provider body was logged")
		}
	}
}
