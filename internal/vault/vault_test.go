package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testPass  = "correct horse battery staple"
	testEmail = "alice@tuta.com"
	testPW    = "s3cret-LOGIN-pw-7f3a9"
)

// isolate points every path the package could touch at a temp dir before any
// of them is computed, so a test can never read or clobber a real vault.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
}

func saveDefault(t *testing.T) {
	t.Helper()
	if err := Save(testPass, Entry{Email: testEmail, Password: testPW}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func readFile(t *testing.T) file {
	t.Helper()
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFile(t *testing.T, f file) {
	t.Helper()
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func kdfOf(t *testing.T, f file) kdfParams {
	t.Helper()
	var p kdfParams
	if err := json.Unmarshal(f.KDF, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func withKDF(t *testing.T, f file, p kdfParams) file {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	f.KDF = raw
	return f
}

func TestPathIsNextToConfig(t *testing.T) {
	isolate(t)
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "tuta-alias", "vault.json")
	if got := Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	isolate(t)
	if Exists() {
		t.Fatal("Exists() before any save")
	}
	saveDefault(t)
	if !Exists() {
		t.Fatal("Exists() false after Save")
	}
	got, err := Open(testPass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != (Entry{Email: testEmail, Password: testPW}) {
		t.Errorf("Open = %+v", got)
	}
}

// Awkward values must survive the JSON payload and the AEAD unchanged.
func TestRoundTripValues(t *testing.T) {
	cases := []struct {
		name string
		pass string
		e    Entry
	}{
		{"empty password is allowed", testPass, Entry{Email: testEmail}},
		{"unicode and quotes", "pässwörd \"q\" ✓", Entry{Email: "a+b@tuta.io", Password: "pw \" \\ \n\t ö 日本"}},
		{"long password", "p", Entry{Email: testEmail, Password: strings.Repeat("x", 100_000)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			if err := Save(c.pass, c.e); err != nil {
				t.Fatal(err)
			}
			got, err := Open(c.pass)
			if err != nil || got != c.e {
				t.Errorf("Open = %+v, %v; want %+v", got, err, c.e)
			}
		})
	}
}

// A wrong passphrase must be a clean sentinel, never a panic or a partial
// read of the entry.
func TestWrongPassphrase(t *testing.T) {
	isolate(t)
	saveDefault(t)
	for _, pass := range []string{"wrong", "", testPass + " ", strings.ToUpper(testPass)} {
		got, err := Open(pass)
		if !errors.Is(err, ErrWrongPassphrase) {
			t.Errorf("Open(%q) err = %v, want ErrWrongPassphrase", pass, err)
		}
		if got != (Entry{}) {
			t.Errorf("Open(%q) leaked %+v", pass, got)
		}
	}
	// The message must not claim to know which of the two causes it was.
	msg := ErrWrongPassphrase.Error()
	if !strings.Contains(msg, "wrong passphrase") || !strings.Contains(msg, "tampered") {
		t.Errorf("ErrWrongPassphrase message %q should name both causes", msg)
	}
}

// Any single-field modification has to be caught by the AEAD: the ciphertext
// and nonce directly, the salt because it changes both the key and the
// authenticated header.
func TestTamperingIsDetected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *file)
	}{
		{"ciphertext", func(t *testing.T, f *file) { f.Ciphertext[len(f.Ciphertext)/2] ^= 0x01 }},
		{"ciphertext tag", func(t *testing.T, f *file) { f.Ciphertext[len(f.Ciphertext)-1] ^= 0x80 }},
		{"nonce", func(t *testing.T, f *file) { f.Nonce[0] ^= 0x01 }},
		{"salt", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Salt[0] ^= 0x01
			*f = withKDF(t, *f, p)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			saveDefault(t)
			f := readFile(t)
			c.mutate(t, &f)
			writeFile(t, f)
			got, err := Open(testPass)
			if !errors.Is(err, ErrWrongPassphrase) {
				t.Errorf("err = %v, want ErrWrongPassphrase", err)
			}
			if got != (Entry{}) {
				t.Errorf("tampered file yielded %+v", got)
			}
		})
	}
}

// Lowering the work factor is the attack the authenticated header exists to
// stop. The floor check refuses these outright; the next test shows the
// header authentication catching what the floor cannot.
func TestKDFDowngradeIsRefused(t *testing.T) {
	isolate(t)
	saveDefault(t)
	f := readFile(t)
	p := kdfOf(t, f)
	p.Memory, p.Time = 8, 1
	writeFile(t, withKDF(t, f, p))
	if _, err := Open(testPass); !errors.Is(err, ErrCorrupt) {
		t.Errorf("downgraded header: err = %v, want ErrCorrupt", err)
	}
}

// Rewriting the header without changing its meaning (same name, same
// numbers, same salt, so the same derived key) must still fail: only the
// header bytes differ, and only the additional-data binding can notice. This
// is the property the floor check alone would not give.
func TestKDFHeaderBytesAreAuthenticated(t *testing.T) {
	isolate(t)
	saveDefault(t)
	f := readFile(t)
	orig := string(f.KDF)
	if !strings.Contains(orig, `"argon2id"`) {
		t.Fatalf("unexpected header %s", orig)
	}
	// JSON escape for the "i": same string once decoded, different bytes.
	escaped := "\"argon2" + `\` + "u0069d\""
	f.KDF = json.RawMessage(strings.Replace(orig, `"argon2id"`, escaped, 1))
	if string(f.KDF) == orig {
		t.Fatal("test setup: header was not rewritten")
	}
	// Same parameters, same key:
	if kdfOf(t, f).Name != kdfName {
		t.Fatal("test setup: rewritten header should decode to the same name")
	}
	writeFile(t, f)
	if _, err := Open(testPass); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("equivalent but different header bytes: err = %v, want ErrWrongPassphrase", err)
	}
}

// A re-indented file (an editor, jq) keeps the same header content; it must
// keep opening rather than look like tampering.
func TestPrettyPrintedFileStillOpens(t *testing.T) {
	isolate(t)
	saveDefault(t)
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), pretty.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Open(testPass); err != nil || got.Email != testEmail {
		t.Errorf("Open(pretty) = %+v, %v", got, err)
	}
}

// Structural rejects: each field the format fixes must be refused before any
// key derivation is attempted.
func TestOpenRejectsBadFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *file)
	}{
		{"unknown version", func(t *testing.T, f *file) { f.Version = 2 }},
		{"version zero", func(t *testing.T, f *file) { f.Version = 0 }},
		{"other kdf name", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Name = "argon2i"
			*f = withKDF(t, *f, p)
		}},
		{"short salt", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Salt = p.Salt[:8]
			*f = withKDF(t, *f, p)
		}},
		{"empty salt", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Salt = nil
			*f = withKDF(t, *f, p)
		}},
		{"short key length", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.KeyLen = 16
			*f = withKDF(t, *f, p)
		}},
		{"time below floor", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Time = 3
			*f = withKDF(t, *f, p)
		}},
		{"memory below floor", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Memory = 64*1024 - 1
			*f = withKDF(t, *f, p)
		}},
		{"zero threads", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Threads = 0
			*f = withKDF(t, *f, p)
		}},
		{"memory far above ceiling", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Memory = 1 << 31 // would be 2 TiB
			*f = withKDF(t, *f, p)
		}},
		{"time far above ceiling", func(t *testing.T, f *file) {
			p := kdfOf(t, *f)
			p.Time = 1 << 30
			*f = withKDF(t, *f, p)
		}},
		{"unknown kdf field", func(t *testing.T, f *file) {
			f.KDF = json.RawMessage(strings.Replace(string(f.KDF), `"name"`, `"extra":1,"name"`, 1))
		}},
		{"kdf not an object", func(t *testing.T, f *file) { f.KDF = json.RawMessage(`"argon2id"`) }},
		{"missing kdf", func(t *testing.T, f *file) { f.KDF = nil }},
		{"short nonce", func(t *testing.T, f *file) { f.Nonce = f.Nonce[:12] }},
		{"empty nonce", func(t *testing.T, f *file) { f.Nonce = nil }},
		{"ciphertext shorter than the tag", func(t *testing.T, f *file) { f.Ciphertext = f.Ciphertext[:8] }},
	}
	// One real save, restored before every case: the rejects happen before
	// any key derivation, so the cases themselves are cheap.
	isolate(t)
	saveDefault(t)
	pristine, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(Path(), pristine, 0o600); err != nil {
				t.Fatal(err)
			}
			f := readFile(t)
			c.mutate(t, &f)
			writeFile(t, f) // a nil kdf marshals as null
			if _, err := Open(testPass); !errors.Is(err, ErrCorrupt) {
				t.Errorf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestOpenMalformedFile(t *testing.T) {
	cases := []struct{ name, content string }{
		{"empty file", ""},
		{"not json", "this is not json"},
		{"truncated json", `{"version":1,"kdf":{"name":"argon2id"`},
		{"json array", `[]`},
		{"wrong field type", `{"version":"one"}`},
		{"bad base64", `{"version":1,"kdf":{},"nonce":"!!!","ciphertext":"!!!"}`},
		{"empty object", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(), []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(testPass); !errors.Is(err, ErrCorrupt) {
				t.Errorf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestFileIsPrivateAndEncrypted(t *testing.T) {
	isolate(t)
	// A pre-existing, world-readable file must not leak its mode into the
	// replacement.
	if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveDefault(t)

	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %v, want 0600", got)
	}

	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("vault file is not valid JSON: %v", err)
	}
	for _, k := range []string{"version", "kdf", "nonce", "ciphertext"} {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	if len(generic) != 4 {
		t.Errorf("fields = %v, want exactly version, kdf, nonce, ciphertext", generic)
	}
	for _, secret := range []string{testPW, testEmail, testPass} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("plaintext %q appears in the vault file", secret)
		}
	}
	f := readFile(t)
	for _, secret := range []string{testPW, testEmail} {
		if bytes.Contains(f.Ciphertext, []byte(secret)) {
			t.Errorf("decoded ciphertext contains %q", secret)
		}
	}
	// And the payload has no TOTP slot: only the pair is ever stored.
	if bytes.Contains(raw, []byte("totp")) {
		t.Error("vault file mentions totp")
	}
}

func TestFileHeaderValues(t *testing.T) {
	isolate(t)
	saveDefault(t)
	f := readFile(t)
	if f.Version != 1 {
		t.Errorf("version = %d", f.Version)
	}
	p := kdfOf(t, f)
	want := kdfParams{Name: "argon2id", Time: 4, Memory: 64 * 1024, Threads: 1, KeyLen: 32, Salt: p.Salt}
	if p.Name != want.Name || p.Time != want.Time || p.Memory != want.Memory || p.Threads != want.Threads || p.KeyLen != want.KeyLen {
		t.Errorf("kdf = %+v", p)
	}
	if len(p.Salt) != 16 || len(f.Nonce) != 24 {
		t.Errorf("salt %d bytes, nonce %d bytes; want 16 and 24", len(p.Salt), len(f.Nonce))
	}
}

// Nonce reuse under one key breaks the cipher, and a fixed salt makes the
// same passphrase yield the same key. Same input twice must differ everywhere.
func TestEverySaveIsFresh(t *testing.T) {
	isolate(t)
	saveDefault(t)
	a := readFile(t)
	saveDefault(t)
	b := readFile(t)
	if bytes.Equal(kdfOf(t, a).Salt, kdfOf(t, b).Salt) {
		t.Error("salt repeated across saves")
	}
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Error("nonce repeated across saves")
	}
	if bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Error("ciphertext repeated across saves")
	}
	if got, err := Open(testPass); err != nil || got.Password != testPW {
		t.Errorf("latest save does not open: %+v, %v", got, err)
	}
}

func TestSaveReplacesNotAppends(t *testing.T) {
	isolate(t)
	if err := Save(testPass, Entry{Email: "first@tuta.com", Password: strings.Repeat("a", 5000)}); err != nil {
		t.Fatal(err)
	}
	if err := Save(testPass, Entry{Email: "second@tuta.com", Password: "short"}); err != nil {
		t.Fatal(err)
	}
	got, err := Open(testPass)
	if err != nil || got.Email != "second@tuta.com" || got.Password != "short" {
		t.Errorf("Open = %+v, %v", got, err)
	}
	if fi, _ := os.Stat(Path()); fi.Size() > 2000 {
		t.Errorf("file is %d bytes: old content left behind", fi.Size())
	}
	entries, _ := os.ReadDir(filepath.Dir(Path()))
	if len(entries) != 1 {
		t.Errorf("config dir has %d entries, want only the vault", len(entries))
	}
}

func TestNotFoundAndRemove(t *testing.T) {
	isolate(t)
	if _, err := Open(testPass); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open without file: err = %v, want ErrNotFound", err)
	}
	if err := Remove(); err != nil {
		t.Errorf("Remove without file = %v, want nil", err)
	}
	saveDefault(t)
	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if Exists() {
		t.Error("Exists() after Remove")
	}
	if _, err := Open(testPass); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open after Remove: err = %v, want ErrNotFound", err)
	}
	if err := Remove(); err != nil {
		t.Errorf("second Remove = %v, want nil", err)
	}
}

func TestSaveRefusesEmpty(t *testing.T) {
	cases := []struct {
		name string
		pass string
		e    Entry
	}{
		{"empty passphrase", "", Entry{Email: testEmail, Password: testPW}},
		{"empty email", testPass, Entry{Password: testPW}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isolate(t)
			if err := Save(c.pass, c.e); err == nil {
				t.Error("Save accepted it")
			}
			if Exists() {
				t.Error("a refused Save still created the file")
			}
		})
	}
}

// A refused Save must not destroy the vault that is already there.
func TestRefusedSaveKeepsExistingVault(t *testing.T) {
	isolate(t)
	saveDefault(t)
	if err := Save("", Entry{Email: "x@tuta.com"}); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if got, err := Open(testPass); err != nil || got.Email != testEmail {
		t.Errorf("existing vault damaged: %+v, %v", got, err)
	}
}

func TestChangePassphrase(t *testing.T) {
	isolate(t)
	saveDefault(t)
	before := readFile(t)
	if err := ChangePassphrase(testPass, "new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(testPass); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("old passphrase after change: err = %v, want ErrWrongPassphrase", err)
	}
	got, err := Open("new passphrase")
	if err != nil || got != (Entry{Email: testEmail, Password: testPW}) {
		t.Errorf("new passphrase: %+v, %v", got, err)
	}
	after := readFile(t)
	if bytes.Equal(kdfOf(t, before).Salt, kdfOf(t, after).Salt) {
		t.Error("salt reused across a passphrase change")
	}
}

func TestChangePassphraseFailures(t *testing.T) {
	isolate(t)
	if err := ChangePassphrase(testPass, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no vault: err = %v, want ErrNotFound", err)
	}
	saveDefault(t)
	if err := ChangePassphrase("wrong", "x"); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong old passphrase: err = %v, want ErrWrongPassphrase", err)
	}
	if err := ChangePassphrase(testPass, ""); err == nil {
		t.Error("empty new passphrase accepted")
	}
	// None of the failures may have changed what the original passphrase opens.
	if got, err := Open(testPass); err != nil || got.Password != testPW {
		t.Errorf("vault changed by failed ChangePassphrase: %+v, %v", got, err)
	}
}

// A planted symlink at the vault path is replaced, not followed (the guarantee
// comes from internal/atomicfile; this pins that Save really uses it).
func TestSaveReplacesSymlink(t *testing.T) {
	isolate(t)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, Path()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	saveDefault(t)
	if b, _ := os.ReadFile(victim); string(b) != "untouched" {
		t.Errorf("symlink target was overwritten: %q", b)
	}
	if got, err := Open(testPass); err != nil || got.Email != testEmail {
		t.Errorf("Open = %+v, %v", got, err)
	}
}
