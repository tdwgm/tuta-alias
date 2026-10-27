package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTermuxNameservers(t *testing.T) {
	prefix := t.TempDir()
	t.Setenv("PREFIX", prefix)
	if got := termuxNameservers(); got != nil {
		t.Fatalf("missing resolv.conf: got %v, want nil", got)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Termux's resolv-conf package writes exactly the first two lines.
	conf := "nameserver 8.8.8.8\nnameserver 8.8.4.4\n# comment\nsearch lan\nnameserver\n  nameserver 2001:4860:4860::8888  \n"
	if err := os.WriteFile(filepath.Join(prefix, "etc", "resolv.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8", "8.8.4.4", "2001:4860:4860::8888"}
	if got := termuxNameservers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
