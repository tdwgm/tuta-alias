package creds

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
	dir, err := os.MkdirTemp("", "tuta-alias-creds-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creds tests: temp dir:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	for _, k := range []string{"HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		if err := os.Setenv(k, dir); err != nil {
			fmt.Fprintln(os.Stderr, "creds tests: setenv:", err)
			return 1
		}
	}
	return m.Run()
}
