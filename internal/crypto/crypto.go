// Package crypto ports tuta_alias/crypto.py + the parseSessionID half of
// tuta_alias/login.py.
package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"

	"github.com/tdwgm/tuta-alias/internal/consts"
)

// base64ExtAlphabet is Tuta's sortable base64 variant. Used for the
// 9-byte list-id prefix of accessToken. See
// tuta-sdk/rust/sdk/src/login/login_facade.rs::parse_session_id.
const base64ExtAlphabet = "-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
const stdBase64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var stdToExt [256]byte

func init() {
	for i := range stdToExt {
		stdToExt[i] = byte(i)
	}
	for i := 0; i < len(stdBase64Alphabet); i++ {
		stdToExt[stdBase64Alphabet[i]] = base64ExtAlphabet[i]
	}
}

// DeriveUserPassphraseKey runs the Tuta KDF for the given passphrase, salt,
// and kdfVersion (1=Argon2id; 0=bcrypt unsupported).
func DeriveUserPassphraseKey(passphrase string, salt []byte, kdfVersion int) ([]byte, error) {
	switch kdfVersion {
	case consts.KDFArgon2ID:
		return argon2.IDKey(
			[]byte(passphrase),
			salt,
			consts.Argon2IDIterations,
			consts.Argon2IDMemoryKiB,
			consts.Argon2IDParallelism,
			consts.Argon2IDKeyLength,
		), nil
	case consts.KDFBcrypt:
		return nil, errors.New(
			"bcrypt KDF (kdf_version=0) not implemented — log in via the " +
				"web client once to migrate the account to Argon2id, then retry",
		)
	default:
		return nil, fmt.Errorf("unknown kdfVersion=%d", kdfVersion)
	}
}

// AuthVerifierB64URL computes SHA-256(passphraseKey) and base64url-encodes
// without padding. Matches Python auth_verifier_b64url.
func AuthVerifierB64URL(passphraseKey []byte) string {
	sum := sha256.Sum256(passphraseKey)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ParseSessionID decodes accessToken and returns (listID, elementID).
// The list-id is the BASE64_EXT-encoded first 9 raw bytes; the element-id
// is the b64url-no-pad SHA-256 of the remainder. Mirrors
// tuta_alias/login.py::parse_session_id.
func ParseSessionID(accessToken string) (string, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(accessToken)
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(accessToken); err != nil {
			return "", "", fmt.Errorf("invalid accessToken: %w", err)
		}
	}
	const generatedIDBytesLength = 9
	if len(raw) < generatedIDBytesLength {
		return "", "", fmt.Errorf("accessToken too short (%dB)", len(raw))
	}
	listID := base64ExtEncode(raw[:generatedIDBytesLength])
	sum := sha256.Sum256(raw[generatedIDBytesLength:])
	elementID := base64.RawURLEncoding.EncodeToString(sum[:])
	return listID, elementID, nil
}

func base64ExtEncode(b []byte) string {
	std := base64.StdEncoding.EncodeToString(b)
	out := make([]byte, len(std))
	for i := 0; i < len(std); i++ {
		out[i] = stdToExt[std[i]]
	}
	return string(out)
}
