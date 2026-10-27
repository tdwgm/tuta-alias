// Package login ports tuta_alias/login.py — the salt → KDF → authVerifier →
// SessionService → optional TOTP → resolveAccountIDs flow.
package login

import (
	"errors"
	"fmt"
	"time"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/crypto"
	"github.com/tdwgm/tuta-alias/internal/session"
)

// ErrTOTPRequired is returned when the server requires a second factor and no
// TOTP code or callback was provided.
var ErrTOTPRequired = errors.New("account has 2FA enabled; supply --totp")

// TOTPCallback is invoked to obtain a TOTP code interactively.
type TOTPCallback func() (int, error)

// Login performs the full login flow: salt → Argon2id KDF → authVerifier →
// SessionService → optional TOTP → resolveAccountIDs.
// Mirrors tuta_alias/login.py::login.
func Login(c *api.Client, mailAddress, passphrase string, totpCode int, totpCB TOTPCallback) (*session.Session, error) {
	addr := normalize(mailAddress)
	salt, kdfVersion, err := fetchSalt(c, addr)
	if err != nil {
		return nil, err
	}
	key, err := crypto.DeriveUserPassphraseKey(passphrase, salt, kdfVersion)
	if err != nil {
		return nil, err
	}
	verifier := crypto.AuthVerifierB64URL(key)
	cs, err := createSession(c, addr, verifier)
	if err != nil {
		return nil, err
	}
	accessToken, _ := cs["accessToken"].(string)
	userID, _ := cs["user"].(string)
	challenges, _ := cs["challenges"].([]any)
	c.AccessToken = accessToken

	if len(challenges) > 0 {
		if totpCode == 0 && totpCB != nil {
			n, err := totpCB()
			if err != nil {
				return nil, err
			}
			totpCode = n
		}
		if totpCode == 0 {
			return nil, ErrTOTPRequired
		}
		listID, elemID, err := crypto.ParseSessionID(accessToken)
		if err != nil {
			return nil, err
		}
		_, err = c.ServiceRequest("POST", consts.SysApp, "SecondFactorAuthService",
			"SecondFactorAuthData", map[string]any{
				"_format": 0,
				"type":    consts.SecondFactorTypeTOTP,
				"otpCode": totpCode,
				"session": []string{listID, elemID},
			}, "")
		if err != nil {
			return nil, err
		}
		if err := waitForSecondFactor(c, accessToken, 60*time.Second, time.Second); err != nil {
			return nil, err
		}
	}

	mailGroupID, userGroupID, groupInfoID, customerID, customerInfoID, err := resolveAccountIDs(c, userID)
	if err != nil {
		return nil, err
	}

	return &session.Session{
		MailAddress:    addr,
		UserID:         userID,
		AccessToken:    accessToken,
		UserGroupID:    userGroupID,
		GroupInfoID:    groupInfoID,
		MailGroupID:    mailGroupID,
		CustomerID:     customerID,
		CustomerInfoID: customerInfoID,
		SavedAt:        time.Now().UTC().Format(time.RFC3339),
		Version:        1,
	}, nil
}

func normalize(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out = append(out, c)
	}
	return string(out)
}

func fetchSalt(c *api.Client, addr string) ([]byte, int, error) {
	out, err := c.ServiceRequest("GET", consts.SysApp, "SaltService", "SaltData",
		map[string]any{"_format": 0, "mailAddress": addr}, "SaltReturn")
	if err != nil {
		return nil, 0, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("salt response: %T", out)
	}
	salt, _ := m["salt"].([]byte)
	kdf, _ := m["kdfVersion"].(int)
	return salt, kdf, nil
}

func createSession(c *api.Client, addr, verifier string) (map[string]any, error) {
	accessKey, err := newAccessKey()
	if err != nil {
		return nil, err
	}
	out, err := c.ServiceRequest("POST", consts.SysApp, "SessionService",
		"CreateSessionData", map[string]any{
			"_format":          0,
			"mailAddress":      addr,
			"authVerifier":     verifier,
			"clientIdentifier": consts.ClientIdentifier,
			"accessKey":        accessKey, // persistent session, see persist.go
		}, "CreateSessionReturn")
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	return m, nil
}

func waitForSecondFactor(c *api.Client, accessToken string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		out, err := c.ServiceRequest("GET", consts.SysApp, "SecondFactorAuthService",
			"SecondFactorAuthGetData", map[string]any{
				"_format":     0,
				"accessToken": accessToken,
			}, "SecondFactorAuthGetReturn")
		if err != nil {
			return fmt.Errorf("second-factor poll failed: %w", err)
		}
		m, _ := out.(map[string]any)
		pending, ok := m["secondFactorPending"].(bool)
		if !ok {
			// Only a reply that says the approval is no longer pending ends the
			// wait. Tuta's type model makes the flag required, so a reply
			// without it (an empty body, an object that lacks it, a null) means
			// the protocol changed or this is not a Tuta server, and neither
			// shows that the user approved on their phone. It fails at once
			// instead of polling again, because a Tuta server never sends such
			// a reply and waiting would only delay the error.
			return errors.New("second-factor poll failed: the reply has no secondFactorPending field, so the approval cannot be confirmed")
		}
		if !pending {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("second-factor approval timed out")
		}
		time.Sleep(poll)
	}
}

func resolveAccountIDs(c *api.Client, userID string) (mailGroupID, userGroupID string,
	groupInfoID []string, customerID string, customerInfoID []string, err error) {

	user, err := c.EntityLoad(consts.SysApp, "User", "User", userID)
	if err != nil {
		return "", "", nil, "", nil, err
	}

	ug, _ := user["userGroup"].([]map[string]any)
	if len(ug) == 0 {
		return "", "", nil, "", nil, errors.New("user.userGroup empty")
	}
	userGroupID, _ = ug[0]["group"].(string)
	if gi, ok := ug[0]["groupInfo"].([]string); ok && len(gi) == 2 {
		groupInfoID = gi
	}

	mailGroupID = userGroupID // fallback (Python emits a warning)
	if ms, ok := user["memberships"].([]map[string]any); ok {
		for _, m := range ms {
			if gt, _ := m["groupType"].(int); gt == consts.GroupTypeMail {
				if g, ok := m["group"].(string); ok {
					mailGroupID = g
				}
				break
			}
		}
	}

	if cid, ok := user["customer"].(string); ok && cid != "" {
		customerID = cid
		cust, err := c.EntityLoad(consts.SysApp, "Customer", "Customer", cid)
		if err == nil {
			if ci, ok := cust["customerInfo"].([]string); ok && len(ci) == 2 {
				customerInfoID = ci
			}
		}
	}
	return
}
