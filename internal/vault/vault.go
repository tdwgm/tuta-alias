// Package vault stores one login (email + password) in one passphrase-encrypted
// file.
//
// It exists for platforms with no Secret Service (Termux/Android), where the
// keyring credential backend cannot work and the alternative is typing the
// password on every login. It has no CLI, no prompting and no global state:
// callers pass the passphrase in.
//
// The TOTP secret is deliberately not part of the payload and there is no
// option to add one. The TOTP code stays a separate factor; storing its secret
// next to the password would collapse two factors into one file.
//
// File format (JSON, version 1):
//
//	{
//	  "version": 1,
//	  "kdf": {"name":"argon2id","time":4,"memory":65536,"threads":1,"keyLen":32,"salt":"<base64>"},
//	  "nonce": "<base64>",
//	  "ciphertext": "<base64>"
//	}
//
// The key is argon2id(passphrase, salt); the payload is sealed with
// XChaCha20-Poly1305. The exact bytes of the "kdf" object are the AEAD's
// additional data, so changing any KDF parameter or the salt makes the open
// fail instead of silently weakening the key derivation. Every save uses a new
// salt and a new nonce and re-encrypts the whole file.
package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
	"github.com/tdwgm/tuta-alias/internal/config"
)

// Sentinels, distinguishable with errors.Is.
var (
	// ErrNotFound means there is no vault file.
	ErrNotFound = errors.New("no vault file")
	// ErrWrongPassphrase means the authenticated decryption failed. A wrong
	// passphrase and a modified file are indistinguishable by design, so the
	// message names both and never claims to know which.
	ErrWrongPassphrase = errors.New("could not decrypt the vault: wrong passphrase or the file has been tampered with")
	// ErrCorrupt means the file is not a vault this version can read: bad
	// JSON, unknown version, or a field of the wrong size or strength.
	ErrCorrupt = errors.New("vault file is corrupt")
)

const (
	fileVersion = 1
	kdfName     = "argon2id"

	// The work factor written by this version. It is also the floor accepted
	// on open, so a doctored header cannot ask for a cheaper derivation.
	kdfTime    uint32 = 4
	kdfMemory  uint32 = 64 * 1024 // KiB
	kdfThreads uint8  = 1
	kdfKeyLen  uint32 = 32
	saltLen           = 16

	// Ceilings on open. The header is read before it can be authenticated, so
	// without them a hostile file could ask for terabytes of memory.
	maxTime    uint32 = 32
	maxMemory  uint32 = 1024 * 1024 // 1 GiB
	maxThreads uint8  = 16
)

// Entry is the credential pair held in the vault. There is no TOTP field.
type Entry struct {
	Email    string
	Password string
}

type kdfParams struct {
	Name    string `json:"name"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"`
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"keyLen"`
	Salt    []byte `json:"salt"`
}

// file is the on-disk document. KDF is kept as raw bytes so the very bytes
// that were authenticated on save are the ones written and re-read.
type file struct {
	Version    int             `json:"version"`
	KDF        json.RawMessage `json:"kdf"`
	Nonce      []byte          `json:"nonce"`
	Ciphertext []byte          `json:"ciphertext"`
}

type payload struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Path returns where the vault lives: vault.json next to the config file. It
// is "" when the home directory cannot be determined.
func Path() string {
	p := config.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "vault.json")
}

// Exists reports whether a vault file is present. It does not read it.
func Exists() bool {
	p := Path()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// Save encrypts e under passphrase and writes it, creating or replacing the
// vault. A fresh salt and nonce are drawn on every call.
func Save(passphrase string, e Entry) error {
	if passphrase == "" {
		return errors.New("vault: passphrase must not be empty")
	}
	if e.Email == "" {
		return errors.New("vault: email must not be empty")
	}
	path := Path()
	if path == "" {
		return errors.New("vault: cannot locate the config directory (no home directory)")
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("vault: random salt: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("vault: random nonce: %w", err)
	}

	// Marshal the header once; these bytes go to the file and to the AEAD.
	kdfRaw, err := json.Marshal(kdfParams{
		Name: kdfName, Time: kdfTime, Memory: kdfMemory, Threads: kdfThreads,
		KeyLen: kdfKeyLen, Salt: salt,
	})
	if err != nil {
		return fmt.Errorf("vault: encode kdf header: %w", err)
	}

	plain, err := json.Marshal(payload{Email: e.Email, Password: e.Password})
	if err != nil {
		return fmt.Errorf("vault: encode payload: %w", err)
	}
	defer wipe(plain)

	key := argon2.IDKey([]byte(passphrase), salt, kdfTime, kdfMemory, kdfThreads, kdfKeyLen)
	defer wipe(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("vault: cipher: %w", err)
	}
	ct := aead.Seal(nil, nonce, plain, kdfRaw)

	// json.Marshal, not MarshalIndent: indenting would rewrite the whitespace
	// inside the raw kdf object.
	data, err := json.Marshal(file{Version: fileVersion, KDF: kdfRaw, Nonce: nonce, Ciphertext: ct})
	if err != nil {
		return fmt.Errorf("vault: encode file: %w", err)
	}
	data = append(data, '\n')
	if err := atomicfile.WriteMkdir(path, data, 0o600, 0o700); err != nil {
		return fmt.Errorf("vault: write %s: %w", path, err)
	}
	return nil
}

// Open decrypts the vault with passphrase. It returns ErrNotFound when there
// is no file, ErrCorrupt when the file is not a readable vault and
// ErrWrongPassphrase when authentication fails.
func Open(passphrase string) (Entry, error) {
	path := Path()
	if path == "" {
		return Entry{}, errors.New("vault: cannot locate the config directory (no home directory)")
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("vault: read %s: %w", path, err)
	}

	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: %w", ErrCorrupt, path, err)
	}
	if f.Version != fileVersion {
		return Entry{}, fmt.Errorf("%w: %s: unsupported version %d", ErrCorrupt, path, f.Version)
	}
	// Compact so a file re-indented by an editor still yields the bytes that
	// were authenticated (Save writes the header compact).
	var kdfRaw bytes.Buffer
	if err := json.Compact(&kdfRaw, f.KDF); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: kdf header: %w", ErrCorrupt, path, err)
	}
	var p kdfParams
	dec := json.NewDecoder(bytes.NewReader(kdfRaw.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: kdf header: %w", ErrCorrupt, path, err)
	}
	if err := p.check(); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: %w", ErrCorrupt, path, err)
	}
	if len(f.Nonce) != chacha20poly1305.NonceSizeX {
		return Entry{}, fmt.Errorf("%w: %s: nonce is %d bytes, want %d", ErrCorrupt, path, len(f.Nonce), chacha20poly1305.NonceSizeX)
	}
	if len(f.Ciphertext) < chacha20poly1305.Overhead {
		return Entry{}, fmt.Errorf("%w: %s: ciphertext too short", ErrCorrupt, path)
	}

	key := argon2.IDKey([]byte(passphrase), p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	defer wipe(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Entry{}, fmt.Errorf("vault: cipher: %w", err)
	}
	plain, err := aead.Open(nil, f.Nonce, f.Ciphertext, kdfRaw.Bytes())
	if err != nil {
		return Entry{}, ErrWrongPassphrase
	}
	defer wipe(plain)

	var pl payload
	if err := json.Unmarshal(plain, &pl); err != nil {
		return Entry{}, fmt.Errorf("%w: %s: payload: %w", ErrCorrupt, path, err)
	}
	if pl.Email == "" {
		return Entry{}, fmt.Errorf("%w: %s: payload has no email", ErrCorrupt, path)
	}
	return Entry{Email: pl.Email, Password: pl.Password}, nil
}

// check enforces what this version writes: the algorithm, the exact salt and
// key sizes, and a work factor no lower than the one Save uses.
func (p kdfParams) check() error {
	switch {
	case p.Name != kdfName:
		return fmt.Errorf("unsupported kdf %q", p.Name)
	case len(p.Salt) != saltLen:
		return fmt.Errorf("salt is %d bytes, want %d", len(p.Salt), saltLen)
	case p.KeyLen != kdfKeyLen:
		return fmt.Errorf("key length is %d, want %d", p.KeyLen, kdfKeyLen)
	case p.Time < kdfTime:
		return fmt.Errorf("kdf time %d is below the minimum %d", p.Time, kdfTime)
	case p.Memory < kdfMemory:
		return fmt.Errorf("kdf memory %d KiB is below the minimum %d", p.Memory, kdfMemory)
	case p.Threads < kdfThreads:
		return fmt.Errorf("kdf threads %d is below the minimum %d", p.Threads, kdfThreads)
	case p.Time > maxTime || p.Memory > maxMemory || p.Threads > maxThreads:
		return errors.New("kdf parameters exceed the supported maximum")
	}
	return nil
}

// Remove deletes the vault file. It returns nil when there is none.
func Remove() error {
	path := Path()
	if path == "" {
		return errors.New("vault: cannot locate the config directory (no home directory)")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("vault: remove %s: %w", path, err)
	}
	return nil
}

// ChangePassphrase re-encrypts the vault under newPass. The old passphrase
// must open it first; nothing is written when it does not.
func ChangePassphrase(oldPass, newPass string) error {
	if newPass == "" {
		return errors.New("vault: passphrase must not be empty")
	}
	e, err := Open(oldPass)
	if err != nil {
		return err
	}
	return Save(newPass, e)
}

// wipe zeroes b. Best effort: Go may have copied the data elsewhere.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
