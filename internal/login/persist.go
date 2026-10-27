package login

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/crypto"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// newAccessKey returns a random AES-256 key for CreateSessionData.accessKey.
//
// Sending one makes the session persistent, like the web client's "Stay
// logged in" (LoginFacade.createSession: SessionType.Persistent →
// accessKey = aes256RandomKey()). Without it Tuta creates a temporary Login
// session that expires (HTTP 440) and forces a new password + TOTP login.
// The web client keeps the key only to decrypt a locally stored password on
// resume; tuta-alias resumes with the access token alone, so the key is not
// stored.
func newAccessKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// CloseSession deletes the server-side session behind accessToken (the
// web client's LoginFacade.deleteSession). Persistent sessions never expire
// on their own, so a logout or a re-login must close the old one, or it stays
// in the account's active-session list. A session that is already gone
// (401/404/440/471) counts as closed.
func CloseSession(accessToken string) error {
	return closeSessionAt(consts.BaseURL, accessToken)
}

func closeSessionAt(baseURL, accessToken string) error {
	listID, elemID, err := crypto.ParseSessionID(accessToken)
	if err != nil {
		return err
	}
	c := api.NewClient(baseURL, accessToken)
	err = c.EntityDelete(consts.SysApp, "Session", listID, elemID)
	var ae *api.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case 401, 404, 440, 471:
			return nil
		}
	}
	return err
}

// Seam for tests.
var closeSession = CloseSession

// ReplaceSession writes s as the current session and returns the access token
// of the session it replaces, or "" when there was none or it is the same
// token. It is SaveSession without the server round trip. The file write is
// local and is what the next screen depends on; the close of the replaced
// session is a network call, and the API client gives each request 30 seconds
// and sends up to three of them when Tuta answers 474. A caller with an event
// loop to keep alive stores here and closes the returned token itself with
// CloseSession, where it can wait.
func ReplaceSession(s *session.Session) (path, replaced string, err error) {
	old, _ := session.Load()
	path, err = session.Save(s)
	if err != nil {
		return "", "", err
	}
	if old != nil && old.AccessToken != "" && old.AccessToken != s.AccessToken {
		replaced = old.AccessToken
	}
	return path, replaced, nil
}

// SaveSession persists s and closes the session it replaces (best-effort:
// a failed close never fails the login).
func SaveSession(s *session.Session) (string, error) {
	p, replaced, err := ReplaceSession(s)
	if err != nil {
		return "", err
	}
	if replaced != "" {
		_ = closeSession(replaced)
	}
	return p, nil
}

// ErrSessionFileRemains wraps a failed deletion of the session file. It is
// the half of a logout that matters: the access token is still on disk, so
// the next command logs straight back in. A caller that only prints a warning
// must not print it for this.
var ErrSessionFileRemains = errors.New("the session file could not be deleted")

// clearSessionFile deletes the session file. A deletion that fails is wrapped
// in ErrSessionFileRemains, which is what tells this half of a logout from the
// server close.
func clearSessionFile() (bool, error) {
	cleared, err := session.Clear()
	if err != nil {
		err = fmt.Errorf("%w: %w", ErrSessionFileRemains, err)
	}
	return cleared, err
}

// ClearSession is the local half of Logout: it deletes the session file and
// returns the access token the file held, or "" when there was none, without
// closing that session on the server. A caller with an event loop to keep
// alive clears here, so that the token is off the disk before it returns, and
// closes the returned token itself with CloseSession, where it can wait. An
// error is always the file's and wraps ErrSessionFileRemains; the token is
// returned with it, because closing a session whose file could not be deleted
// is still worth doing: the token left on disk is then a dead one.
func ClearSession() (token string, cleared bool, err error) {
	if old, loadErr := session.Load(); loadErr == nil {
		token = old.AccessToken
	}
	cleared, err = clearSessionFile()
	return token, cleared, err
}

// Logout closes the current session on the server (best effort) and deletes
// the local session file. It reports whether a local session existed, and an
// error that joins both halves: the server close, which is allowed to fail
// because the token expires on its own, and the file deletion, which is not.
// Use errors.Is(err, ErrSessionFileRemains) to tell them apart.
//
// The deletion error used to be dropped, so `tuta-alias logout` printed "No
// session to clear." and exited 0 with the token still there.
func Logout() (bool, error) {
	var closeErr error
	if old, err := session.Load(); err == nil && old.AccessToken != "" {
		closeErr = closeSession(old.AccessToken)
	}
	cleared, clearErr := clearSessionFile()
	return cleared, errors.Join(closeErr, clearErr)
}
