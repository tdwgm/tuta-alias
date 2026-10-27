package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/typemodel"
)

// Client is a thin HTTP client for the Tuta Mail REST API.
// Mirrors tuta_alias/api.py::TutaClient.
type Client struct {
	BaseURL     string
	AccessToken string
	HTTP        *http.Client
}

// NewClient creates a Client with a 30-second timeout. Its requests go
// through TUTA_PROXY when SetProxy configured one (nil Transport = direct).
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:     baseURL,
		AccessToken: token,
		HTTP:        &http.Client{Timeout: 30 * time.Second, Transport: currentTransport()},
	}
}

func (c *Client) modelVersion(app string) (int, error) {
	if app == consts.SysApp {
		return consts.SysModelVersion, nil
	}
	return 0, fmt.Errorf("unknown app %q", app)
}

func (c *Client) headers(app, cv string, bodyPresent bool) (http.Header, error) {
	v, err := c.modelVersion(app)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("cv", cv)
	h.Set("v", strconv.Itoa(v))
	h.Set("Accept", "application/json")
	if c.AccessToken != "" {
		h.Set("accessToken", c.AccessToken)
	}
	if bodyPresent {
		h.Set("Content-Type", "application/json")
	}
	return h, nil
}

// ServiceRequest is the single entry-point for all HTTP verbs against a
// service endpoint. Packs reqData through the TypeModel wire format and
// unpacks the response. Supports GET, POST, and DELETE.
func (c *Client) ServiceRequest(method, app, serviceName, reqEntity string,
	reqData map[string]any, respEntity string) (any, error) {

	u := fmt.Sprintf("%s/rest/%s/%s", c.BaseURL, app, lower(serviceName))
	var payload map[string]any
	if reqEntity != "" && reqData != nil {
		p, err := typemodel.Pack(reqEntity, reqData)
		if err != nil {
			return nil, err
		}
		payload = p
	}
	bodyPresent := (method == "POST" || method == "PUT" || method == "DELETE") && payload != nil

	var buf []byte
	if payload != nil {
		buf, _ = json.Marshal(payload)
	}
	switch method {
	case "GET":
		if payload != nil {
			q := url.Values{}
			q.Set("_body", string(buf))
			u = u + "?" + q.Encode()
		}
	case "POST", "DELETE":
	default:
		return nil, fmt.Errorf("unsupported method %q", method)
	}
	build := func() (*http.Request, error) {
		var body io.Reader
		if method != "GET" && payload != nil {
			body = bytes.NewReader(buf)
		}
		return http.NewRequest(method, u, body)
	}
	return c.send(app, bodyPresent, build, respEntity)
}

// EntityLoad loads an entity (Element or ListElement) and unpacks via TypeModel.
// Pass a single id for Element types, or listID+elementID for ListElement types.
func (c *Client) EntityLoad(app, typeName, entityName string, ids ...string) (map[string]any, error) {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = url.PathEscape(id)
	}
	u := fmt.Sprintf("%s/rest/%s/%s/%s", c.BaseURL, app, lower(typeName), joinPath(parts))
	out, err := c.send(app, false, func() (*http.Request, error) {
		return http.NewRequest("GET", u, nil)
	}, "")
	if err != nil || out == nil {
		return nil, err
	}
	raw, ok := out.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected response shape: %T", out)
	}
	return typemodel.Unpack(entityName, raw)
}

// EntityDelete sends a DELETE to a list-element URL.
func (c *Client) EntityDelete(app, typeName, listID, elementID string) error {
	u := fmt.Sprintf("%s/rest/%s/%s/%s/%s", c.BaseURL, app, lower(typeName),
		url.PathEscape(listID), url.PathEscape(elementID))
	_, err := c.send(app, false, func() (*http.Request, error) {
		return http.NewRequest("DELETE", u, nil)
	}, "")
	return err
}

// send executes the request produced by build. When Tuta rejects the client
// version (HTTP 474) it retries once with a newer `cv`: the one another
// request already refreshed to, or one freshly read from the live web client.
// build runs again for the retry because a body can only be read once.
func (c *Client) send(app string, bodyPresent bool, build func() (*http.Request, error), respEntity string) (any, error) {
	out, sent, err := c.sendOnce(app, bodyPresent, build, respEntity)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != StatusInvalidSoftwareVersion {
		return out, err
	}
	if currentClientVersion() != sent {
		// A parallel request refreshed cv while this one was in flight.
		out, _, err = c.sendOnce(app, bodyPresent, build, respEntity)
		return out, err
	}
	fresh, rerr := c.refreshClientVersion(sent)
	if rerr != nil {
		ae.Message = appendDetail(ae.Message,
			fmt.Sprintf("Tuta rejected client version %s and the refresh failed: %v", sent, rerr))
		return nil, ae
	}
	if compareVersions(fresh, sent) <= 0 {
		ae.Message = appendDetail(ae.Message,
			fmt.Sprintf("Tuta rejected client version %s and %s serves %s, which is not newer — "+
				"the sys model version (v=%d) is probably stale", sent, c.BaseURL, fresh, consts.SysModelVersion))
		return nil, ae
	}
	out, _, err = c.sendOnce(app, bodyPresent, build, respEntity)
	return out, err
}

// appendDetail joins an explanation onto an APIError message, keeping the
// server's own detail (e.g. "error-id …") first.
func appendDetail(msg, detail string) string {
	if msg == "" {
		return detail
	}
	return msg + " — " + detail
}

// sendOnce performs one request and also returns the `cv` it sent, so a 474
// can be judged against the version actually rejected rather than the
// global one, which a parallel request may have changed meanwhile.
func (c *Client) sendOnce(app string, bodyPresent bool, build func() (*http.Request, error), respEntity string) (any, string, error) {
	cv := currentClientVersion()
	h, err := c.headers(app, cv, bodyPresent)
	if err != nil {
		return nil, cv, err
	}
	req, err := build()
	if err != nil {
		return nil, cv, err
	}
	req.Header = h
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var pe *ProxyError
		if errors.As(err, &pe) {
			// Readable on its own; drops the *url.Error's URL (incl. ?_body=).
			return nil, cv, pe
		}
		return nil, cv, redactURLError(err)
	}
	defer resp.Body.Close()
	out, err := c.handle(resp, respEntity)
	return out, cv, err
}

func (c *Client) handle(resp *http.Response, respEntity string) (any, error) {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	switch resp.StatusCode {
	case 200, 201, 204:
		if readErr != nil {
			// A body that stopped short is a failed request, not an empty one.
			// Callers read "no body" as "nothing to report": a DNS check came
			// back OK, a quota came back as zero and a second factor stopped
			// being pending, all on a response that never arrived. This also
			// surfaces Client.Timeout firing while the body is read.
			return nil, fmt.Errorf("read response: %w", readErr)
		}
		if len(body) == 0 {
			return nil, nil
		}
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		if respEntity == "" {
			return raw, nil
		}
		return typemodel.Unpack(respEntity, raw)
	default:
		// The status is the answer here. A body that stopped short only
		// shortens the message, and failing on it would hide the 474 that
		// send has to see.
		code := resp.Header.Get("Error")
		if code == "" {
			code = resp.Header.Get("error")
		}
		msg := redact(body, consts.ErrorBodyMaxBytes)
		if msg == "" && resp.Header.Get("error-id") != "" {
			// Tuta's server-side incident id — the only detail most errors carry.
			msg = "error-id " + resp.Header.Get("error-id")
		}
		return nil, &APIError{
			Status:  resp.StatusCode,
			Code:    code,
			Message: msg,
		}
	}
}

// maxResponseBytes bounds how much of a response body is read into memory.
const maxResponseBytes = 8 << 20

// redactURLError drops the query string from a transport error's URL: GET
// requests carry the packed body (incl. accessToken) in `?_body=`, and
// *url.Error prints the full URL.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if i := strings.IndexByte(ue.URL, '?'); i >= 0 {
			ue.URL = ue.URL[:i] + "?<redacted>"
		}
	}
	return err
}

func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}

func joinPath(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}
