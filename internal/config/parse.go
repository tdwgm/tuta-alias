// Package config parses and writes the shell-style ~/.config/tuta-alias/tuta-alias.conf
// file. The parser is round-trip-aware: comments, blank lines, and unknown
// keys are preserved across Load → Save cycles so users can hand-edit the
// file in vim without losing context when the TUI saves changes back.
package config

import (
	"bytes"
	"fmt"
	"strings"
)

type entryKind int

const (
	kindBlank entryKind = iota
	kindComment
	kindKV
	kindRaw
)

type entry struct {
	kind  entryKind
	text  string
	key   string
	value string
}

// Doc is a parsed config-file representation that preserves layout.
type Doc struct {
	entries []entry
}

// NewDoc returns an empty Doc.
func NewDoc() *Doc { return &Doc{} }

// Parse parses raw bytes into a Doc.
func Parse(raw []byte) (*Doc, error) {
	d := &Doc{}
	if len(raw) == 0 {
		return d, nil
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			d.entries = append(d.entries, entry{kind: kindBlank, text: ""})
		case strings.HasPrefix(trimmed, "#"):
			d.entries = append(d.entries, entry{kind: kindComment, text: line})
		default:
			k, v, ok := parseKV(line)
			if !ok {
				d.entries = append(d.entries, entry{kind: kindRaw, text: line})
				continue
			}
			d.entries = append(d.entries, entry{kind: kindKV, text: line, key: k, value: v})
		}
	}
	return d, nil
}

func parseKV(line string) (string, string, bool) {
	eq := strings.Index(line, "=")
	if eq <= 0 {
		return "", "", false
	}
	rawKey := strings.TrimSpace(line[:eq])
	if !validKey(rawKey) {
		return "", "", false
	}
	rawVal := strings.TrimSpace(line[eq+1:])
	if len(rawVal) >= 2 && rawVal[0] == '"' && rawVal[len(rawVal)-1] == '"' {
		return rawKey, unescape(rawVal[1 : len(rawVal)-1]), true
	}
	return rawKey, rawVal, true
}

// unescape reverses escape() for a double-quoted value. Without it every
// Load -> Save cycle doubled the backslashes in a value: SHOW_ALIASES_FOR
// regexps (`\.`), BANNER_TEXT and KeePass paths grew one level per save until
// they no longer matched anything.
//
// Only the two sequences escape() produces are consumed. Anything else keeps
// its backslash, so a hand-written `^spam\.example\.com$` and the escaped
// `^spam\\.example\\.com$` this package writes both read back as the same
// regexp.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '"') {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, c := range k {
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// Get returns the value for key and whether it was set in the doc.
//
// The LAST occurrence wins, which is what a shell sourcing the file would do
// and what someone who appended a line at the bottom meant. Returning the
// first one made such an edit look as if it had been ignored.
func (d *Doc) Get(key string) (string, bool) {
	value, found := "", false
	for _, e := range d.entries {
		if e.kind == kindKV && e.key == key {
			value, found = e.value, true
		}
	}
	return value, found
}

// Set updates the value for key. If the key already exists, only the value is
// replaced (comments and position preserved). If the key is new, a new kv
// entry is appended at the end of the doc.
//
// A duplicated key is collapsed: the last occurrence is the one Get reads, so
// that is the one rewritten, and the earlier ones are dropped rather than
// left behind as stale lines that look authoritative.
func (d *Doc) Set(key, value string) {
	last := -1
	for i, e := range d.entries {
		if e.kind == kindKV && e.key == key {
			last = i
		}
	}
	if last >= 0 {
		d.entries[last].value = value
		d.entries[last].text = fmt.Sprintf(`%s="%s"`, key, escape(value))
		if dup := d.countKey(key); dup > 1 {
			d.dropKeyExcept(key, last)
		}
		return
	}
	d.entries = append(d.entries, entry{
		kind:  kindKV,
		text:  fmt.Sprintf(`%s="%s"`, key, escape(value)),
		key:   key,
		value: value,
	})
}

// Delete removes a key (and nothing else) from the doc. Used when a key is
// renamed: Save preserves unknown keys on purpose, so without this the old
// name would sit in the file forever, ignored and confusing.
func (d *Doc) Delete(key string) {
	out := d.entries[:0]
	for _, e := range d.entries {
		if e.kind == kindKV && e.key == key {
			continue
		}
		out = append(out, e)
	}
	d.entries = out
}

// countKey counts how many lines set key.
func (d *Doc) countKey(key string) int {
	n := 0
	for _, e := range d.entries {
		if e.kind == kindKV && e.key == key {
			n++
		}
	}
	return n
}

// dropKeyExcept removes every line setting key except the one at keep.
func (d *Doc) dropKeyExcept(key string, keep int) {
	out := d.entries[:0]
	for i, e := range d.entries {
		if e.kind == kindKV && e.key == key && i != keep {
			continue
		}
		out = append(out, e)
	}
	d.entries = out
}

// AppendComment adds a comment line at the end of the doc.
func (d *Doc) AppendComment(text string) {
	if !strings.HasPrefix(text, "#") {
		text = "# " + text
	}
	d.entries = append(d.entries, entry{kind: kindComment, text: text})
}

// AppendBlank adds a blank line at the end of the doc.
func (d *Doc) AppendBlank() {
	d.entries = append(d.entries, entry{kind: kindBlank, text: ""})
}

// Bytes renders the doc back to bytes for writing to disk.
func (d *Doc) Bytes() []byte {
	var buf bytes.Buffer
	for _, e := range d.entries {
		buf.WriteString(e.text)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
