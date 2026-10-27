package creds

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	keyringlib "github.com/zalando/go-keyring"

	"github.com/tdwgm/tuta-alias/internal/config"
)

// fakeBW is a stand-in `bw` CLI: always locked, unlocks with "master".
//
// It reproduces the real CLI's behaviour that `bw get <field> <name>` is a
// search, not a lookup: an item named exactly "tuta" is not enough, because
// other items match the string through their URI or notes, and bw then exits
// 1 with "More than one result was found" and a list of bare ids. Only a
// request by id answers. `bw list items --search` is what carries the names,
// so that is where the id has to come from.
const fakeBW = `#!/bin/sh
case "$1" in
status) echo '{"status":"locked"}' ;;
unlock) read pw; [ "$pw" = "master" ] && echo SESSION || { echo "Invalid master password." >&2; exit 1; } ;;
list) [ "$BW_SESSION" = SESSION ] || exit 1
     cat <<'JSON'
[{"id":"fd3bfc5e-036f-42f6-a77f-b4d700bdc830","name":"tuta"},
 {"id":"8f8d52cc-41ec-4616-ab8c-b4d300c64a2b","name":"tutanota recovery codes"},
 {"id":"811d5029-b4c1-47cd-a960-b462005865eb","name":"mail archive"}]
JSON
     ;;
get) [ "$BW_SESSION" = SESSION ] || exit 1
     if [ "$3" != fd3bfc5e-036f-42f6-a77f-b4d700bdc830 ]; then
       echo "More than one result was found. Try getting a specific object by id:" >&2
       echo fd3bfc5e-036f-42f6-a77f-b4d700bdc830 >&2
       echo 8f8d52cc-41ec-4616-ab8c-b4d300c64a2b >&2
       exit 1
     fi
     case "$2" in username) echo u@tuta.com ;; password) echo pw ;; *) exit 1 ;; esac ;;
esac
`

// The master password comes from the Prompter passed to this Fetch call —
// no package-level hook.
func TestBitwardenFetch_UsesPerCallPrompter(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bw"), []byte(fakeBW), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := &config.Config{CredBackend: "bitwarden", BWItem: "tuta", CacheTTL: "8h"}
	errAbort := errors.New("user aborted")

	cases := []struct {
		name      string
		prompt    Prompter
		wantErr   string
		wantEmail string
		wantLabel bool
	}{
		{name: "prompt answers", prompt: func(string) (string, error) { return "master", nil }, wantEmail: "u@tuta.com", wantLabel: true},
		{name: "prompt error is returned", prompt: func(string) (string, error) { return "", errAbort }, wantErr: errAbort.Error(), wantLabel: true},
		{name: "wrong password", prompt: func(string) (string, error) { return "nope", nil }, wantErr: "Invalid master password", wantLabel: true},
		{name: "nil prompt", prompt: nil, wantErr: "no master-password prompt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyringlib.MockInit()
			var labels []string
			prompt := c.prompt
			if prompt != nil {
				inner := prompt
				prompt = func(label string) (string, error) {
					labels = append(labels, label)
					return inner(label)
				}
			}
			got, err := bitwardenBackend{}.Fetch(cfg, prompt, nil)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
			} else if err != nil || got.Email != c.wantEmail || got.Password != "pw" {
				t.Fatalf("Fetch = %+v, %v", got, err)
			}
			if gotLabel := len(labels) == 1 && strings.Contains(labels[0], "Bitwarden"); gotLabel != c.wantLabel {
				t.Errorf("prompt labels = %q", labels)
			}
		})
	}
}

// BW_ITEM names an item; bw wants an id. The name is resolved through
// `bw list items --search`, and what the user sees when that cannot be done
// has to fit the one error line the TUI gives it: the raw bw failure is a
// header plus one bare id per matching item.
func TestBitwardenFetch_ResolvesTheItemNameToAnID(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bw"), []byte(fakeBW), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	master := func(string) (string, error) { return "master", nil }

	cases := []struct {
		name    string
		item    string
		wantErr string // "" = the login must come back
	}{
		// One item is named exactly "tuta"; the others only match the search.
		{name: "exact name wins", item: "tuta"},
		// An id is passed straight through, no lookup needed.
		{name: "id passes through", item: "fd3bfc5e-036f-42f6-a77f-b4d700bdc830"},
		// Nothing is named "tutanota", so there is nothing to pick.
		{name: "no exact match says what to set", item: "tutanota", wantErr: "set BW_ITEM"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keyringlib.MockInit()
			cfg := &config.Config{CredBackend: "bitwarden", BWItem: c.item, CacheTTL: "8h"}
			got, err := bitwardenBackend{}.Fetch(cfg, master, nil)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Fetch: %v", err)
				}
				if got.Email != "u@tuta.com" || got.Password != "pw" {
					t.Errorf("Fetch = %+v, want u@tuta.com / pw", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("Fetch = %+v, want an error", got)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("error spans %d lines, the TUI shows one:\n%s",
					strings.Count(err.Error(), "\n")+1, err)
			}
		})
	}
}
