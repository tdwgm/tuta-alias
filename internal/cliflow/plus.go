package cliflow

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/plusalias"
)

// Plus generates a "+" (sub-)address for base, saves it locally with an
// optional note, and prints it. No session/API needed — sub-addressing is
// handled server-side; only the generated address is stored locally. If
// suffix is empty, a random one of config-defined length is generated.
func Plus(stdout, stderr io.Writer, base, note, suffix string) int {
	local, domain := plusalias.SplitBase(base)
	if domain == "" {
		fmt.Fprintf(stderr, "base address %q has no domain\n", base)
		return 2
	}
	if suffix == "" {
		cfg, _ := config.Load()
		if cfg == nil {
			cfg = config.Default()
		}
		gen, err := plusalias.Generate(cfg.PlusSuffixLenInt())
		if err != nil {
			return bail(stderr, err)
		}
		suffix = gen
	}
	if !plusalias.ValidSuffix(suffix) {
		fmt.Fprintf(stderr, "invalid suffix %q (use only a-z 0-9 . _ -)\n", suffix)
		return 2
	}
	addr := plusalias.Compose(local, suffix, domain)
	entry := plusalias.Entry{
		Address: addr,
		Note:    note,
		Created: time.Now().Format("2006-01-02"),
	}
	if err := plusalias.Append(entry); err != nil {
		return bail(stderr, err)
	}
	fmt.Fprintln(stdout, addr)
	return 0
}

// PlusList prints the locally-saved "+" addresses.
func PlusList(stdout, stderr io.Writer, asJSON bool) int {
	entries, err := plusalias.Load()
	if err != nil {
		return bail(stderr, err)
	}
	if asJSON {
		b, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "(no saved + addresses)")
		return 0
	}
	for _, e := range entries {
		line := e.Address
		if e.Note != "" {
			line += "  — " + e.Note
		}
		if e.Created != "" {
			line += "  (" + e.Created + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}
