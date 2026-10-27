// sessionclose.go closes a session on the server without holding up the
// screen. A logout and a re-login each end a session by sending Tuta a DELETE.
// The API client gives every request 30 seconds and sends up to three when
// Tuta answers 474, so one close can run for about a minute and a half, and
// made inside Update it froze the whole interface for that long: no frame, no
// key, not even the spinner. On a dropped network that looks like a hang.
//
// The split follows what the screen that comes next depends on. The session
// FILE is written or deleted before Update returns: it is local, a failed
// write keeps the user on the login form, and a logout is only a logout once
// the token is off the disk. login.ReplaceSession and login.ClearSession do
// that and hand back the token they displaced. The server's half is a
// command, and the flash says so until its result lands.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tdwgm/tuta-alias/internal/login"
	"github.com/tdwgm/tuta-alias/internal/logx"
)

// closeSession is the one network call a logout or a re-login makes. It is a
// var so the tests can answer without a network, and so they can tell "a
// command was dispatched" from "the network was hit on the UI loop"; the
// default is what `tuta-alias logout` sends.
var closeSession = login.CloseSession

// closingNote is the flash text for a close that is on its way. It is a
// constant because sessionClosed takes it out again when the result lands: a
// flash that nothing clears is a lie about what the program is doing.
const closingNote = "closing the previous session on the server…"

// sessionClosedMsg carries the outcome of closing a session on the server. By
// the time it is sent the session is already gone from this machine, so an
// error is a warning about the server's copy and never a failed logout. The
// failure that must not be quiet, a session file that stayed
// (login.ErrSessionFileRemains), is reported when the file is cleared.
type sessionClosedMsg struct{ err error }

// closeSessionCmd closes the server-side session behind token. An empty token
// means there was nothing to close, and then there is no command.
func closeSessionCmd(token string) tea.Cmd {
	if token == "" {
		return nil
	}
	return func() tea.Msg { return sessionClosedMsg{err: closeSession(token)} }
}

// beginClose starts closing token and says so in the flash. The note goes at
// the very end, because sessionClosed finds it again by its place there:
// whatever the caller adds to the flash has to be added before this call.
func (m *model) beginClose(token string) tea.Cmd {
	cmd := closeSessionCmd(token)
	if cmd != nil {
		m.flash = joinFlash(m.flash, closingNote)
	}
	return cmd
}

// joinFlash puts tail after base in the " · " style the flash already uses.
func joinFlash(base, tail string) string {
	if base == "" {
		return tail
	}
	return base + " · " + tail
}

// sessionClosed folds a finished close into the flash. The note comes out only
// while it is still the end of the flash: anything said since (a copy, an
// alias created) is newer than it and stays. A failure is added whatever the
// flash says now, because it is the one thing here that must reach the user.
func (m model) sessionClosed(msg sessionClosedMsg) model {
	base := m.flash
	if base == closingNote {
		base = ""
	} else {
		base = strings.TrimSuffix(base, " · "+closingNote)
	}
	if msg.err == nil {
		logx.Infof("closed the previous session on the server")
		m.flash = base
		return m
	}
	logx.Errorf("close previous session: %v", msg.err)
	m.flash = joinFlash(base, "could not close the previous session on the server: "+msg.err.Error())
	return m
}
