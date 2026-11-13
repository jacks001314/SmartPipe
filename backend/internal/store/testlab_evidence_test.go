package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidateDesktopEvidence(t *testing.T) {
	b := []byte("evidence")
	sum := sha256.Sum256(b)
	e := DesktopEvidence{SHA256: hex.EncodeToString(sum[:]), MIMEType: "text/plain", Payload: b}
	if err := ValidateDesktopEvidence(e); err != nil {
		t.Fatal(err)
	}
	e.Payload = []byte("tampered")
	if ValidateDesktopEvidence(e) == nil {
		t.Fatal("tampered bytes accepted")
	}
	e.Payload = b
	e.MIMEType = "text/html"
	if ValidateDesktopEvidence(e) == nil {
		t.Fatal("active content accepted")
	}
	e.MIMEType = "text/plain"
	e.Payload = make([]byte, MaxDesktopEvidenceBytes+1)
	if ValidateDesktopEvidence(e) == nil {
		t.Fatal("oversize accepted")
	}
}
