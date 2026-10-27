// Package cliflow holds the CLI command handlers — one function per
// subcommand. They are called both from main.go (cobra) and (some of
// them) from the TUI's command-runner.
package cliflow

import (
	"errors"
	"fmt"
	"io"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/logx"
)

// bail writes a user-friendly error to w and returns the exit code (always 2).
// Mirrors tuta_alias/cli.py::_bail.
func bail(w io.Writer, err error) int {
	logx.Errorf("%v", err)
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			fmt.Fprintln(w, "Session expired. Run `tuta-alias login` again.")
		case 412:
			fmt.Fprintf(w, "Precondition failed: %s\n", apiErr.Error())
		case 429:
			fmt.Fprintln(w, "Rate limited or quota exceeded.")
		default:
			fmt.Fprintln(w, apiErr.Error())
		}
	} else {
		fmt.Fprintln(w, err.Error())
	}
	return 2
}

// Bail is the exported wrapper that prints to stderr and returns 2.
func Bail(stderr io.Writer, err error) int { return bail(stderr, err) }
