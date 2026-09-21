package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/coder/websocket"
)

func event(ctx context.Context, conn *websocket.Conn, session, method string, params any) {
	b, _ := json.Marshal(map[string]any{"sessionId": session, "method": method, "params": params})
	conn.Write(ctx, websocket.MessageText, b)
}

func TestNavigationObservesLoadBeforeCommandReply(t *testing.T) {
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
				reply(ctx, conn, r, map[string]any{"targetInfo": map[string]any{"targetId": "one", "type": "page", "url": "about:blank", "title": "Old"}})
			case "Target.attachToTarget":
				reply(ctx, conn, r, map[string]string{"sessionId": "session"})
			case "Page.enable", "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.navigate":
				event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "old", "name": "load"})
				event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "new", "name": "load"})
				reply(ctx, conn, r, map[string]string{"frameId": "one", "loaderId": "new"})
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
	if err := p.Navigate(testContext(t), "https://example.org/new"); err != nil {
		t.Fatal(err)
	}
	info, err := p.Info(testContext(t))
	if err != nil || info.ID != "one" {
		t.Fatalf("info: %+v %v", info, err)
	}
	if _, err := c.Page(testContext(t), "missing"); err == nil {
		t.Fatal("stale explicit page selected another tab")
	}
}

func TestNavigationDoesNotAcceptUnrelatedLoad(t *testing.T) {
	navigated := make(chan struct{}, 1)
	release := make(chan struct{})
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
				event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "old", "name": "load"})
				event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "child", "loaderId": "new", "name": "load"})
				reply(ctx, conn, r, map[string]string{"frameId": "one", "loaderId": "new"})
				navigated <- struct{}{}
				<-release
			default:
				t.Errorf("unexpected method: %s", r.Method)
				return
			}
		}
	})
	defer close(release)
	p, err := c.Page(testContext(t), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { done <- p.Navigate(ctx, "https://example.org/") }()
	<-navigated
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong document satisfied wait: %v", err)
	}
}
