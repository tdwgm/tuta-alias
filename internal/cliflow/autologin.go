package cliflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/creds"
	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// Exit codes of `login --auto`. Each has a matching "status" in the JSON
// line on stdout, so a GUI caller knows which input to ask for without
// parsing error text.
const (
	autoExitOK       = 0
	autoExitFailed   = 2 // status "rejected" (401) or "error"
	autoExitNeedMstr = 3 // status "need_master": backend is locked
	autoExitNeedTOTP = 4 // status "need_totp": credentials lack a TOTP
	autoExitNeedCred = 5 // status "need_credentials": no source at all
)

var (
	errNeedMaster = errors.New("master password required")
	errNeedTOTP   = errors.New("TOTP code required")
)

// autoResult is the single JSON line `login --auto` prints on stdout.
type autoResult struct {
	Status string `json:"status"`
	Source string `json:"source,omitempty"` // bitwarden | keepassxc | keyring | manual
	Email  string `json:"email,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Seams for tests.
var (
	autoLoadConfig = config.Load
	autoResolve    = creds.Resolve
	autoLoadSaved  = creds.LoadLogin
	autoDoLogin    = func(email, pw string, totp int, cb login.TOTPCallback) (*session.Session, error) {
		return login.Login(api.NewClient(consts.BaseURL, ""), email, pw, totp, cb)
	}
	autoSaveSession = func(s *session.Session) error { _, err := login.SaveSession(s); return err }
	autoSaveLogin   = creds.SaveLogin
)

// AutoLogin logs in without a terminal: it takes credentials from the first
// source that yields them and reports what is missing otherwise.
//
//  1. --email + $TUTA_PASSWORD (a login form filled in by the caller)
//  2. the configured CRED_BACKEND — fully automatic while its session is
//     cached/unlocked; a master password is read from stdin when given
//  3. the login saved in the system keyring (needs only a TOTP)
//
// Secrets come on stdin (master password, then TOTP; one line each, EOF =
// not given) or in $TUTA_PASSWORD / $TUTA_TOTP — never argv. Nothing is
// prompted, so no external dialog can steal the input.
func AutoLogin(stdout, stderr io.Writer, in *os.File, opts LoginOpts) int {
	ask := newLinePrompter(io.Discard, in)
	res, code := autoLogin(ask, opts, stderr)
	_ = json.NewEncoder(stdout).Encode(res)
	return code
}

func autoLogin(ask *linePrompter, opts LoginOpts, stderr io.Writer) (autoResult, int) {
	var verbose io.Writer
	if opts.Verbose {
		verbose = stderr
	}
	// A master password is read from stdin once; a second call means the one
	// given was not accepted, and the caller must ask again.
	masterRead, masterGiven := false, false
	masterPrompt := func(string) (string, error) {
		if masterRead {
			return "", errNeedMaster
		}
		masterRead = true
		pw, err := ask.buffered()
		if errors.Is(err, errAborted) || (err == nil && pw == "") {
			return "", errNeedMaster
		}
		masterGiven = err == nil
		return pw, err
	}

	var c creds.Creds
	source := ""
	needMaster := false
	backendName := "" // named in need_master, so the caller can say whose master password
	var backendErr error

	if pw := os.Getenv("TUTA_PASSWORD"); opts.Email != "" && pw != "" {
		c, source = creds.Creds{Email: opts.Email, Password: pw}, "manual"
	} else if cfg, err := autoLoadConfig(); err == nil && cfg != nil {
		if b, err := autoResolve(cfg); err == nil && b != nil && b.Name() != "bwrun" && b.Available() == nil {
			backendName = b.Name()
			vlog(stderr, opts.Verbose, "trying credential backend "+b.Name())
			got, err := b.Fetch(cfg, masterPrompt, verbose)
			switch {
			case err == nil && got.Email != "" && got.Password != "":
				c, source = got, b.Name()
			case errors.Is(err, errNeedMaster):
				needMaster = true
			case err != nil:
				backendErr = err
			}
		}
	}
	if source == "" && !masterGiven {
		// A master password that was given and failed is reported below; only
		// fall back to the saved login when none was typed.
		if saved, err := autoLoadSaved(); err == nil {
			vlog(stderr, opts.Verbose, "using login saved in the system keyring")
			c, source = saved, "keyring"
		}
	}
	if source == "" {
		switch {
		case masterGiven && (needMaster || backendErr != nil):
			msg := "master password not accepted"
			if backendErr != nil {
				msg = firstLine(backendErr.Error())
			}
			return autoResult{Status: "need_master", Source: backendName, Error: msg}, autoExitNeedMstr
		case needMaster:
			return autoResult{Status: "need_master", Source: backendName}, autoExitNeedMstr
		case backendErr != nil:
			return autoResult{Status: "need_credentials", Error: firstLine(backendErr.Error())}, autoExitNeedCred
		default:
			return autoResult{Status: "need_credentials"}, autoExitNeedCred
		}
	}

	totp := opts.TOTP
	if totp == 0 && c.TOTP != "" {
		totp, _ = strconv.Atoi(strings.TrimSpace(c.TOTP))
	}
	if totp == 0 {
		totp, _ = strconv.Atoi(os.Getenv("TUTA_TOTP"))
	}
	// Asked only if the account has 2FA: the next stdin line, else need_totp.
	totpCB := func() (int, error) {
		line, err := ask.buffered()
		if errors.Is(err, errAborted) || (err == nil && strings.TrimSpace(line) == "") {
			return 0, errNeedTOTP
		}
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			return 0, fmt.Errorf("TOTP must be digits: %w", err)
		}
		return n, nil
	}

	vlog(stderr, opts.Verbose, fmt.Sprintf("login %s via %s", c.Email, source))
	sess, err := autoDoLogin(c.Email, c.Password, totp, totpCB)
	res := autoResult{Source: source, Email: c.Email}
	if err != nil {
		var ae *api.APIError
		switch {
		case errors.Is(err, errNeedTOTP), errors.Is(err, login.ErrTOTPRequired):
			res.Status = "need_totp"
			return res, autoExitNeedTOTP
		case errors.As(err, &ae) && ae.Status == 401:
			res.Status, res.Error = "rejected", "login rejected — check password and TOTP"
		default:
			res.Status, res.Error = "error", firstLine(err.Error())
		}
		return res, autoExitFailed
	}
	if err := autoSaveSession(sess); err != nil {
		res.Status, res.Error = "error", firstLine(err.Error())
		return res, autoExitFailed
	}
	res.Email = sess.MailAddress
	if opts.Save && source == "manual" {
		if err := autoSaveLogin(c.Email, c.Password); err != nil {
			fmt.Fprintln(stderr, "warning:", err)
		}
	}
	res.Status = "ok"
	return res, autoExitOK
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
