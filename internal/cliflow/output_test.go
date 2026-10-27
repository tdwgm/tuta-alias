package cliflow

import (
	"bytes"
	"errors"
	"testing"

	"github.com/tdwgm/tuta-alias/internal/api"
)

func TestBail_401_GivesSessionExpiredMessage(t *testing.T) {
	var buf bytes.Buffer
	code := bail(&buf, &api.APIError{Status: 401, Message: "x"})
	if code != 2 {
		t.Errorf("exit = %d", code)
	}
	if !bytes.Contains(buf.Bytes(), []byte("Session expired")) {
		t.Errorf("stderr = %q", buf.String())
	}
}

func TestBail_412_PreconditionFailedPrefix(t *testing.T) {
	var buf bytes.Buffer
	code := bail(&buf, &api.APIError{Status: 412, Code: "InvalidDataError", Message: "domain not verified"})
	if code != 2 || !bytes.Contains(buf.Bytes(), []byte("Precondition failed")) {
		t.Errorf("code=%d stderr=%q", code, buf.String())
	}
}

func TestBail_429_RateLimited(t *testing.T) {
	var buf bytes.Buffer
	code := bail(&buf, &api.APIError{Status: 429})
	if code != 2 || !bytes.Contains(buf.Bytes(), []byte("Rate limited")) {
		t.Errorf("code=%d stderr=%q", code, buf.String())
	}
}

func TestBail_Generic(t *testing.T) {
	var buf bytes.Buffer
	code := bail(&buf, errors.New("boom"))
	if code != 2 || !bytes.Contains(buf.Bytes(), []byte("boom")) {
		t.Errorf("code=%d stderr=%q", code, buf.String())
	}
}
