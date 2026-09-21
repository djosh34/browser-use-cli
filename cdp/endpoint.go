package cdp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const discoveryLimit = 4 << 20

func endpointURL(ctx context.Context, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return "", failure("invalid_input", "invalid browser endpoint")
	}
	switch u.Scheme {
	case "ws", "wss":
		if strings.Contains(u.Path, "/devtools/page/") {
			return "", failure("invalid_input", "a browser-level endpoint is required, not a page endpoint")
		}
		return u.String(), nil
	case "http", "https":
	default:
		return "", failure("invalid_input", "endpoint must use HTTP(S) or WS(S)")
	}
	escapedPath := strings.TrimRight(u.EscapedPath(), "/") + "/json/version"
	u.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return "", failure("invalid_input", "invalid browser endpoint path")
	}
	u.RawPath = escapedPath
	client := http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !sameOrigin(req.URL, u) {
			return failure("connection", "discovery redirect refused")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", failure("invalid_input", "invalid browser endpoint")
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", failure("connection", "browser discovery failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", failure("connection", "browser discovery returned a non-200 status")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, discoveryLimit+1))
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", failure("connection", "cannot read browser discovery")
	}
	if len(data) > discoveryLimit {
		return "", failure("overflow", "browser discovery exceeds 4 MiB")
	}
	var info struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if json.Unmarshal(data, &info) != nil || info.WS == "" {
		return "", failure("protocol", "invalid browser discovery response")
	}
	ws, err := url.Parse(info.WS)
	if err != nil || ws.Host == "" || (ws.Scheme != "ws" && ws.Scheme != "wss") || ws.Fragment != "" {
		return "", failure("protocol", "invalid discovered WebSocket endpoint")
	}
	// Never rewrite an advertised authority or forward discovery credentials to it.
	equivalent := *ws
	if equivalent.Scheme == "ws" {
		equivalent.Scheme = "http"
	} else {
		equivalent.Scheme = "https"
	}
	if !sameOrigin(&equivalent, u) {
		return "", failure("connection", "discovered WebSocket has a different origin; supply its exact browser WS URL")
	}
	if ws.User == nil {
		ws.User = u.User
	}
	return endpointURL(ctx, ws.String())
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
