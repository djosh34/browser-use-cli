package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func connectPeer(t *testing.T, serve func(context.Context, *websocket.Conn)) *cdp.Client {
	t.Helper()
	s := peer(t, func(ctx context.Context, conn *websocket.Conn) {
		r, err := receive(ctx, conn)
		if err != nil {
			return
		}
		reply(ctx, conn, r, map[string]any{"browserContextIds": []string{}})
		r, err = receive(ctx, conn)
		if err != nil {
			return
		}
		reply(ctx, conn, r, map[string]any{}) // Target discovery initialization.
		serve(ctx, conn)
	})
	c, err := cdp.Connect(testContext(t), "ws"+strings.TrimPrefix(s.URL, "http")+"/browser?secret=hidden")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func pagesReply(title string) any {
	return map[string]any{"targetInfos": []any{map[string]any{"targetId": "one", "type": "page", "url": "about:blank", "title": title}}}
}

func TestOutOfOrderAndCanceledCalls(t *testing.T) {
	firstReceived := make(chan struct{})
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		first, err := receive(ctx, conn)
		if err != nil {
			return
		}
		close(firstReceived)
		second, err := receive(ctx, conn)
		if err != nil {
			return
		}
		reply(ctx, conn, second, pagesReply("second"))
		reply(ctx, conn, first, pagesReply("first"))
		canceled, err := receive(ctx, conn)
		if err != nil {
			return
		}
		next, err := receive(ctx, conn)
		if err != nil {
			return
		}
		reply(ctx, conn, canceled, pagesReply("late"))
		reply(ctx, conn, next, pagesReply("after cancellation"))
		receive(ctx, conn)
	})
	type outcome struct {
		r   cdp.PagesResult
		err error
	}
	first := make(chan outcome, 1)
	go func() { r, err := c.Pages(testContext(t)); first <- outcome{r, err} }()
	<-firstReceived
	second, err := c.Pages(testContext(t))
	if err != nil || second.Pages[0].Title != "second" {
		t.Fatalf("second: %+v %v", second, err)
	}
	f := <-first
	if f.err != nil || f.r.Pages[0].Title != "first" {
		t.Fatalf("first: %+v", f)
	}
	ctx, cancel := context.WithTimeout(testContext(t), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Pages(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	after, err := c.Pages(testContext(t))
	if err != nil || after.Pages[0].Title != "after cancellation" {
		t.Fatalf("after: %+v %v", after, err)
	}
}

func TestInFlightBoundAndCloseUnblocksCalls(t *testing.T) {
	received := make(chan struct{}, 64)
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			_, err := receive(ctx, conn)
			if err != nil {
				return
			}
			received <- struct{}{}
		}
	})
	results := make(chan error, 64)
	for range 64 {
		go func() { _, err := c.Pages(testContext(t)); results <- err }()
	}
	for range 64 {
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("calls not received")
		}
	}
	_, err := c.Pages(testContext(t))
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "overflow" {
		t.Fatalf("bound: %v", err)
	}
	c.Close()
	for range 64 {
		select {
		case err := <-results:
			if !errors.As(err, &e) || e.Code != "connection" {
				t.Errorf("close: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("close stranded call")
		}
	}
	c.Close()
}

func TestPeerFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct{ name, payload, code string }{
		{"malformed", "not JSON secret-token", "protocol"},
		{"both result and error", `{"id":%d,"result":{},"error":{"code":-1,"message":"secret-token"}}`, "protocol"},
		{"protocol error", `{"id":%d,"error":{"code":-32000,"message":"secret-token"}}`, "protocol"},
		{"invalid result", `{"id":%d,"result":"secret-token"}`, "protocol"},
		{"null result", `{"id":%d,"result":null}`, "protocol"},
		{"missing page list", `{"id":%d,"result":{}}`, "protocol"},
		{"wrong session", `{"id":%d,"sessionId":"other","result":{}}`, "protocol"},
		{"disconnect", "", "connection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
				r, err := receive(ctx, conn)
				if err != nil {
					return
				}
				if tc.payload != "" {
					payload := tc.payload
					if strings.Contains(payload, "%d") {
						payload = fmt.Sprintf(payload, r.ID)
					}
					conn.Write(ctx, websocket.MessageText, []byte(payload))
					receive(ctx, conn)
				}
			})
			_, err := c.Pages(testContext(t))
			var e *cdp.Error
			if !errors.As(err, &e) || e.Code != tc.code || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("failure: %v", err)
			}
		})
	}
}

func TestOversizeBrowserMessageFailsExplicitly(t *testing.T) {
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		if _, err := receive(ctx, conn); err != nil {
			return
		}
		w, err := conn.Writer(ctx, websocket.MessageText)
		if err != nil {
			return
		}
		defer w.Close()
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for range 65 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	_, err := c.Pages(testContext(t))
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "overflow" {
		t.Fatalf("message bound: %v", err)
	}
}

func TestEndpointSecurity(t *testing.T) {
	t.Run("no credentials in errors", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "user:password secret-path token", http.StatusForbidden)
		}))
		defer s.Close()
		for _, endpoint := range []string{
			strings.Replace(s.URL, "http://", "http://user:password@", 1) + "/secret-path?token=secret",
			strings.Replace(s.URL, "http://", "ws://user:password@", 1) + "/secret-path?token=secret",
			"https://user:password@%broken/secret-path?token=secret",
			"ws://user:password@localhost/devtools/page/secret-path?token=secret",
		} {
			c, err := cdp.Connect(testContext(t), endpoint)
			if c != nil {
				c.Close()
			}
			if err == nil {
				t.Fatal("expected failure")
			}
			for _, secret := range []string{"password", "secret-path", "token", "user:"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("leaked %q: %v", secret, err)
				}
			}
		}
	})
	t.Run("cross origin redirect", func(t *testing.T) {
		contacted := make(chan struct{}, 1)
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted <- struct{}{} }))
		defer other.Close()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
		defer s.Close()
		_, err := cdp.Connect(testContext(t), s.URL)
		if err == nil {
			t.Fatal("followed cross-origin redirect")
		}
		select {
		case <-contacted:
			t.Fatal("contacted other origin")
		default:
		}
	})
	t.Run("discovery bound", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat(" ", 4<<20) + "x")) }))
		defer s.Close()
		_, err := cdp.Connect(testContext(t), s.URL)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "overflow" {
			t.Fatalf("bound: %v", err)
		}
	})
	t.Run("direct route query and basic auth", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "user" || password != "password" || r.URL.RequestURI() != "/custom%2Froute?token=secret" {
				t.Errorf("direct route or auth not preserved")
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			req, err := receive(r.Context(), conn)
			if err == nil {
				reply(r.Context(), conn, req, json.RawMessage(`{"browserContextIds":[]}`))
			}
			req, err = receive(r.Context(), conn)
			if err == nil {
				reply(r.Context(), conn, req, map[string]any{})
			}
			receive(r.Context(), conn)
		}))
		defer s.Close()
		c, err := cdp.Connect(testContext(t), strings.Replace(s.URL, "http://", "ws://user:password@", 1)+"/custom%2Froute?token=secret")
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	})
}
