package config

import (
	"net/url"
	"strings"
)

// LogSecret is what replaces the credentials in a value that carries them.
const LogSecret = "<redacted>"

// LogValue renders a config value for a log file or a bug report.
//
// TUTA_PROXY is the one key whose value can carry a secret: api.ParseProxy
// accepts userinfo, so "socks5://bob:s3cr3t@10.0.0.1:1055" is a valid
// setting. Everywhere else that value is already protected (doctor prints
// u.Redacted(), internal/api strips it out of a transport error), and the log
// was the one place the raw string was written to disk, where `logs` prints
// it, the Logs screen renders it and log rotation copies it.
//
// Both halves of the userinfo go, not only the password: url.Redacted()
// keeps the user name, and a user name identifies the person as surely as a
// password compromises them.
func LogValue(key, value string) string {
	if key != "TUTA_PROXY" || strings.TrimSpace(value) == "" {
		return value
	}
	return redactURLCreds(value)
}

func redactURLCreds(raw string) string {
	s := strings.TrimSpace(raw)
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		if u.User == nil {
			return s
		}
		// The placeholder is spliced in rather than set as the userinfo:
		// url.URL.String() percent-encodes a user name, and "%3Credacted%3E"
		// reads like a secret rather than like a marker.
		u.User = nil
		out := u.String()
		if i := strings.Index(out, "://"); i >= 0 {
			return out[:i+3] + LogSecret + "@" + out[i+3:]
		}
		return LogSecret + "@" + out
	}
	// Unparseable, which does not mean harmless: a value the user typed by
	// hand can hold credentials and still not be a URL. Everything between
	// the scheme and the last "@" goes.
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return s
	}
	head := ""
	if i := strings.Index(s, "://"); i >= 0 && i+3 <= at {
		head = s[:i+3]
	}
	return head + LogSecret + "@" + s[at+1:]
}
