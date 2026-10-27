// Package session ports tuta_alias/session.py — the persistent session file
// at $XDG_CACHE_HOME/tuta-alias/session.json with mode 0600 and atomic write.
//
// Field names use json tags that match the Python schema exactly so the
// two implementations can read each other's files.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

type Session struct {
	MailAddress    string         `json:"mail_address"`
	UserID         string         `json:"user_id"`
	AccessToken    string         `json:"access_token"`
	UserGroupID    string         `json:"user_group_id"`
	GroupInfoID    []string       `json:"group_info_id"`
	MailGroupID    string         `json:"mail_group_id,omitempty"`
	CustomerID     string         `json:"customer_id,omitempty"`
	CustomerInfoID []string       `json:"customer_info_id,omitempty"`
	SavedAt        string         `json:"saved_at,omitempty"`
	Version        int            `json:"version"`
	Extra          map[string]any `json:"extra"`
}

// dir is the cache directory, or "" when neither XDG_CACHE_HOME nor a home
// directory says where it is. The error from os.UserHomeDir used to be
// discarded, and filepath.Join("", ".cache", …) is a RELATIVE path: with no
// HOME, `tuta-alias whoami` looked for ".cache/tuta-alias/session.json" under
// whatever directory it happened to run in, and a login wrote the access
// token there. internal/config and internal/vault already answer "" and
// refuse rather than guess.
//
// The XDG spec says a relative XDG_CACHE_HOME is invalid and must be ignored,
// which is the same reasoning.
func dir() string {
	if x := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "tuta-alias")
	}
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return ""
	}
	return filepath.Join(h, ".cache", "tuta-alias")
}

// Path is "" when there is nowhere to put the session. Every caller that
// writes checks for it; Load turns it into the same "no session" error the
// user would get from a missing file, because that is what it means.
func Path() string {
	d := dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "session.json")
}

// errNoHome is what every entry point returns when there is nowhere to keep
// the session. It names the two variables that decide it, so the fix is in
// the message.
var errNoHome = errors.New("no home directory: set HOME or XDG_CACHE_HOME so the session has somewhere to live")

func (s *Session) ToJSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}

func FromJSON(raw []byte) (*Session, error) {
	s := &Session{}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("malformed session file: %w", err)
	}
	if s.Extra == nil {
		s.Extra = map[string]any{}
	}
	return s, nil
}

func Save(s *Session) (string, error) {
	d := dir()
	if d == "" {
		return "", errNoHome
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(d, 0o700)
	body, err := s.ToJSON()
	if err != nil {
		return "", err
	}
	p := Path()
	// 0600 and flushed before the rename: this file holds the access token,
	// and a half-written one logs the user out.
	if err := atomicfile.Write(p, body, 0o600); err != nil {
		return "", err
	}
	return p, nil
}

func Load() (*Session, error) {
	p := Path()
	if p == "" {
		return nil, errNoHome
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no session at %s — run `tuta-alias login` first", p)
		}
		return nil, err
	}
	return FromJSON(raw)
}

func Clear() (bool, error) {
	p := Path()
	if p == "" {
		// Nothing to clear, and nowhere it could have been written.
		return false, nil
	}
	err := os.Remove(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
