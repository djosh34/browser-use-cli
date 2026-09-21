package cdp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func TestDiscoveryPreservesEscapedBasePath(t *testing.T) {
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/browser" {
			if r.URL.RequestURI() != "/proxy%2Ftenant/json/version?token=secret" {
				t.Errorf("discovery changed base path: %s", r.URL.RequestURI())
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(s.URL, "http") + "/browser"})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		req, err := receive(r.Context(), conn)
		if err == nil {
			reply(r.Context(), conn, req, map[string]any{"browserContextIds": []string{}})
		}
		receive(r.Context(), conn)
	}))
	defer s.Close()
	c, err := cdp.Connect(testContext(t), s.URL+"/proxy%2Ftenant?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestDiscoveryDoesNotTrustUnverifiedTLS(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS reached HTTP handler") }))
	defer s.Close()
	if c, err := cdp.Connect(testContext(t), s.URL); err == nil {
		c.Close()
		t.Fatal("unverified TLS succeeded")
	}
}
