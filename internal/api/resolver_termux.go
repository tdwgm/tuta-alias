package api

import (
	"os"
	"path/filepath"
	"strings"
)

// termuxPrefix is Termux's $PREFIX when the environment does not carry it.
const termuxPrefix = "/data/data/com.termux/files/usr"

// termuxNameservers reads the nameserver lines of Termux's resolv.conf
// ($PREFIX/etc/resolv.conf). Nil when the file is missing or names none.
// Kept free of build tags so it is tested on every platform; only the Android
// build uses it (resolver_android.go).
func termuxNameservers() []string {
	prefix := os.Getenv("PREFIX")
	if prefix == "" {
		prefix = termuxPrefix
	}
	data, err := os.ReadFile(filepath.Join(prefix, "etc", "resolv.conf"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}
