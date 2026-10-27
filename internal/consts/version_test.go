package consts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The version is read out of the build rather than written in the source, so
// what the go tool puts in Main.Version is the input to everything the user
// sees. Every case below is output observed from a real build:
//
//	at the tag              v0.13.0
//	at the tag, dirty tree  v0.13.0+dirty
//	one commit later        v0.13.1-0.20261027004908-a86bfd454e11
//	no tag in the history   v0.0.0-20261027004836-54c5ba3bb5bb
//	not a git checkout      (devel)
func TestParseBuildVersion(t *testing.T) {
	cases := []struct {
		in      string
		version string
		commit  string
	}{
		{"v0.13.0", "0.13.0", ""},
		{"v0.13.0+dirty", "0.13.0+dirty", ""},
		{"v0.13.1-0.20261027004908-a86bfd454e11", "0.13.1-dev", "a86bfd454e11"},
		{"v0.13.1-0.20261027004908-a86bfd454e11+dirty", "0.13.1-dev+dirty", "a86bfd454e11"},
		{"v0.0.0-20261027004836-54c5ba3bb5bb", "0.0.0-dev", "54c5ba3bb5bb"},
		{"v1.0.0-rc.1", "1.0.0-rc.1", ""},
		// Nothing to read: the caller falls back to "dev".
		{"(devel)", "", ""},
		{"", "", ""},
		{"   ", "", ""},
	}
	for _, c := range cases {
		gotV, gotC := parseBuildVersion(c.in)
		if gotV != c.version || gotC != c.commit {
			t.Errorf("parseBuildVersion(%q) = (%q, %q), want (%q, %q)",
				c.in, gotV, gotC, c.version, c.commit)
		}
	}
}

// A release build may state the version outright instead of leaving it to the
// build info. The stamp has to win, and the leading v has to go: the tag
// carries it (Go modules require it) and the banner does not want it.
func TestVersionStampWins(t *testing.T) {
	old := VersionStamp
	t.Cleanup(func() {
		VersionStamp = old
		// AppVersion memoises, so the value read in this test would otherwise
		// leak into every later one in this package.
		resetVersionCache()
	})
	for _, c := range []struct{ stamp, want string }{
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"  v1.2.3  ", "1.2.3"},
	} {
		VersionStamp = c.stamp
		resetVersionCache()
		if got := AppVersion(); got != c.want {
			t.Errorf("VersionStamp = %q -> AppVersion() = %q, want %q", c.stamp, got, c.want)
		}
	}
}

// The commit stamp wins over the build info for the same reason, and the
// updater compares it with a remote sha, so it must come back verbatim.
func TestCommitStampWins(t *testing.T) {
	old := CommitStamp
	t.Cleanup(func() {
		CommitStamp = old
		resetVersionCache()
	})
	CommitStamp = "a86bfd454e11"
	resetVersionCache()
	if got := CommitHash(); got != "a86bfd454e11" {
		t.Errorf("CommitHash() = %q, want the stamp verbatim", got)
	}
}

// The identifier Tuta is told about carries the version, so it must not be
// the literal "dev" in a release build either.
func TestClientIdentifierCarriesAVersion(t *testing.T) {
	if got := ClientIdentifier; got == "" || got[:len("tuta-alias/")] != "tuta-alias/" {
		t.Errorf("ClientIdentifier = %q, want it to start with tuta-alias/", got)
	}
}

// fallbackVersion is the only version number left in the source, and it
// exists for builds with no tag to read. It must be the release the
// CHANGELOG is currently describing, or a build from a tarball would claim a
// version this source is not.
func TestFallbackMatchesTheChangelog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The newest "## [0.13.0]" (or "## 0.13.0") heading in the file.
	head := regexp.MustCompile(`(?m)^## \[?(\d+\.\d+\.\d+)\]?`)
	m := head.FindSubmatch(raw)
	if m == nil {
		t.Fatal("CHANGELOG.md has no version heading")
	}
	if got := string(m[1]); got != fallbackVersion {
		t.Errorf("CHANGELOG.md starts at %s but fallbackVersion is %s — "+
			"a build with no tag would report the wrong release", got, fallbackVersion)
	}
}

// With no tag in the history Go answers with a 0.0.0 pseudo-version, which
// says nothing. The fallback stands in, marked as unreleased.
func TestVersionWithoutATag(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"v0.0.0-20261027004836-54c5ba3bb5bb", "0.0.0-dev"},
		{"(devel)", ""},
	} {
		got, _ := parseBuildVersion(c.in)
		if got != c.want {
			t.Fatalf("parseBuildVersion(%q) = %q, want %q", c.in, got, c.want)
		}
		// Both cases are what appVersion refuses to use, so it reports the
		// fallback instead; the test for that is the binary itself (see
		// cmd/tuta-alias: `tuta-alias version`), since appVersion reads the
		// build info of the test binary, not of a build under test.
	}
	if !strings.HasPrefix(fallbackVersion+devSuffix, fallbackVersion) {
		t.Fatal("the fallback must keep its own number")
	}
}
