package tui

import (
	"io"
	"os"
	"os/exec"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/selfinstall"
)

// execCmd returns an *exec.Cmd wired to the current stdio. Used by
// tea.ExecProcess for $EDITOR.
func execCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

// installBinaryPath returns the install path in force (INSTALL_PATH, else
// ~/.local/bin/tuta-alias).
func installBinaryPath() string {
	if cfg, err := config.Load(); err == nil && cfg != nil {
		return cfg.BinaryPath()
	}
	return config.DefaultInstallPath()
}

func bashCompletionPath() string { return config.BashCompletionPath() }
func zshCompletionPath() string  { return config.ZshCompletionPath() }
func fishCompletionPath() string { return config.FishCompletionPath() }

// removeInstall is the Config screen's Uninstall row. It is the same code
// `tuta-alias uninstall` runs, so the two can no longer disagree about what
// an install consists of.
func removeInstall(w io.Writer, purge bool) { selfinstall.Remove(w, purge) }
