// Package bw resolves a Tuta password from a Bitwarden vault item, then
// re-exec's `tuta-alias login --email X --totp Y` via `bw-run` so the
// password lives only in the subprocess environment. Port of
// tuta_alias/cli.py::_bw_exec_login + _resolve_bw_item + _get_bw_session.
package bw

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

func vlog(stderr io.Writer, verbose bool, msg string) {
	if verbose && stderr != nil {
		fmt.Fprintf(stderr, "→ %s\n", msg)
	}
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IsItemID reports whether s is already a Bitwarden item id, in which case
// no lookup is needed.
func IsItemID(s string) bool { return uuidRE.MatchString(strings.TrimSpace(s)) }

// ResolveItemFromList encapsulates the pure-logic part of resolution so it
// can be tested without running `bw`. Mirrors Python's _resolve_bw_item.
//
// Exported because `bw get <name>` searches names, URIs, notes and more, so
// a name that is unique as a name still makes bw exit 1 with "More than one
// result was found"; internal/creds resolves the name to an id through here
// before asking for a field.
func ResolveItemFromList(query string, items []map[string]any) (string, error) {
	if IsItemID(query) {
		return strings.TrimSpace(query), nil
	}
	if len(items) == 0 {
		return "", fmt.Errorf("no Bitwarden item matches '%s'", query)
	}
	exact := []map[string]any{}
	for _, i := range items {
		if n, _ := i["name"].(string); n == query {
			exact = append(exact, i)
		}
	}
	if len(exact) == 1 {
		if id, ok := exact[0]["id"].(string); ok {
			return id, nil
		}
	}
	if len(exact) > 1 {
		names := []string{}
		for _, i := range exact {
			names = append(names, asString(i["name"]))
		}
		return "", fmt.Errorf("%d Bitwarden items are named %q (%s) — set BW_ITEM to the item id",
			len(exact), query, strings.Join(names, ", "))
	}
	if len(items) > 1 {
		names := []string{}
		for _, i := range items {
			names = append(names, asString(i["name"]))
		}
		return "", fmt.Errorf("%q matches %d Bitwarden items (%s) and none is named exactly %q — "+
			"set BW_ITEM to the exact name or to the item id", query, len(items),
			strings.Join(names, ", "), query)
	}
	if id, ok := items[0]["id"].(string); ok {
		return id, nil
	}
	return "", errors.New("could not extract item id")
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return "?"
}

// ExecLogin runs the full BW flow then exec's bw-run … tuta-alias login.
// On success this function never returns. On failure it returns an error.
// stderr receives "→ step" lines when verbose is true (mirrors Python -v).
func ExecLogin(stderr io.Writer, query, email string, totp int, verbose bool) error {
	if _, err := exec.LookPath("bw-run"); err != nil {
		return errors.New("bw-run not on PATH; install it or pass --email/--totp/--password manually")
	}
	vlog(stderr, verbose, "bw-run --sync")
	if err := exec.Command("bw-run", "--sync").Run(); err != nil {
		return fmt.Errorf("bw-run --sync failed: %w", err)
	}

	bwSession, err := readKeyringSession()
	if err != nil {
		return err
	}

	listRaw, err := bwListItems(query, bwSession)
	if err != nil {
		return err
	}
	var items []map[string]any
	if err := json.Unmarshal(listRaw, &items); err != nil {
		return fmt.Errorf("bw list items: bad output: %w", err)
	}
	uuid, err := ResolveItemFromList(query, items)
	if err != nil {
		return err
	}
	vlog(stderr, verbose, fmt.Sprintf("resolving '%s' → bw item %s", query, uuid))

	resolvedEmail := email
	if resolvedEmail == "" {
		vlog(stderr, verbose, fmt.Sprintf("bw get username %s", uuid))
		out, err := bwGet("username", uuid, bwSession)
		if err != nil {
			return err
		}
		resolvedEmail = strings.TrimSpace(out)
		if resolvedEmail == "" {
			return fmt.Errorf("no username for Bitwarden item %s", uuid)
		}
	}

	resolvedTOTP := totp
	if resolvedTOTP == 0 {
		vlog(stderr, verbose, fmt.Sprintf("bw get totp %s", uuid))
		out, err := bwGet("totp", uuid, bwSession)
		if err != nil {
			return err
		}
		s := strings.TrimSpace(out)
		if s == "" {
			return fmt.Errorf("no TOTP for %s — does the item have a TOTP field?", uuid)
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("TOTP not an integer: %q", s)
		}
		resolvedTOTP = n
	}

	// TOTP goes via env (TUTA_TOTP), not argv, so it doesn't show in ps.
	//
	// The child is THIS binary, by absolute path: handing bw-run the bare
	// name let $PATH decide, so a different (or older) tuta-alias could end
	// up doing the login that this one started. Falling back to the name is
	// only for the case where the running binary cannot be located at all.
	self := "tuta-alias"
	if p, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
			p = resolved
		}
		self = p
	}
	args := []string{"bw-run", uuid, "TUTA_PASSWORD", "--",
		self, "login", "--email", resolvedEmail}
	if verbose {
		args = append(args, "-v")
	}
	vlog(stderr, verbose, "exec bw-run … tuta-alias login")
	binPath, err := exec.LookPath("bw-run")
	if err != nil {
		return err
	}
	env := append(os.Environ(), "TUTA_TOTP="+strconv.Itoa(resolvedTOTP))
	return syscall.Exec(binPath, args, env)
}

func readKeyringSession() (string, error) {
	keyOut, err := exec.Command("keyctl", "search", "@u", "user", "bw:session").Output()
	if err != nil {
		return "", errors.New("no cached Bitwarden session — run `bw-run --sync` once")
	}
	keyID := strings.TrimSpace(string(keyOut))
	sess, err := exec.Command("keyctl", "pipe", keyID).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(sess)), nil
}

func bwListItems(query, session string) ([]byte, error) {
	c := exec.Command("bw", "list", "items", "--search", query)
	c.Env = append(os.Environ(), "BW_SESSION="+session)
	return c.Output()
}

func bwGet(field, uuid, session string) (string, error) {
	c := exec.Command("bw", "get", field, uuid)
	c.Env = append(os.Environ(), "BW_SESSION="+session)
	out, err := c.Output()
	return string(out), err
}
