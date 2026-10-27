// Package notes stores local, free-text notes about aliases (e.g. where an
// address is used). Like the plus-alias store it is purely local — no API —
// and lives next to the config as a simple address→note JSON map.
package notes

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/atomicfile"
)

// Path returns ~/.config/tuta-alias/alias-notes.json.
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "tuta-alias", "alias-notes.json")
}

// Load reads the address→note map. A missing file yields an empty map.
func Load() (map[string]string, error) {
	p := Path()
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]string{}, nil
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return m, nil
}

// Save writes the map atomically, creating the config dir if needed.
func Save(m map[string]string) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(p), err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicfile.Write(p, data, 0o600)
}

// Set stores (or, when note is empty, removes) the note for addr.
func Set(addr, note string) error {
	m, err := Load()
	if err != nil {
		return err
	}
	addr = strings.TrimSpace(addr)
	note = strings.TrimSpace(note)
	if note == "" {
		delete(m, addr)
	} else {
		m[addr] = note
	}
	return Save(m)
}
