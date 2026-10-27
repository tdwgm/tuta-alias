package creds

import (
	"errors"
	"io"
	"os/exec"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// bwrunBackend wraps the local `bw-run` helper script that handles a
// flatpak-Bitwarden agent + keyctl-cached session token. The actual
// credential resolution and re-exec happens in internal/bw; this backend
// reports availability/configuration but does NOT call into Fetch — the
// bw-run flow must run *before* tuta-alias login because it uses
// syscall.Exec to replace the current process. Callers detect bwrun and
// invoke internal/bw.ExecLogin directly.
type bwrunBackend struct{}

func (bwrunBackend) Name() string { return "bwrun" }

func (bwrunBackend) Available() error {
	if _, err := exec.LookPath("bw-run"); err != nil {
		return errors.New("bw-run not on PATH — the bwrun backend requires the local bw-run wrapper script")
	}
	if _, err := exec.LookPath("keyctl"); err != nil {
		return errors.New("keyctl not on PATH — the bwrun backend requires keyutils for the session cache")
	}
	return nil
}

func (bwrunBackend) Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	return Creds{}, errors.New("bwrun backend uses syscall.Exec — call internal/bw.ExecLogin instead of creds.Fetch")
}

func (bwrunBackend) ClearCache(_ *config.Config) error {
	// bw-run manages its own keyctl cache; clearing it requires the user
	// to run `keyctl clear @u` or revoke the specific key by id, which is
	// outside our scope.
	return nil
}
