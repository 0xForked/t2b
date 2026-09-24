package pumpportal

import (
	"net/http"
	"strings"
	"testing"
)

// TestErrorsArrayParsed guards the exact bug found in production: PumpPortal
// returns {"errors": [...]} (plural array) on rejection, not {"error": "..."}
// (singular string) — parsing only the singular shape silently dumps the
// raw JSON body into the error message instead of the real reason.
func TestErrorsArrayParsed(t *testing.T) {
	_, err := parseTradeResponse(http.StatusForbidden, []byte(`{"errors":["Must supply a valid API key"]}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Must supply a valid API key") {
		t.Fatalf("expected the real PumpPortal message surfaced, got: %v", err)
	}
}

func TestSingularErrorStillParsed(t *testing.T) {
	_, err := parseTradeResponse(http.StatusBadRequest, []byte(`{"error":"insufficient balance"}`))
	if err == nil || !strings.Contains(err.Error(), "insufficient balance") {
		t.Fatalf("expected singular error message surfaced, got: %v", err)
	}
}

func TestSuccessReturnsSignature(t *testing.T) {
	sig, err := parseTradeResponse(http.StatusOK, []byte(`{"signature":"abc123"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig != "abc123" {
		t.Fatalf("expected signature abc123, got %q", sig)
	}
}
