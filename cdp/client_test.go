package cdp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

type request struct {
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Session string          `json:"sessionId"`
}

// peer is an external protocol peer. Tests exercise only the exported library.
func peer(t *testing.T, serve func(context.Context, *websocket.Conn)) *httptest.Server {
	t.Helper()
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/json/version" {
			if r.URL.Query().Get("token") != "secret" {
				t.Error("discovery lost query")
			}
			json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(s.URL, "http") + "/devtools/browser/test?token=secret"})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		serve(r.Context(), conn)
	}))
	t.Cleanup(s.Close)
	return s
}

func receive(ctx context.Context, conn *websocket.Conn) (request, error) {
	var r request
	_, data, err := conn.Read(ctx)
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	return r, err
}
func reply(ctx context.Context, conn *websocket.Conn, r request, result any) error {
	b, _ := json.Marshal(map[string]any{"id": r.ID, "sessionId": r.Session, "result": result})
	return conn.Write(ctx, websocket.MessageText, b)
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDiscoveryAndCapturedPages(t *testing.T) {
	s := peer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getBrowserContexts":
				reply(ctx, conn, r, map[string]any{"browserContextIds": []string{}})
			case "Target.setDiscoverTargets":
				reply(ctx, conn, r, map[string]any{})
			case "Target.getTargets":
				reply(ctx, conn, r, map[string]any{"targetInfos": []any{
					map[string]any{"targetId": "b", "type": "page", "url": "about:blank", "title": "Blank"},
					map[string]any{"targetId": "a", "type": "page", "url": "https://example.org/", "title": "日本語"},
					map[string]any{"targetId": "internal", "type": "page", "url": "chrome://settings", "title": "Settings"},
					map[string]any{"targetId": "worker", "type": "service_worker", "url": "https://example.org/"},
				}})
			default:
				t.Errorf("unexpected method %s", r.Method)
				return
			}
		}
	})
	connectCtx, cancel := context.WithCancel(testContext(t))
	c, err := cdp.Connect(connectCtx, s.URL+"/proxy?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // Connection lifetime is not the lifetime of its establishment call.
	result, err := c.Pages(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Pages) != 2 || result.Pages[0].ID != 1 || result.Pages[1].ID != 2 {
		t.Fatalf("pages: %+v", result)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	text := result.String()
	if text != result.String() || !strings.Contains(text, "日本語") {
		t.Fatalf("rendering: %q", text)
	}
	data, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(data), `"id":1`) {
		t.Fatalf("JSON: %s %v", data, err)
	}
}
