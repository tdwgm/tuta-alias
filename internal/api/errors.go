package api

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// APIError is returned for non-2xx HTTP responses from the Tuta REST API.
type APIError struct {
	Status  int
	Code    string
	Message string
}

// tutaErrorNames maps Tuta's REST status codes to the error names upstream
// uses (tuta-sdk/rust/sdk/src/rest_error.rs). Tuta sends no error body for
// most of these, so the name is often the only readable part of an error.
var tutaErrorNames = map[int]string{
	400: "BadRequestError",
	401: "NotAuthenticatedError",
	403: "NotAuthorizedError",
	404: "NotFoundError",
	405: "MethodNotAllowedError",
	408: "RequestTimeoutError",
	412: "PreconditionFailedError",
	413: "PayloadTooLargeError",
	423: "LockedError",
	429: "TooManyRequestsError",
	440: "SessionExpiredError",
	470: "AccessDeactivatedError",
	471: "AccessExpiredError",
	472: "AccessBlockedError",
	473: "InvalidDataError",
	474: "InvalidSoftwareVersionError",
	475: "LimitReachedError",
	500: "InternalServerError",
	502: "BadGatewayError",
	503: "ServiceUnavailableError",
	507: "InsufficientStorageError",
}

func (e *APIError) Error() string {
	name := e.Code
	if name == "" {
		name = tutaErrorNames[e.Status]
	}
	s := fmt.Sprintf("HTTP %d", e.Status)
	if name != "" {
		s += " " + name
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	// This line is what the CLI prints to stderr and the TUI puts in its error
	// line, and not all of it has been through redact: Code is a response
	// header, so is Message when the body was empty (the error-id), and a
	// detail may have been appended to it since. Text that redact already
	// escaped passes through unchanged.
	return oneLine(s)
}

// IsSessionExpired reports whether err is an APIError indicating the access
// token is no longer valid. Tuta returns HTTP 401 (Unauthorized) and 440
// (Login Time-out / session expired) in this case.
func IsSessionExpired(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 401 || ae.Status == 440
	}
	return false
}

// Mirrors tuta_alias/api.py::_REDACT_RE.
var redactRE = regexp.MustCompile(
	`(?i)(authVerifier|accessToken|authToken|sessionKey|password|salt|otpCode|recoverCodeVerifier|recoverCode|\w*Enc\w*Key)(?:["'\s:=]|%22|%3A)+[A-Za-z0-9+/=_%-]+`,
)

// redact turns the body of an error response into text that is safe to show:
// the first max bytes of it, with the secrets masked and with whatever a
// terminal would act on written out as an escape.
//
// The order is deliberate. The body is cut first, on the bytes the server sent,
// so the pattern never scans more than max of them and max stays the amount of
// the reply that is quoted. Escaping makes the text longer, by up to four times,
// so what comes back can be longer than max. The alternative, cutting after the
// escaping, would show less and less of the readable text the more control
// characters the body has, and could end in the middle of an escape. The cut is
// moved back to the start of a rune, so a character that was split is dropped
// whole instead of being shown as an invalid byte the server did not send.
// Secrets are masked before the escaping, while the text is still what the
// server sent: an escaped newline between a key and its value is no longer
// whitespace to the pattern, and the secret would survive. The escaping comes
// last, so that nothing after it can bring a raw character back.
func redact(body []byte, max int) string {
	if len(body) > max {
		n := max
		// A rune is at most utf8.UTFMax bytes long, so there is nothing to gain
		// by looking back further: what is there is not UTF-8, and the escaping
		// shows it as it is.
		for i := 1; i < utf8.UTFMax && n > 0 && !utf8.RuneStart(body[n]); i++ {
			n--
		}
		body = body[:n]
	}
	return oneLine(redactRE.ReplaceAllString(string(body), "$1=<redacted>"))
}

// oneLine spells out whatever would end the line or act on the terminal that
// shows it, the way logx's oneLine does for the log file. It is repeated here
// because logx imports config, which imports this package.
//
// What a server sends is not this program's own text. A newline in it forges a
// second line of output, a carriage return overwrites the line before it, and
// an escape sequence is run by the terminal that prints it (OSC 52 writes the
// clipboard). The escape is written out rather than dropped, so the line still
// shows that something was there. A byte that is not valid UTF-8 is escaped
// too: a terminal in 8-bit mode reads a stray 0x9b as CSI.
func oneLine(s string) string {
	if !strings.ContainsFunc(s, mustEscape) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case r == '\u2028' || r == '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// mustEscape is the cheap first pass of oneLine, so that the usual message,
// which has nothing to escape, is not rebuilt. It may say yes to a valid
// U+FFFD, which the second pass then writes back unchanged.
func mustEscape(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError
}
