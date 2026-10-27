package consts

import (
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
)

// VersionStamp and CommitStamp are link-time stamps:
//
//	go build -ldflags "-X github.com/tdwgm/tuta-alias/internal/consts.VersionStamp=0.13.0"
//
// They must stay plain string variables with a literal initializer. The
// linker's -X writes the initial value of such a symbol; give one a computed
// initializer and -X is silently ignored.
//
// Nothing has to set them. The go tool records the version and the commit in
// every binary it builds (see AppVersion), so these exist for a release build
// that wants to state the version outright — goreleaser does this — and for
// a build made outside a git checkout.
var (
	VersionStamp = ""
	CommitStamp  = ""
)

// fallbackVersion is the release this source is working toward. It is used
// only when the build has no tag to derive a number from: a tarball, a
// shallow clone, a checkout from before the first tag was pushed, or a build
// outside git entirely. Go answers "(devel)" or a 0.0.0 pseudo-version in
// those cases, and "0.0.0-dev" tells the user nothing.
//
// It is the one number left in the source, and it cannot drift: it is the
// newest heading in CHANGELOG.md, which TestFallbackMatchesTheChangelog
// checks. The tag still decides what a real build reports.
const fallbackVersion = "0.14.5"

// devSuffix marks a version that no tag backs.
const devSuffix = "-dev"

// AppVersion is this CLI's own version, shown in the TUI banner and by
// `tuta-alias version`.
//
// The git tag is the source of truth and nothing in the source says the
// number. The go tool derives Main.Version from the repository it builds in:
// at tag v0.13.0 it is "v0.13.0", one commit later the pseudo-version
// "v0.13.1-0.20261027004908-a86bfd454e11", and a modified tree gets "+dirty"
// appended. A hand-maintained constant was a second place to remember, and
// the one that was always a release behind.
var AppVersion = sync.OnceValue(appVersion)

func appVersion() string {
	if s := strings.TrimSpace(VersionStamp); s != "" {
		return strings.TrimPrefix(s, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		// A 0.0.0 pseudo-version means the history holds no tag to count
		// from, which is the fallback's case, not a version.
		if v, _ := parseBuildVersion(bi.Main.Version); v != "" && !strings.HasPrefix(v, "0.0.0") {
			return v
		}
	}
	v := fallbackVersion + devSuffix
	if modified() {
		v += dirtySuffix
	}
	return v
}

// CommitHash is the commit this binary was built from, "" when unknown. The
// updater compares it with what the remote ref points at and treats "" as
// "cannot tell whether this is current" rather than guessing.
//
// `go build` and `go install ./cmd/...` record the revision of the checkout
// they ran in. `go install module@ref`, which is how `tuta-alias update`
// rebuilds, records no revision — the build happens in the module cache, not
// in a checkout — but the version it resolved ref to is a pseudo-version with
// the commit in it, so the commit survives either way.
var CommitHash = sync.OnceValue(commitHash)

func commitHash() string {
	if s := strings.TrimSpace(CommitStamp); s != "" {
		return s
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= shortCommitLen {
			return s.Value[:shortCommitLen]
		}
	}
	_, commit := parseBuildVersion(bi.Main.Version)
	return commit
}

// Modified reports whether the tree the binary was built from had
// uncommitted changes. The commit alone would claim more than it knows.
var Modified = sync.OnceValue(modified)

func modified() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.modified" {
			return s.Value == "true"
		}
	}
	return strings.HasSuffix(bi.Main.Version, dirtySuffix)
}

// shortCommitLen is how much of the revision is kept. It matches the length
// of a pseudo-version's commit field, so the two sources agree, and
// update.Check shortens the remote sha to the local length before comparing.
const shortCommitLen = 12

const dirtySuffix = "+dirty"

// pseudoRE matches the tail of a module pseudo-version:
// v0.13.1-0.20261027004908-a86bfd454e11, or v0.0.0-20261027004836-54c5ba3bb5bb
// when no tag precedes it. See https://go.dev/ref/mod#pseudo-versions.
var pseudoRE = regexp.MustCompile(`^(.+?)-(?:0\.)?\d{14}-([0-9a-f]{12})$`)

// parseBuildVersion turns debug.BuildInfo's Main.Version into the version to
// show and, for a pseudo-version, the commit it carries. Both are "" when
// there is nothing to read: Go writes "(devel)" when it cannot tell, and
// older toolchains wrote nothing at all.
func parseBuildVersion(main string) (version, commit string) {
	v := strings.TrimSpace(main)
	if v == "" || v == "(devel)" {
		return "", ""
	}
	dirty := strings.HasSuffix(v, dirtySuffix)
	v = strings.TrimPrefix(strings.TrimSuffix(v, dirtySuffix), "v")
	if v == "" {
		return "", ""
	}
	if m := pseudoRE.FindStringSubmatch(v); m != nil {
		// A pseudo-version's base is the version this commit comes *before*,
		// so "0.13.1-dev" says "on the way to 0.13.1", not "is 0.13.1".
		v, commit = m[1]+"-dev", m[2]
	}
	if dirty {
		v += dirtySuffix
	}
	return v, commit
}
