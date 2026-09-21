package cdp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func navigationPeer(t *testing.T, navigate func(context.Context, *websocket.Conn, request)) *cdp.Page {
	t.Helper()
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getTargets":
				reply(ctx, conn, r, pagesReply("Old"))
			case "Target.getTargetInfo":
				reply(ctx, conn, r, map[string]any{"targetInfo": map[string]any{"targetId": "one", "type": "page", "url": "about:blank"}})
			case "Target.attachToTarget":
				reply(ctx, conn, r, map[string]string{"sessionId": "session"})
			case "Page.enable", "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.navigate":
				navigate(ctx, conn, r)
			default:
				t.Errorf("unexpected method: %s", r.Method)
				return
			}
		}
	})
	p, err := c.Page(testContext(t), "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreviouslySelectedPageDoesNotRetargetAfterClosure(t *testing.T) {
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		first := true
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			if r.Method == "Target.getTargets" {
				if first {
					reply(ctx, conn, r, pagesReply("Original"))
					first = false
				} else {
					reply(ctx, conn, r, map[string]any{"targetInfos": []any{}})
				}
			} else {
				conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"error":{"code":-32602,"message":"No target"}}`, r.ID)))
			}
		}
	})
	p, err := c.Page(testContext(t), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Info(testContext(t))
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "page" {
		t.Fatalf("closed selected page: %v", err)
	}
}

func TestMissingSessionErrorDoesNotPoisonConnection(t *testing.T) {
	p := navigationPeer(t, func(ctx context.Context, conn *websocket.Conn, r request) {
		// Chrome answers this browser-level routing error without a sessionId.
		conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"error":{"code":-32001,"message":"Session with given id not found."}}`, r.ID)))
	})
	err := p.Navigate(testContext(t), "https://example.org/")
	var typed *cdp.Error
	if !errors.As(err, &typed) || typed.Code != "protocol" {
		t.Fatalf("missing session: %v", err)
	}
	if _, err := p.Info(testContext(t)); err != nil {
		t.Fatalf("session routing error poisoned independent browser requests: %v", err)
	}
}

func TestNavigationInterruptedByPeerEvents(t *testing.T) {
	for _, code := range []string{"page", "overflow"} {
		t.Run(code, func(t *testing.T) {
			p := navigationPeer(t, func(ctx context.Context, conn *websocket.Conn, r request) {
				if code == "page" {
					event(ctx, conn, "", "Target.detachedFromTarget", map[string]string{"sessionId": "session"})
				} else {
					for range 257 {
						event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "old", "name": "load"})
					}
				}
				// Deliberately no navigation reply. A relevant failure must interrupt it.
			})
			ctx, cancel := context.WithTimeout(testContext(t), 300*time.Millisecond)
			defer cancel()
			err := p.Navigate(ctx, "https://example.org/")
			var e *cdp.Error
			if !errors.As(err, &e) || e.Code != code {
				t.Fatalf("event interruption: %v", err)
			}
		})
	}
}
