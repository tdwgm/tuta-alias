package plusalias

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerate_LengthAndCharset(t *testing.T) {
	for _, n := range []int{1, 5, 8, 20} {
		s, err := Generate(n)
		if err != nil {
			t.Fatalf("Generate(%d): %v", n, err)
		}
		if len(s) != n {
			t.Errorf("Generate(%d) len = %d", n, len(s))
		}
		for _, c := range s {
			if !strings.ContainsRune(suffixAlphabet, c) {
				t.Errorf("Generate(%d) produced out-of-charset %q", n, c)
			}
		}
	}
}

func TestGenerate_FloorsToOne(t *testing.T) {
	s, err := Generate(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 1 {
		t.Errorf("Generate(0) len = %d, want 1", len(s))
	}
}

func TestGenerate_Varies(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		s, _ := Generate(8)
		seen[s] = true
	}
	if len(seen) < 2 {
		t.Errorf("Generate appears non-random: %v", seen)
	}
}

func TestSplitBase(t *testing.T) {
	cases := []struct {
		in, local, domain string
	}{
		{"spam@example.com", "spam", "example.com"},
		{"spam+old@example.com", "spam", "example.com"},
		{"spam+a+b@example.com", "spam", "example.com"},
		{" toni@example.org ", "toni", "example.org"},
		{"nodomain", "nodomain", ""},
	}
	for _, c := range cases {
		l, d := SplitBase(c.in)
		if l != c.local || d != c.domain {
			t.Errorf("SplitBase(%q) = (%q,%q), want (%q,%q)", c.in, l, d, c.local, c.domain)
		}
	}
}

func TestCompose(t *testing.T) {
	if got := Compose("spam", "jv5d8", "example.com"); got != "spam+jv5d8@example.com" {
		t.Errorf("Compose = %q", got)
	}
}

func TestValidSuffix(t *testing.T) {
	ok := []string{"jv5d8", "github", "a", "a.b-c_d", "ABC123"}
	bad := []string{"", "has space", "a@b", "a+b", "över"}
	for _, s := range ok {
		if !ValidSuffix(s) {
			t.Errorf("ValidSuffix(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidSuffix(s) {
			t.Errorf("ValidSuffix(%q) = true, want false", s)
		}
	}
}

func TestLoad_Missing_ReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load empty = %v", got)
	}
}

func TestAppend_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	e1 := Entry{Address: "spam+jv5d8@example.com", Note: "github", Created: "2026-06-17"}
	e2 := Entry{Address: "spam+a3k9x@example.com", Note: "reddit", Created: "2026-06-17"}
	if err := Append(e1); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	if err := Append(e2); err != nil {
		t.Fatalf("Append e2: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0] != e1 || got[1] != e2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	want := filepath.Join(home, ".config", "tuta-alias", "plus-aliases.json")
	if Path() != want {
		t.Errorf("Path() = %q, want %q", Path(), want)
	}
}
