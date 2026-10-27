// Package redact removes the parts of this program's own output that identify
// the person running it.
//
// Everything tuta-alias prints for diagnosis names the account somewhere: the
// checklist shows the logged-in address, the log shows which alias failed to
// be created, a path shows the user name, a proxy URL can carry credentials,
// and an error can quote a token. All of that is fine on the machine it
// belongs to and wrong the moment it is pasted into an issue, a chat or a
// screenshot.
//
// Redaction is therefore a step on the way OUT, never on the way in: nothing
// here changes what is stored. The log file keeps the real address, because a
// log you cannot read is not a log; `logs --redact` is what you share.
//
// It over-redacts on purpose. A token that was not a token comes back as
// <token> and costs nothing; an address that slipped through costs the person
// who trusted the output.
package redact

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Placeholders. They are deliberately obvious: a reader has to be able to see
// that something was removed rather than wonder whether it was ever there.
const (
	Email  = "<email>"
	User   = "<user>"
	Token  = "<token>"
	Secret = "<redacted>"
	UID    = "<uid>"
	Domain = "<domain>"
)

var (
	// An address with a domain. Custom domains identify a person as surely as
	// the local part does, so the whole address goes.
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

	// user:password@host in any URL, as a proxy value can carry one.
	urlCredsRE = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s:]+:[^/@\s]*@`)

	// A long unbroken base64url run: the api-token (43 chars), a Tuta access
	// token, a bearer token quoted in an error. Hyphen and underscore are in
	// the alphabet, so the bound is high enough not to eat ordinary words.
	tokenRE = regexp.MustCompile(`\b[A-Za-z0-9_-]{24,}\b`)

	// /run/user/1000, where the reboot cache lives.
	runUserRE = regexp.MustCompile(`/run/user/\d+`)

	// Authorization: Bearer <anything>
	bearerRE = regexp.MustCompile(`(?i)(bearer\s+)\S+`)

	// A bare host name. An address is not the only thing that names the
	// account: this program prints the custom domain on its own, in a DNS
	// check ("dns check mydomain.se: HTTP 500"), in DEFAULT_DOMAIN and in
	// SHOW_ALIASES_FOR, and a domain identifies its owner as surely as the
	// local part does, through WHOIS if nothing else.
	//
	// Case-insensitive, as a domain name is: the list below is lower case, and
	// "MyDomain.SE" must not get through because of it.
	//
	// The suffix has to be a real top-level domain, which is what keeps this
	// from eating every file name with an extension: without that bound
	// "options.go", "tuta-alias.conf" and "CHANGELOG.md" all look like hosts.
	// The list is the common ones plus the Nordic ones this program's users
	// are likely to own, because a miss is a leak and an unlisted TLD only
	// leaves a domain in the output, which is where it already was.
	hostRE = regexp.MustCompile(`(?i)\b(?:[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?\.)+` +
		`(?:com|net|org|edu|gov|io|dev|app|me|co|biz|info|xyz|email|mail|cloud|` +
		`online|site|tech|pro|link|live|one|page|space|store|website|systems|` +
		`de|se|no|dk|fi|is|nu|ax|fo|gl|nl|be|fr|it|es|pt|ch|at|pl|cz|uk|ie|eu|ru|us|ca)\b`)
)

// keptHosts are the hosts this program prints that belong to it, not to the
// person running it. A domain under one of them is kept too, so
// app.tuta.com survives as itself.
var keptHosts = []string{
	"tuta.com", "tutanota.de", "tutanota.com", "tuta.io", "tutao.de",
	"github.com", "githubusercontent.com", "golang.org", "go.dev",
	"bitwarden.com", "keepassxc.org", "gitleaks.io",
	// The documentation domains, which the README and the examples use.
	"example.com", "example.org", "example.net", "example.invalid",
}

// keptHost reports whether host is one of this program's own, in which case
// removing it would only make the output harder to read.
func keptHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, kept := range keptHosts {
		if h == kept || strings.HasSuffix(h, "."+kept) {
			return true
		}
	}
	return false
}

// looksLikeToken separates an encoded secret from a long word. A token is a
// mixed run of letters and digits; "InvalidSoftwareVersionError" is 27 letters
// and the name of an error this program prints, so length alone cannot decide.
func looksLikeToken(s string) bool {
	var digit, letter bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return digit && letter
}

// Text removes the identifying parts of s.
//
// The order is load-bearing: the home directory is folded to ~ first so that
// what remains is readable, and the user name is only removed after that, so
// a path does not turn into a row of placeholders.
func Text(s string) string {
	if s == "" {
		return s
	}
	s = foldHome(s)
	s = urlCredsRE.ReplaceAllString(s, "${1}"+Secret+"@")
	s = bearerRE.ReplaceAllString(s, "${1}"+Token)
	s = emailRE.ReplaceAllString(s, Email)
	// After emailRE, which has already taken the host of any address with it.
	s = hostRE.ReplaceAllStringFunc(s, func(m string) string {
		if keptHost(m) {
			return m
		}
		return Domain
	})
	s = runUserRE.ReplaceAllString(s, "/run/user/"+UID)
	s = tokenRE.ReplaceAllStringFunc(s, func(m string) string {
		if looksLikeToken(m) {
			return Token
		}
		return m
	})
	return foldUser(s)
}

// Lines redacts each line, for the log and the checklist.
func Lines(in []string) []string {
	out := make([]string, len(in))
	for i, l := range in {
		out[i] = Text(l)
	}
	return out
}

// foldHome replaces the home directory with ~.
func foldHome(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return s
	}
	home = strings.TrimRight(home, string(os.PathSeparator))
	return strings.ReplaceAll(s, home, "~")
}

// foldUser removes the account name where it appears on its own, which is
// what is left of a path that did not start at $HOME (/var/mail/toni, a
// KeePass database on another volume).
func foldUser(s string) string {
	name := userName()
	if name == "" || len(name) < 3 {
		// Too short to replace safely: "al" would hit every word with it.
		return s
	}
	return strings.ReplaceAll(s, name, User)
}

func userName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Base(home)
}

// Notice is the line that goes above redacted output, so a reader knows what
// is missing and why the placeholders are there.
const Notice = "# redacted for sharing: addresses, domains, paths, user name and tokens were removed"
