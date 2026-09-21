package secretbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func key() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func TestRoundTripAndSeparation(t *testing.T) {
	k := key()
	box, err := New(k)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("glpat-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "glpat") || strings.Contains(sealed, "secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	opened, err := box.Open(sealed)
	if err != nil || opened != "glpat-secret-value" {
		t.Fatalf("round trip: %q %v", opened, err)
	}
	// A different key must not decrypt the value.
	other, _ := New(key())
	if _, err = other.Open(sealed); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	box, _ := New(key())
	first, _ := box.Seal("same")
	second, _ := box.Seal("same")
	if first == second {
		t.Fatal("nonce reuse")
	}
}

func TestKeyParsing(t *testing.T) {
	raw := key()
	if b, err := ParseKey(raw); err != nil || len(b) != 32 {
		t.Fatal("hex key rejected", err)
	}
	b, _ := hex.DecodeString(raw)
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(b)); err != nil {
		t.Fatal("base64 key rejected", err)
	}
	for _, bad := range []string{"", "short", strings.Repeat("z", 64), base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("accepted bad key %q", bad)
		}
	}
}

func TestOpenRejectsCorruption(t *testing.T) {
	box, _ := New(key())
	sealed, _ := box.Seal("value")
	for _, bad := range []string{"", "not-base64!!", sealed[:len(sealed)-1]} {
		if _, err := box.Open(bad); err == nil {
			t.Fatalf("accepted corrupted payload %q", bad)
		}
	}
}

func TestFingerprintStableAndOpaque(t *testing.T) {
	a := Fingerprint("token-value")
	if a != Fingerprint("token-value") || len(a) != 12 || strings.Contains(a, "token") {
		t.Fatal(a)
	}
	if a == Fingerprint("other") {
		t.Fatal("fingerprint collision")
	}
}
