package tui

import (
	"reflect"
	"strings"
	"testing"

	bkey "github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
)

// textinputKeys returns every key bubbles' textinput binds, read from the
// dependency rather than from a list someone wrote down. A hand-maintained
// copy is exactly how 0.11.2 shipped ^u, ^e and ^h as shortcuts while the
// field already owned them.
func textinputKeys() map[string]bool {
	out := map[string]bool{}
	v := reflect.ValueOf(textinput.DefaultKeyMap)
	for i := 0; i < v.NumField(); i++ {
		b, ok := v.Field(i).Interface().(bkey.Binding)
		if !ok {
			continue
		}
		for _, k := range b.Keys() {
			out[k] = true
		}
	}
	return out
}

// No shortcut may use a key the text field owns. This is the guard that makes
// the gate unnecessary for the global shortcuts: ^g, ^o and ^q are free, so
// they can fire inside a field without stealing anything — and if a future
// bubbles release claims one of them, this fails loudly instead of quietly
// breaking editing.
func TestNoShortcutCollidesWithTextInput(t *testing.T) {
	owned := textinputKeys()
	if len(owned) < 10 {
		t.Fatalf("read only %d keys from textinput.DefaultKeyMap — reflection broke", len(owned))
	}
	for _, sm := range allMenus() {
		for _, b := range sm.btns {
			for _, k := range strings.Split(b.key, "/") {
				k = strings.TrimSpace(strings.Replace(k, "^", "ctrl+", 1))
				if k == "" || !strings.HasPrefix(k, "ctrl+") {
					continue
				}
				if owned[k] && !gatedShortcuts[k] && fieldShortcuts[k] == "" {
					t.Errorf("%s binds %q to %q, but bubbles' textinput already uses it for editing.\n"+
						"Either pick a free key, or add it to fieldShortcuts with what the collision costs.",
						sm.name, k, b.label)
				}
			}
		}
	}
}

// A gated key must really be gated: it may collide, but only because the
// field wins inside a field.
func TestGatedShortcutsYieldToTheField(t *testing.T) {
	for k := range gatedShortcuts {
		m := homeWithRows(t, "alpha@example.com")
		m.homeFocus = homeFocusNew
		m.syncHomeFocus()
		if _, _, done := m.globalKey(k); done {
			t.Errorf("%s fired inside a text field; it is gated precisely because the field owns it", k)
		}
		m2 := homeWithRows(t, "alpha@example.com")
		m2.homeFocus = homeFocusList
		if _, _, done := m2.globalKey(k); !done {
			t.Errorf("%s did nothing outside a field either — then it is just broken", k)
		}
	}
}

// The three global shortcuts must be free, since they are deliberately NOT
// gated behind inTextField().
func TestGlobalShortcutsAreFree(t *testing.T) {
	owned := textinputKeys()
	for _, k := range []string{"ctrl+g", "ctrl+o", "ctrl+q"} {
		if owned[k] {
			t.Errorf("%s is global and ungated, but textinput owns it", k)
		}
	}
}

// A documented collision has to be a real one. An entry in fieldShortcuts for
// a key the field does NOT own is a stale exception that would hide the next
// genuine collision on that key.
func TestFieldShortcutsAreRealCollisions(t *testing.T) {
	owned := textinputKeys()
	for k, why := range fieldShortcuts {
		if !owned[k] {
			t.Errorf("fieldShortcuts documents %q (%s), but textinput does not bind it — drop the exception", k, why)
		}
		if why == "" {
			t.Errorf("fieldShortcuts[%q] has no explanation of what the collision costs", k)
		}
	}
}
