package bw

import (
	"encoding/json"
	"testing"
)

func TestResolveItem_UUIDPassesThrough(t *testing.T) {
	name, err := ResolveItemFromList("11111111-2222-3333-4444-555555555555", nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("got %q", name)
	}
}

func TestResolveItem_ExactNameWins(t *testing.T) {
	items := []map[string]any{
		{"id": "u1", "name": "tuta"},
		{"id": "u2", "name": "app.tuta.com"},
	}
	raw, _ := json.Marshal(items)
	var parsed []map[string]any
	_ = json.Unmarshal(raw, &parsed)
	uuid, err := ResolveItemFromList("tuta", parsed)
	if err != nil {
		t.Fatal(err)
	}
	if uuid != "u1" {
		t.Errorf("uuid = %q", uuid)
	}
}

func TestResolveItem_AmbiguousErrors(t *testing.T) {
	items := []map[string]any{
		{"id": "u1", "name": "tuta one"},
		{"id": "u2", "name": "tuta two"},
	}
	_, err := ResolveItemFromList("tuta", items)
	if err == nil {
		t.Fatalf("expected ambiguity error")
	}
}
