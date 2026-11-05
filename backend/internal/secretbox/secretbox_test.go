package secretbox

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(testKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plain := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	sealed, err := box.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, Version+":") {
		t.Fatalf("ciphertext missing version prefix: %q", sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != plain {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	box, _ := New(testKey(t))
	a, _ := box.Seal("same-secret-value")
	b, _ := box.Seal("same-secret-value")
	if a == b {
		t.Fatal("two seals produced identical ciphertext; nonce reuse suspected")
	}
}

func TestOpenWrongKey(t *testing.T) {
	box, _ := New(testKey(t))
	sealed, _ := box.Seal("value-to-protect")

	other := make([]byte, KeySize)
	for i := range other {
		other[i] = byte(99 - i)
	}
	box2, _ := New(other)
	if _, err := box2.Open(sealed); err == nil {
		t.Fatal("expected error with wrong key")
	}
}

func TestOpenTampered(t *testing.T) {
	box, _ := New(testKey(t))
	sealed, _ := box.Seal("value-to-protect")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, Version+":"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := Version + ":" + base64.StdEncoding.EncodeToString(raw)
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("expected error on tampered ciphertext")
	}
}

func TestOpenMalformed(t *testing.T) {
	box, _ := New(testKey(t))
	for _, in := range []string{"", "plain-text", "v9:AAAA", "v1:not-base64!!"} {
		if _, err := box.Open(in); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("short")); err == nil {
		t.Fatal("expected ErrInvalidKey")
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"short":                        "****",
		"ghp_abcdefghijklmnopqrstuvwx": "****uvwx",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Fatalf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGenerateKeyRoundTrips(t *testing.T) {
	encoded, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("empty key")
	}
	if _, err := NewFromBase64(encoded); err != nil {
		t.Fatalf("NewFromBase64: %v", err)
	}
}
