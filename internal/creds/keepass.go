package creds

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/config"
)

type keepassBackend struct{}

func (keepassBackend) Name() string { return "keepassxc" }

func (keepassBackend) Available() error {
	if _, err := exec.LookPath("keepassxc-cli"); err != nil {
		return errors.New("keepassxc-cli not on PATH — install the keepassxc package for your system")
	}
	return nil
}

// Fetch resolves credentials by calling keepassxc-cli with the configured
// DB + entry. Master is cached per absolute DB path under the user's
// CACHE_TTL policy.
func (b keepassBackend) Fetch(cfg *config.Config, prompt Prompter, verbose io.Writer) (Creds, error) {
	if cfg.KeePassDB == "" || cfg.KeePassEntry == "" {
		return Creds{}, errors.New("KEEPASS_DB and KEEPASS_ENTRY must both be set")
	}
	if err := b.Available(); err != nil {
		return Creds{}, err
	}
	cli, _ := exec.LookPath("keepassxc-cli")

	cacheKey := KeePassCacheKey(cfg.KeePassDB)
	master, ok := Get(cacheKey, cfg.CacheTTL)
	if ok {
		vlog(verbose, "using cached keepass master")
		creds, err := b.fetchWith(cli, cfg, master, verbose)
		if err == nil {
			return creds, nil
		}
		_ = Clear(cacheKey)
		vlog(verbose, "cached keepass master rejected — re-prompting")
	}

	if prompt == nil {
		return Creds{}, errors.New("no master-password prompt given — internal bug")
	}
	pw, err := prompt(fmt.Sprintf("KeePassXC master password for %s: ", filepath.Base(cfg.KeePassDB)))
	if err != nil {
		return Creds{}, err
	}
	creds, err := b.fetchWith(cli, cfg, pw, verbose)
	if err != nil {
		return Creds{}, err
	}
	if err := Cache(cacheKey, pw, cfg.CacheTTL); err != nil {
		fmt.Fprintln(os.Stderr, "[keepass] warning: could not cache master:", err)
	}
	return creds, nil
}

func (b keepassBackend) ClearCache(cfg *config.Config) error {
	if cfg.KeePassDB == "" {
		return nil
	}
	return Clear(KeePassCacheKey(cfg.KeePassDB))
}

func (b keepassBackend) fetchWith(cli string, cfg *config.Config, master string, verbose io.Writer) (Creds, error) {
	vlog(verbose, fmt.Sprintf("keepassxc-cli show -sa UserName %s %s", filepath.Base(cfg.KeePassDB), cfg.KeePassEntry))
	email, err := kpxcShow(cli, cfg.KeePassDB, cfg.KeePassEntry, master, "UserName")
	if err != nil {
		return Creds{}, fmt.Errorf("keepassxc-cli UserName: %w", err)
	}
	vlog(verbose, "keepassxc-cli show -sa Password ...")
	password, err := kpxcShow(cli, cfg.KeePassDB, cfg.KeePassEntry, master, "Password")
	if err != nil {
		return Creds{}, fmt.Errorf("keepassxc-cli Password: %w", err)
	}
	// TOTP is best-effort.
	vlog(verbose, "keepassxc-cli show -t ...")
	totp, _ := kpxcTOTP(cli, cfg.KeePassDB, cfg.KeePassEntry, master)
	return Creds{Email: email, Password: password, TOTP: totp}, nil
}

// kpxcShow runs `keepassxc-cli show -sa <attr> <db> <entry>` with master
// on stdin. -s = no protected-attribute warnings, -a = show one attribute.
func kpxcShow(cli, db, entry, master, attr string) (string, error) {
	cmd := exec.Command(cli, "show", "-s", "-a", attr, db, entry)
	cmd.Stdin = strings.NewReader(master + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// kpxcTOTP runs `keepassxc-cli show -t <db> <entry>` returning the current
// 6-digit TOTP code. Returns "" + err when the entry has no TOTP field.
func kpxcTOTP(cli, db, entry, master string) (string, error) {
	cmd := exec.Command(cli, "show", "-t", db, entry)
	cmd.Stdin = strings.NewReader(master + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// KeePassCacheKey returns the keyring user key for a given DB path
// (absolute path so symlink/relative variations don't fragment the cache).
// Exported so Options and uninstall --purge address the same entry.
func KeePassCacheKey(db string) string {
	abs, err := filepath.Abs(db)
	if err == nil {
		return "keepass:" + abs
	}
	return "keepass:" + db
}
