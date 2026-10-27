package config

import (
	"fmt"
	"strings"
	"testing"
)

// mustParse fails the test on a parse error; Parse never returns one today but
// the signature allows it.
func mustParse(t *testing.T, s string) *Doc {
	t.Helper()
	d, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return d
}

// kinds renders the entry kinds of a doc, one letter per line, so a test can
// say "this line was kept as raw text, not parsed as a key" in one comparison.
func kinds(d *Doc) string {
	var b strings.Builder
	for _, e := range d.entries {
		b.WriteByte("BCKR"[e.kind])
	}
	return b.String()
}

// kvList is the ordered key=value view of a doc, used to compare two docs by
// meaning rather than by bytes.
func kvList(d *Doc) []string {
	var out []string
	for _, e := range d.entries {
		if e.kind == kindKV {
			out = append(out, fmt.Sprintf("%s=%q", e.key, e.value))
		}
	}
	return out
}

// The file is hand-edited and saved back by the TUI. For input that is already
// in the canonical form, Parse -> Bytes must be the identity, or every save
// rewrites the user's file and every diff in their dotfiles repo is noise.
func TestParseBytesIsIdentityForCanonicalInput(t *testing.T) {
	for name, in := range map[string]string{
		"keys only":          "A=\"1\"\nB=\"2\"\n",
		"comments and blank": "# header\n\n# about A\nA=\"1\"\n\n\n# about B\nB=\"two words\"\n",
		"unknown key":        "NOT_A_REAL_KEY=\"x\"\nDEFAULT_DOMAIN=\"example.com\"\n",
		"indented comment":   "  # indented comment\nA=\"1\"\n",
		"raw lines":          "just a bare word\nKEY_WITHOUT_EQUALS\n=value\nA=\"1\"\n",
		"blank only":         "\n",
		"two blanks":         "\n\n",
		"comments only":      "# one\n# two\n",
		"empty":              "",
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(mustParse(t, in).Bytes()); got != in {
				t.Errorf("Parse->Bytes changed the file\n in: %q\nout: %q", in, got)
			}
		})
	}
}

// Set on an existing key must touch exactly one line. The comment above it, the
// blank lines, the unknown keys and the order are the user's, and the old Save
// implementation that rebuilt the file from a template threw them away.
func TestSetExistingKeyChangesOnlyThatLine(t *testing.T) {
	in := "# top\n\n# the domain\nDEFAULT_DOMAIN=\"old.example\"\nCUSTOM=\"keep\"\n\n# trailing\n"
	d := mustParse(t, in)
	d.Set("DEFAULT_DOMAIN", "new.example")

	want := strings.Replace(in, `DEFAULT_DOMAIN="old.example"`, `DEFAULT_DOMAIN="new.example"`, 1)
	if got := string(d.Bytes()); got != want {
		t.Errorf("Set changed more than one line\n got: %q\nwant: %q", got, want)
	}
	if v, ok := d.Get("DEFAULT_DOMAIN"); !ok || v != "new.example" {
		t.Errorf("Get after Set = %q, %v", v, ok)
	}
}

// A hand-written line has its own spacing and unquoted value. Set normalises
// that one line to KEY="value" and nothing else; this pins that the
// normalisation stays on the line that was set.
func TestSetNormalisesOnlyTheSetLine(t *testing.T) {
	d := mustParse(t, "A = 1\nB =  2\n")
	d.Set("A", "x")
	if got, want := string(d.Bytes()), "A=\"x\"\nB =  2\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A new key goes at the end, after everything the user wrote, so a save never
// reshuffles an existing file.
func TestSetNewKeyAppends(t *testing.T) {
	d := mustParse(t, "# c\nA=\"1\"\n")
	d.Set("B", "2")
	if got, want := string(d.Bytes()), "# c\nA=\"1\"\nB=\"2\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// And into an empty doc, which is how Save starts from the template.
	e := NewDoc()
	e.Set("A", "1")
	if got, want := string(e.Bytes()), "A=\"1\"\n"; got != want {
		t.Errorf("empty doc: got %q, want %q", got, want)
	}
}

// Callers distinguish "set to empty" from "not set" (Load only overrides a
// default when the key is present), so Get's second result has to be right,
// including for an empty value.
func TestGetPresentAndAbsent(t *testing.T) {
	d := mustParse(t, "A=\"1\"\nEMPTY=\"\"\n# COMMENTED=\"x\"\nbare\n")
	for _, c := range []struct {
		key  string
		want string
		ok   bool
	}{
		{"A", "1", true},
		{"EMPTY", "", true},
		{"MISSING", "", false},
		{"COMMENTED", "", false}, // a commented-out key is not set
		{"bare", "", false},      // a raw line is not a key
		{"", "", false},
	} {
		got, ok := d.Get(c.key)
		if got != c.want || ok != c.ok {
			t.Errorf("Get(%q) = %q, %v; want %q, %v", c.key, got, ok, c.want, c.ok)
		}
	}
}

// Delete exists so renamed keys do not linger. It must remove that key's line
// and nothing else (not the comment above it, which may belong to the next
// key), and removing a key that is not there must change nothing.
func TestDelete(t *testing.T) {
	in := "# head\nA=\"1\"\n# about B\nB=\"2\"\n\nC=\"3\"\n"
	d := mustParse(t, in)

	d.Delete("B")
	if got, want := string(d.Bytes()), "# head\nA=\"1\"\n# about B\n\nC=\"3\"\n"; got != want {
		t.Errorf("after Delete(B):\n got: %q\nwant: %q", got, want)
	}
	if _, ok := d.Get("B"); ok {
		t.Error("B still present after Delete")
	}

	before := string(d.Bytes())
	d.Delete("NOT_THERE")
	if got := string(d.Bytes()); got != before {
		t.Errorf("Delete of an absent key changed the doc: %q", got)
	}
	d.Delete("head") // text of a comment is not a key
	if got := string(d.Bytes()); got != before {
		t.Errorf("Delete matched a comment: %q", got)
	}
	NewDoc().Delete("A") // must not panic on an empty doc
}

// Only KEY="value" lines are keys. Everything else is kept byte for byte and
// never reported by Get: if a bare word or a half-typed line were parsed as a
// key, Save would rewrite or drop something the user typed. validKey allows
// letters and underscore, plus digits after the first character.
func TestNonKeyLinesAreKeptRaw(t *testing.T) {
	for _, c := range []struct {
		line string
		raw  bool
	}{
		{"bare", true},
		{"two words", true},
		{"KEY", true},                  // no "="
		{"=value", true},               // no key
		{`="value"`, true},             // no key
		{"MY-KEY=1", true},             // dash
		{"1KEY=1", true},               // leading digit
		{"KEY NAME=1", true},           // space inside the key
		{"export KEY=1", true},         // shell habit; the parser does not know "export"
		{"KEY.NAME=1", true},           // dot
		{"ÄKEY=1", true},               // non-ASCII letter
		{"K1=1", false},                // digit after the first char is fine
		{"_K=1", false},                // leading underscore is fine
		{"lower=1", false},             // lower case is fine
		{"  # not a key=\"x\"", false}, // comment, kept as comment (see below)
		{"A=1", false},
	} {
		d := mustParse(t, c.line+"\n")
		if len(d.entries) != 1 {
			t.Errorf("%q: %d entries, want 1", c.line, len(d.entries))
			continue
		}
		e := d.entries[0]
		if c.raw && e.kind != kindRaw {
			t.Errorf("%q parsed as kind %d (key %q), want raw", c.line, e.kind, e.key)
		}
		if !c.raw && e.kind == kindRaw {
			t.Errorf("%q kept raw, want it parsed", c.line)
		}
		if got := string(d.Bytes()); got != c.line+"\n" {
			t.Errorf("%q not preserved: %q", c.line, got)
		}
	}
}

// A raw line next to real keys stays put when a neighbour is edited.
func TestRawLinesSurviveEdits(t *testing.T) {
	d := mustParse(t, "oops\nA=\"1\"\nMY-KEY=\"x\"\n")
	d.Set("A", "2")
	d.Set("B", "3")
	d.Delete("A")
	if got, want := string(d.Bytes()), "oops\nMY-KEY=\"x\"\nB=\"3\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, ok := d.Get("MY-KEY"); ok {
		t.Error("Get found a key with a dash")
	}
}

// SURPRISE, pinned as current behaviour: an indented assignment IS a key. The
// parser trims the text before the "=" as well as after it, so "  A=1" reads
// as A. The line is kept verbatim until Set rewrites it, and Set drops the
// indentation. Shell allows indented assignments, so this is defensible, but
// it is not what "only column-0 KEY=" would suggest.
func TestIndentedKeyIsParsedAsKey(t *testing.T) {
	d := mustParse(t, "  A=\"1\"\n\tB=2\n")
	if kinds(d) != "KK" {
		t.Fatalf("kinds = %s, want KK", kinds(d))
	}
	if v, ok := d.Get("A"); !ok || v != "1" {
		t.Errorf("Get(A) = %q, %v", v, ok)
	}
	if v, ok := d.Get("B"); !ok || v != "2" {
		t.Errorf("Get(B) = %q, %v", v, ok)
	}
	if got := string(d.Bytes()); got != "  A=\"1\"\n\tB=2\n" {
		t.Errorf("untouched indented lines changed: %q", got)
	}
	d.Set("A", "9")
	if got := string(d.Bytes()); got != "A=\"9\"\n\tB=2\n" {
		t.Errorf("Set on an indented key: %q", got)
	}
}

// How values are read. In particular "#" inside quotes is data, and there is no
// inline-comment syntax: a trailing comment after a value is part of the value.
func TestValueForms(t *testing.T) {
	for _, c := range []struct {
		name, line, want string
	}{
		{"unquoted", `A=hello`, "hello"},
		{"unquoted with spaces", `A=hello world`, "hello world"},
		{"quoted", `A="hello"`, "hello"},
		{"quoted with spaces", `A="hello  world"`, "hello  world"},
		{"quoted hash is data", `A="a # b"`, "a # b"},
		{"quoted leading hash", `A="#fff"`, "#fff"},
		{"unquoted hash is data", `A=a#b`, "a#b"},
		{"empty unquoted", `A=`, ""},
		{"empty quoted", `A=""`, ""},
		{"spaces around equals", `A  =  "v"  `, "v"},
		{"spaces around equals unquoted", "A\t=\tv\t", "v"},
		{"quoted keeps inner spaces", `A=" x "`, " x "},
		{"escaped quote", `A="a\"b"`, `a"b`},
		{"escaped backslash", `A="a\\b"`, `a\b`},
		{"single quotes are literal", `A='x'`, `'x'`},
		{"lone quote", `A="`, `"`},
		{"only opening quote", `A="abc`, `"abc`},
		{"equals in value", `A="x=y"`, "x=y"},
		{"unquoted equals in value", `A=x=y`, "x=y"},
		// No inline comments: the value is everything after "=", so the
		// closing quote is no longer last and the quotes stay in the value.
		{"trailing comment after quoted value", `A="v" # note`, `"v" # note`},
		{"trailing comment after unquoted value", `A=v # note`, "v # note"},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := mustParse(t, c.line+"\n")
			got, ok := d.Get("A")
			if !ok || got != c.want {
				t.Errorf("%s -> %q, %v; want %q", c.line, got, ok, c.want)
			}
		})
	}
}

// Files without a final newline, empty files and comment-only files are all
// real: editors differ, and Save starts from an empty file on first run. None
// may lose a line, and the only change allowed is the final newline that Bytes
// always writes.
func TestEdgeShapesKeepEveryLine(t *testing.T) {
	for name, c := range map[string]struct {
		in    string
		lines int
		out   string
	}{
		"no final newline":          {"A=\"1\"\n# last", 2, "A=\"1\"\n# last\n"},
		"key without final newl.":   {"A=\"1\"", 1, "A=\"1\"\n"},
		"empty":                     {"", 0, ""},
		"only newline":              {"\n", 1, "\n"},
		"only comments":             {"# a\n# b\n# c\n", 3, "# a\n# b\n# c\n"},
		"only comments, no newl.":   {"# a\n# b", 2, "# a\n# b\n"},
		"comment with key-ish text": {"# A=\"1\"\n", 1, "# A=\"1\"\n"},
		"blank lines at the end":    {"A=\"1\"\n\n\n", 3, "A=\"1\"\n\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			d := mustParse(t, c.in)
			if len(d.entries) != c.lines {
				t.Errorf("%d entries, want %d", len(d.entries), c.lines)
			}
			if got := string(d.Bytes()); got != c.out {
				t.Errorf("Bytes = %q, want %q", got, c.out)
			}
			// What Bytes wrote must be stable from here on.
			if again := string(mustParse(t, string(d.Bytes())).Bytes()); again != c.out {
				t.Errorf("second pass = %q, want %q", again, c.out)
			}
		})
	}
	if _, ok := mustParse(t, "# A=\"1\"\n").Get("A"); ok {
		t.Error("a commented-out key was read as set")
	}
}

// A whitespace-only line is a blank line, and Bytes writes it back empty. This
// is the one place Parse -> Bytes is not byte-identical for otherwise canonical
// input; it is harmless but worth pinning so it is a decision, not an accident.
func TestWhitespaceOnlyLineBecomesEmpty(t *testing.T) {
	d := mustParse(t, "A=\"1\"\n   \n\t\nB=\"2\"\n")
	if kinds(d) != "KBBK" {
		t.Errorf("kinds = %s, want KBBK", kinds(d))
	}
	if got, want := string(d.Bytes()), "A=\"1\"\n\n\nB=\"2\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// CRLF, current behaviour (asserted, not endorsed). Parse splits on "\n" only,
// so a "\r" stays at the end of every line:
//   - key lines still read correctly, because the value is TrimSpace'd (the
//     "\r" is not part of the value), and an untouched key line keeps its "\r";
//   - comment and raw lines keep their "\r";
//   - blank lines lose theirs: "\r" alone trims to empty and is stored as "".
//
// So an untouched CRLF file comes back with LF on its blank lines (mixed line
// endings), and a line that Set rewrites turns LF as well.
func TestCRLFCurrentBehaviour(t *testing.T) {
	in := "# c\r\nA=\"1\"\r\n\r\nB=2\r\nraw\r\n"
	d := mustParse(t, in)

	if v, _ := d.Get("A"); v != "1" {
		t.Errorf("A = %q, want 1 (CR must not leak into the value)", v)
	}
	if v, _ := d.Get("B"); v != "2" {
		t.Errorf("B = %q, want 2 (CR must not leak into the value)", v)
	}
	if kinds(d) != "CKBKR" {
		t.Errorf("kinds = %s, want CKBKR", kinds(d))
	}
	if got, want := string(d.Bytes()), "# c\r\nA=\"1\"\r\n\nB=2\r\nraw\r\n"; got != want {
		t.Errorf("untouched CRLF file\n got: %q\nwant: %q", got, want)
	}

	d.Set("A", "9")
	if got, want := string(d.Bytes()), "# c\r\nA=\"9\"\n\nB=2\r\nraw\r\n"; got != want {
		t.Errorf("after Set(A)\n got: %q\nwant: %q", got, want)
	}
}

// Duplicate keys. The LAST assignment wins, which is what a shell sourcing
// the file would do and what someone who appended a line at the bottom meant.
// Get used to return the first one, so such an edit looked as if it had been
// ignored, and Set rewrote the first and left the later line behind as a
// stale assignment that disagreed with what Get reported. Set now collapses
// the duplicates onto the line Get reads. Delete still removes all of them.
func TestDuplicateKeyLastOneWins(t *testing.T) {
	d := mustParse(t, "A=\"first\"\n# between\nA=\"second\"\n")
	if v, _ := d.Get("A"); v != "second" {
		t.Errorf("Get on a duplicate = %q, want the last occurrence", v)
	}

	d.Set("A", "new")
	if got, want := string(d.Bytes()), "# between\nA=\"new\"\n"; got != want {
		t.Errorf("Set on a duplicate should collapse the earlier line\n got: %q\nwant: %q", got, want)
	}
	if v, _ := d.Get("A"); v != "new" {
		t.Errorf("Get after Set = %q, want new", v)
	}
	if n := d.countKey("A"); n != 1 {
		t.Errorf("the file still holds %d assignments of A", n)
	}

	d.Delete("A")
	if got, want := string(d.Bytes()), "# between\n"; got != want {
		t.Errorf("Delete on a duplicate: got %q, want %q", got, want)
	}
}

// A newline in a value used to split the KEY="value" line in two, and the
// second half was read back as another key: `config set BANNER_TEXT
// $'x\nINSTALL_PATH="/elsewhere"'` injected a key. Both the validator and
// Save refuse it now, so neither a typo nor a hostile string can rewrite a
// setting the user did not touch.
func TestControlCharactersInValuesAreRefused(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"BANNER_TEXT", "x\nINSTALL_PATH=\"/elsewhere\""},
		{"SHOW_ALIASES_FOR", "a\rb"},
		{"KEEPASS_ENTRY", "a\x00b"},
		{"DEFAULT_DOMAIN", "example.com\nTHEME=\"red\""},
	} {
		if err := ValidateKey(c.key, c.value); err == nil {
			t.Errorf("%s accepted a value containing a control character: %q", c.key, c.value)
		}
	}

	// And Save refuses it even if something skipped validation.
	t.Setenv("HOME", t.TempDir())
	cfg := Default()
	cfg.BannerText = "x\nINSTALL_PATH=\"/elsewhere\""
	if err := Save(cfg); err == nil {
		t.Fatal("Save wrote a value with a newline in it")
	}
	if _, err := Load(); err != nil {
		t.Errorf("the refused save left the config unreadable: %v", err)
	}
}

// AppendComment adds the "# " itself, AppendBlank adds an empty line; the
// template used for a first run is built from them.
func TestAppendHelpers(t *testing.T) {
	d := NewDoc()
	d.AppendComment("plain")
	d.AppendComment("# already")
	d.AppendBlank()
	d.Set("A", "1")
	if got, want := string(d.Bytes()), "# plain\n# already\n\nA=\"1\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// FuzzParseRoundTrip: whatever bytes are in the file, Parse must not panic,
// rendering and re-parsing must give the same keys and values in the same
// order, and rendering must be stable after the first pass. It also feeds each
// parsed value back through Set, which exercises escape/unescape on arbitrary
// text; the existing round-trip test only covers a handful of fixed values.
func FuzzParseRoundTrip(f *testing.F) {
	for _, s := range []string{
		"",
		"\n",
		"A=\"1\"\n",
		"# comment\n\nA=\"1\"\nunknown=\"x\"\n",
		"bare\nKEY\n=value\n  A=1\nMY-KEY=1\n1KEY=1\n",
		"A=unquoted\nB=\"two words\"\nC=\"a # b\"\nD=\nE=\"\"\nF  =  \"v\"  \n",
		"A=\"1\"\n# no final newline",
		"# only comments\n# here\n",
		"A=\"1\"\r\n# c\r\n\r\nB=2\r\n",
		"A=\"first\"\nA=\"second\"\n",
		`A="a\"b"` + "\n" + `B="C:\\keys\\x"` + "\n" + `C="^spam\.example\.com$"` + "\n",
		"A=\"\n",
		"A=\"\\\"\n",
		"A='x'\nB=\"v\" # trailing\n",
		"   \n\t\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		d, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		out := d.Bytes()
		again, err := Parse(out)
		if err != nil {
			t.Fatalf("Parse(Bytes(Parse(%q))): %v", in, err)
		}
		if a, b := strings.Join(kvList(d), "\n"), strings.Join(kvList(again), "\n"); a != b {
			t.Fatalf("keys/values changed across Bytes->Parse\ninput: %q\nbytes: %q\n first: %s\nsecond: %s", in, out, a, b)
		}
		if string(again.Bytes()) != string(out) {
			t.Fatalf("Bytes not stable\ninput: %q\n  one: %q\n  two: %q", in, out, again.Bytes())
		}

		// Set(key, value) with the value Get returned must read back as that
		// value, wherever it came from.
		for _, e := range d.entries {
			if e.kind != kindKV {
				continue
			}
			s := mustParseFuzz(t, out)
			s.Set(e.key, e.value)
			back := mustParseFuzz(t, s.Bytes())
			got, ok := back.Get(e.key)
			want, _ := s.Get(e.key)
			if !ok || got != want {
				t.Fatalf("Set(%q, %q) read back as %q, %v\ninput: %q", e.key, e.value, got, ok, in)
			}
			break // one entry keeps the body fast
		}
	})
}

func mustParseFuzz(t *testing.T, b []byte) *Doc {
	t.Helper()
	d, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(%q): %v", b, err)
	}
	return d
}
