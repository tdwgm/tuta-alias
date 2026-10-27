package creds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/bw"
	"github.com/tdwgm/tuta-alias/internal/config"
)

const bwSessionKey = "bw-session"

type bitwardenBackend struct{}

func (bitwardenBackend) Name() string { return "bitwarden" }

func (bitwardenBackend) Available() error {
	if _, err := exec.LookPath("bw"); err != nil {
		return errors.New("bw not on PATH — install the Bitwarden CLI (https://bitwarden.com/help/cli/)")
	}
	return nil
}

// Fetch resolves a Tuta credential triple from the configured Bitwarden item.
// Master password (when needed for unlock) is requested via prompt so the
// TUI can present its own overlay.
func (b bitwardenBackend) Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	if cfg.BWItem == "" {
		return Creds{}, errors.New("BW_ITEM is empty — set it in Options or tuta-alias.conf")
	}
	if err := b.Available(); err != nil {
		return Creds{}, err
	}

	cli, _ := exec.LookPath("bw")
	session, err := b.resolveSession(cli, cfg, prompt, verbose)
	if err != nil {
		return Creds{}, err
	}

	// The item id, not the name: see bwItemID. This is also the first command
	// that touches the vault, so it is the one that retries with a fresh
	// session when the cached one turns out to be stale.
	item, err := bwItemID(cli, session, cfg.BWItem, verbose)
	if err != nil {
		var ce *bwCmdError
		if !errors.As(err, &ce) {
			// The lookup ran and the answer was "no" (no match, or several):
			// unlocking again would only ask for the master password for
			// nothing.
			return Creds{}, err
		}
		_ = Clear(bwSessionKey)
		session, err = b.unlock(cli, cfg, prompt, verbose)
		if err != nil {
			return Creds{}, err
		}
		if item, err = bwItemID(cli, session, cfg.BWItem, verbose); err != nil {
			return Creds{}, err
		}
	}

	vlog(verbose, fmt.Sprintf("bw get username %s", item))
	email, err := bwGet(cli, session, "username", item)
	if err != nil {
		return Creds{}, fmt.Errorf("bw get username: %w", err)
	}

	vlog(verbose, fmt.Sprintf("bw get password %s", item))
	password, err := bwGet(cli, session, "password", item)
	if err != nil {
		return Creds{}, fmt.Errorf("bw get password: %w", err)
	}

	// TOTP is best-effort — many Bitwarden items don't have an authenticator
	// field. We swallow the error and return totp="".
	vlog(verbose, fmt.Sprintf("bw get totp %s", item))
	totp, _ := bwGet(cli, session, "totp", item)

	return Creds{Email: email, Password: password, TOTP: totp}, nil
}

func (b bitwardenBackend) ClearCache(_ *config.Config) error {
	return Clear(bwSessionKey)
}

// resolveSession returns a valid session token, using cache when possible.
func (b bitwardenBackend) resolveSession(cli string, cfg *config.Config, prompt Prompter, verbose io.Writer) (string, error) {
	if session, ok := Get(bwSessionKey, cfg.CacheTTL); ok {
		if validateBWSession(cli, session) {
			vlog(verbose, "using cached bw session")
			return session, nil
		}
		_ = Clear(bwSessionKey)
		vlog(verbose, "cached bw session stale — re-unlocking")
	}
	return b.unlock(cli, cfg, prompt, verbose)
}

func (b bitwardenBackend) unlock(cli string, cfg *config.Config, prompt Prompter, verbose io.Writer) (string, error) {
	if prompt == nil {
		return "", errors.New("no master-password prompt given — internal bug")
	}
	vlog(verbose, "bw unlock")
	pw, err := prompt("Bitwarden master password: ")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(cli, "unlock", "--raw")
	cmd.Stdin = strings.NewReader(pw + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("bw unlock: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	session := strings.TrimSpace(stdout.String())
	if session == "" {
		return "", errors.New("bw unlock returned empty session")
	}
	if err := Cache(bwSessionKey, session, cfg.CacheTTL); err != nil {
		fmt.Fprintln(os.Stderr, "[bitwarden] warning: could not cache session:", err)
	}
	return session, nil
}

func validateBWSession(cli, session string) bool {
	cmd := exec.Command(cli, "status")
	cmd.Env = append(os.Environ(), "BW_SESSION="+session)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &result); err != nil {
		return false
	}
	return result.Status == "unlocked"
}

func bwGet(cli, session, field, item string) (string, error) {
	cmd := exec.Command(cli, "get", field, item)
	cmd.Env = append(os.Environ(), "BW_SESSION="+session)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", errors.New(oneLine(stderr.String(), err))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// bwCmdError marks a failure of the `bw` command itself, as opposed to a
// lookup that ran and found nothing or too much. Only the former is worth
// retrying with a fresh session.
type bwCmdError struct{ msg string }

func (e *bwCmdError) Error() string { return e.msg }

// bwItemID resolves BW_ITEM to a Bitwarden item id.
//
// `bw get <field> <query>` does not look up a name, it searches: names, URIs,
// notes and custom fields all count, so a query that is unique as a name can
// still match several items. bw then exits 1 with "More than one result was
// found. Try getting a specific object by `id`" followed by a list of bare
// ids — eight lines of nothing actionable in a TUI that has one line for an
// error. `bw list items --search` returns the names with the ids, so the item
// actually named BW_ITEM can be picked here, and when the name genuinely is
// ambiguous the error says what to set it to.
func bwItemID(cli, session, query string, verbose io.Writer) (string, error) {
	if bw.IsItemID(query) {
		return strings.TrimSpace(query), nil
	}
	vlog(verbose, fmt.Sprintf("bw list items --search %s", query))
	cmd := exec.Command(cli, "list", "items", "--search", query)
	cmd.Env = append(os.Environ(), "BW_SESSION="+session)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &bwCmdError{fmt.Sprintf("bw list items --search %q: %s",
			query, oneLine(stderr.String(), err))}
	}
	var items []map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &items); err != nil {
		return "", &bwCmdError{fmt.Sprintf("bw list items --search %q: unexpected output", query)}
	}
	return bw.ResolveItemFromList(query, items)
}

// oneLine collapses a command's stderr to what fits the one error line the
// TUI has: the first non-empty line, or the exec error when stderr is silent.
func oneLine(stderr string, err error) string {
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return err.Error()
}
