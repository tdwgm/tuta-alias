package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// All "want" values were captured live from the Python reference:
//   tests/test_crypto.py
//   tuta_alias/crypto.py (Argon2id via argon2-cffi hash_secret_raw)
//   tuta_alias/login.py::parse_session_id

func TestDeriveUserPassphraseKey_Argon2ID(t *testing.T) {
	passphrase := "passphrase"
	salt, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	got, err := DeriveUserPassphraseKey(passphrase, salt, 1)
	if err != nil {
		t.Fatalf("derive err: %v", err)
	}
	want, _ := hex.DecodeString("a519b3d5603b512d5fabd622a0efd911e3909f2bd070eb9e1a424a7591d4bd5f")
	if !bytes.Equal(got, want) {
		t.Fatalf("argon2id mismatch\n got=%x\nwant=%x", got, want)
	}
}

func TestDeriveUserPassphraseKey_Bcrypt_Unsupported(t *testing.T) {
	_, err := DeriveUserPassphraseKey("x", []byte("salt"), 0)
	if err == nil {
		t.Fatalf("expected NotImplementedError equivalent for bcrypt")
	}
}

func TestAuthVerifierB64URL_ZeroKey(t *testing.T) {
	key := make([]byte, 32)
	got := AuthVerifierB64URL(key)
	want := "Zmh6rfhivXdsj8GLjp-OIAiXFIVu4jOzkCpZHQ1fKSU"
	if got != want {
		t.Fatalf("auth verifier mismatch\n got=%q\nwant=%q", got, want)
	}
}

func TestParseSessionID_KnownVector(t *testing.T) {
	raw := []byte("ABCDEFGHIJKLMNOP") // 16 bytes
	token := base64.RawURLEncoding.EncodeToString(raw)
	listID, elemID, err := ParseSessionID(token)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if listID != "FJ82G3K5GoW8" {
		t.Errorf("listID = %q, want FJ82G3K5GoW8", listID)
	}
	if elemID != "0vWpdfjVR8IvfEqO6AQBIJ6VS3C8K5G76FCf_luZhtI" {
		t.Errorf("elemID = %q", elemID)
	}
}

func TestParseSessionID_TooShort(t *testing.T) {
	raw := []byte("ABC")
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, _, err := ParseSessionID(token); err == nil {
		t.Fatalf("expected error for short token")
	}
}

func TestBase64ExtAlphabet_Constant(t *testing.T) {
	want := "-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
	if base64ExtAlphabet != want {
		t.Fatalf("base64ExtAlphabet drift\n got=%q\nwant=%q", base64ExtAlphabet, want)
	}
	if len(base64ExtAlphabet) != 64 {
		t.Fatalf("len = %d, want 64", len(base64ExtAlphabet))
	}
}
