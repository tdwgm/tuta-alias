package config

import "testing"

// A proxy URL can carry credentials, and the log file is shared: `logs`
// prints it, the TUI Logs screen renders it, rotation copies it into .1.
func TestLogValueRemovesProxyCredentials(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{"user and password", "TUTA_PROXY", "socks5://bob:s3cr3t@10.0.0.1:1055",
			"socks5://" + LogSecret + "@10.0.0.1:1055"},
		{"user only", "TUTA_PROXY", "http://bob@proxy.example.com:8080",
			"http://" + LogSecret + "@proxy.example.com:8080"},
		{"no userinfo is left alone", "TUTA_PROXY", "socks5://10.0.0.1:1055",
			"socks5://10.0.0.1:1055"},
		{"empty", "TUTA_PROXY", "", ""},
		{"unparseable but still carries credentials", "TUTA_PROXY",
			"socks5://bob:s3c ret@10.0.0.1:1055", "socks5://" + LogSecret + "@10.0.0.1:1055"},
		{"no scheme, still credentials", "TUTA_PROXY", "bob:s3cr3t@10.0.0.1:1055",
			LogSecret + "@10.0.0.1:1055"},
		{"another key passes through", "DEFAULT_DOMAIN", "example.com", "example.com"},
		{"a key whose value looks like a URL passes through", "LOG_FILE",
			"/home/x/bob:s3cr3t@log", "/home/x/bob:s3cr3t@log"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LogValue(c.key, c.value); got != c.want {
				t.Errorf("LogValue(%q, %q) = %q, want %q", c.key, c.value, got, c.want)
			}
		})
	}
}

// Whatever it returns must never hold the password that went in.
func TestLogValueNeverLeaksThePassword(t *testing.T) {
	for _, v := range []string{
		"socks5://bob:s3cr3t@10.0.0.1:1055",
		"SOCKS5://bob:s3cr3t@10.0.0.1:1055",
		"socks5://bob:s3cr3t@[fe80::1]:1055",
		"socks5h://bob:s3cr3t@host/path?x=1",
		"bob:s3cr3t@host",
		"://bob:s3cr3t@host",
	} {
		if got := LogValue("TUTA_PROXY", v); contains(got, "s3cr3t") || contains(got, "bob") {
			t.Errorf("LogValue(%q) = %q, which still names the credentials", v, got)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
