package redact

import (
	"strings"
	"testing"
)

// setup gives the test a known home directory and user name, because both are
// what Text folds away.
func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir() + "/toni"
	t.Setenv("HOME", home)
	t.Setenv("USER", "toni")
	t.Setenv("LOGNAME", "toni")
	return home
}

// Anything that names the account must not survive. Each case is a real line
// this program prints somewhere: the checklist, the log, an error.
func TestTextRemovesIdentifyingParts(t *testing.T) {
	home := setup(t)
	for _, c := range []struct {
		name, in string
		gone     []string
		kept     []string
	}{
		{
			name: "the logged-in address",
			in:   "session          toni@example.com  (saved 3h0m0s ago)",
			gone: []string{"toni@example.com", "toni"},
			kept: []string{"session", "saved 3h0m0s ago", Email},
		},
		{
			name: "an alias in a log line",
			in:   "2026-10-26 23:32:36 ERROR created spam@mydomain.se: HTTP 429", // gitleaks:allow (a fake address on a domain redact must NOT keep)
			gone: []string{"spam@mydomain.se", "mydomain.se"},                    // gitleaks:allow (a fake address on a domain redact must NOT keep)
			kept: []string{"ERROR", "HTTP 429"},
		},
		{
			name: "the home directory",
			in:   "config           " + home + "/.config/tuta-alias/tuta-alias.conf",
			gone: []string{home, "toni"},
			kept: []string{"~/.config/tuta-alias/tuta-alias.conf"},
		},
		{
			name: "a proxy password",
			in:   `proxy            socks5://bob:s3cr3t@10.0.0.1:1055`,
			gone: []string{"s3cr3t", "bob:"},
			kept: []string{"socks5://", "10.0.0.1:1055", Secret},
		},
		{
			name: "a bearer token",
			in:   "Authorization: Bearer aG93ZHkgdGhlcmUgZnJpZW5k",
			gone: []string{"aG93ZHkgdGhlcmUgZnJpZW5k"},
			kept: []string{"Authorization", Token},
		},
		{
			name: "an api token on its own",
			in:   "token is ZmFrZS10b2tlbi12YWx1ZS1mb3ItdGVzdGluZw and nothing else",
			gone: []string{"ZmFrZS10b2tlbi12YWx1ZS1mb3ItdGVzdGluZw"},
			kept: []string{"token is", Token, "and nothing else"},
		},
		{
			name: "the reboot cache path",
			in:   "cache at /run/user/1000/tuta-alias/bw-session",
			gone: []string{"/run/user/1000"},
			kept: []string{"/run/user/" + UID, "tuta-alias"},
		},
		{
			name: "a user name outside the home directory",
			in:   "KeePass DB       /mnt/backup/toni/vault.kdbx",
			gone: []string{"/toni/"},
			kept: []string{"/mnt/backup/", "vault.kdbx", User},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Text(c.in)
			for _, g := range c.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived redaction:\n  in:  %s\n  out: %s", g, c.in, got)
				}
			}
			for _, k := range c.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q should have been kept, the line is unreadable without it:\n  out: %s", k, got)
				}
			}
		})
	}
}

// Redaction must not destroy the parts that make output useful. A checklist
// of nothing but placeholders helps nobody.
func TestTextKeepsWhatIsNotIdentifying(t *testing.T) {
	setup(t)
	for _, s := range []string{
		"tuta-alias 0.12.0",
		"✓  serve            127.0.0.1:8088",
		"!  clipboard        no wl-copy, pbcopy or xclip",
		"HTTP 474 InvalidSoftwareVersionError",
		"LOG_LEVEL=\"debug\"",
		"argon2id + XChaCha20-Poly1305, mode 0600",
	} {
		if got := Text(s); got != s {
			t.Errorf("redaction changed a line that identifies nobody:\n  in:  %s\n  out: %s", s, got)
		}
	}
}

// A short user name must not be substituted: replacing "al" would hit every
// word containing it and turn the output into noise.
func TestShortUserNameIsNotSubstituted(t *testing.T) {
	t.Setenv("HOME", t.TempDir()+"/al")
	t.Setenv("USER", "al")
	t.Setenv("LOGNAME", "al")
	in := "already aligned, naturally"
	if got := Text(in); got != in {
		t.Errorf("a two-letter user name was substituted inside words: %s", got)
	}
}

func TestLinesRedactsEach(t *testing.T) {
	setup(t)
	in := []string{"a toni@example.com b", "plain", ""}
	got := Lines(in)
	if len(got) != len(in) {
		t.Fatalf("Lines returned %d lines for %d", len(got), len(in))
	}
	if strings.Contains(got[0], "toni@example.com") || got[1] != "plain" || got[2] != "" {
		t.Errorf("Lines = %q", got)
	}
}

// Redacting twice must not change the result: output is sometimes passed
// through more than one path (the TUI redacts what it shows, then copies it).
func TestTextIsIdempotent(t *testing.T) {
	setup(t)
	in := "session toni@example.com at " + t.TempDir() + "/toni/.config"
	once := Text(in)
	if twice := Text(once); twice != once {
		t.Errorf("redaction is not idempotent:\n  once:  %s\n  twice: %s", once, twice)
	}
}

func TestEmptyInput(t *testing.T) {
	setup(t)
	if Text("") != "" {
		t.Error("Text turned an empty string into something")
	}
}

// An address is not the only thing that names the account. This program
// prints the custom domain on its own: the DNS check logs it, DEFAULT_DOMAIN
// and SHOW_ALIASES_FOR hold it, and a domain identifies its owner through
// WHOIS. What must survive is the handful of hosts that belong to the program
// rather than to the person, and anything that only looks like a host.
func TestTextRemovesABareDomain(t *testing.T) {
	setup(t)
	cases := []struct {
		name string
		in   string
		gone []string
		kept []string
	}{
		{
			name: "the DNS check names the domain on its own",
			in:   "2026-10-27 10:00:00 ERROR dns check mydomain.se: HTTP 500",
			gone: []string{"mydomain.se"},
			kept: []string{"dns check", "HTTP 500"},
		},
		{
			name: "config values hold it too",
			in:   `config DEFAULT_DOMAIN = "mydomain.example-host.io"`,
			gone: []string{"mydomain.example-host.io"},
			kept: []string{"DEFAULT_DOMAIN"},
		},
		{
			name: "Tuta's own hosts stay, they are not the person",
			in:   "GET https://app.tuta.com/rest/sys/customerinfo failed",
			kept: []string{"app.tuta.com"},
		},
		{
			name: "so does the repository the update comes from",
			in:   `ref "main" does not exist in https://github.com/tdwgm/tuta-alias`,
			kept: []string{"github.com/tdwgm/tuta-alias"},
		},
		{
			name: "the documentation domain stays, examples are not secrets",
			in:   "proxy set to socks5://proxy.example.com:1080",
			kept: []string{"proxy.example.com"},
		},
		{
			name: "an address still goes whole, host included",
			in:   "alias created: shop-1a2b@mydomain.se", // gitleaks:allow (a fake address on a domain redact must NOT keep)
			gone: []string{"mydomain.se", "shop-1a2b"},
			kept: []string{Email},
		},
		{
			name: "a file name is not a host",
			in:   "wrote ~/.config/tuta-alias/tuta-alias.conf, read options.go and CHANGELOG.md",
			kept: []string{"tuta-alias.conf", "options.go", "CHANGELOG.md"},
		},
		{
			name: "an address and a version are not hosts",
			in:   "serve 127.0.0.1:8088, tuta-alias 0.13.0",
			kept: []string{"127.0.0.1:8088", "0.13.0"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Text(c.in)
			for _, g := range c.gone {
				if strings.Contains(got, g) {
					t.Errorf("%q survived redaction:\n  in:  %s\n  out: %s", g, c.in, got)
				}
			}
			for _, k := range c.kept {
				if !strings.Contains(got, k) {
					t.Errorf("%q was removed and should not have been:\n  in:  %s\n  out: %s", k, c.in, got)
				}
			}
		})
	}
}

// A domain name is not case sensitive, and DEFAULT_DOMAIN and SHOW_ALIASES_FOR
// are typed by hand, so MyDomain.SE is the same domain as mydomain.se. The top
// level domain used to be matched in lower case only, which left every domain
// that was not typed in lower case in the output.
func TestTextRemovesABareDomainInAnyCase(t *testing.T) {
	setup(t)
	for _, in := range []string{
		"2026-10-27 10:00:00 ERROR dns check MYDOMAIN.SE: HTTP 500",
		"2026-10-27 10:00:00 ERROR dns check MyDomain.Se: HTTP 500",
		`config DEFAULT_DOMAIN = "Mydomain.COM"`,
	} {
		got := Text(in)
		if strings.Contains(strings.ToLower(got), "mydomain") {
			t.Errorf("the domain survived redaction:\n  in:  %s\n  out: %s", in, got)
		}
		if !strings.Contains(got, Domain) {
			t.Errorf("the domain should have become %s:\n  out: %s", Domain, got)
		}
	}

	// The hosts that belong to the program stay, whatever their case.
	if got := Text("GET https://APP.TUTA.COM/rest failed"); !strings.Contains(got, "APP.TUTA.COM") {
		t.Errorf("a host of the program's own was removed because of its case: %s", got)
	}
}

// The list of top-level domains is "the common ones plus the Nordic ones this
// program's users are likely to own", and it left out .nu, which is the
// domain many Swedish users have, and the Nordic territories .ax, .fo and .gl.
// A miss is a leak: the custom domain stays in a log that is pasted into an
// issue.
func TestTextRemovesANordicDomain(t *testing.T) {
	setup(t)
	for _, tld := range []string{"se", "no", "dk", "fi", "is", "nu", "ax", "fo", "gl"} {
		in := "2026-10-27 10:00:00 ERROR dns check mydomain." + tld + ": HTTP 500"
		if got := Text(in); strings.Contains(got, "mydomain") || !strings.Contains(got, Domain) {
			t.Errorf(".%s survived redaction:\n  in:  %s\n  out: %s", tld, in, got)
		}
	}
}
