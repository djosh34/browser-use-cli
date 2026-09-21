package cdp_test

import (
	"context"
	"errors"
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
