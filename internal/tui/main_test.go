package tui

import (
	"fmt"
	"os"
	"testing"

	keyringlib "github.com/zalando/go-keyring"
)

// TestMain keeps every test off the real system keyring and the user's
// cache/config directories, even when a test forgets its own t.Setenv.
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	keyringlib.MockInit()
	// Never touch the user's real clipboard: the Plus-screen tests drive a
	// copy on Enter, which shelled out to wl-copy on every `go test` run.
	copyToClipboard = func(string) (string, error) { return "test", nil }
	dir, err := os.MkdirTemp("", "tuta-alias-tui-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui tests: temp dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, k := range []string{"HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		if err := os.Setenv(k, dir); err != nil {
			fmt.Fprintln(os.Stderr, "tui tests: setenv:", err)
			return 1
		}
	}
	return m.Run()
}
