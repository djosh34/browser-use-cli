package cdp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func TestPageRecoversAfterCanceledInitialization(t *testing.T) {
	enabling := make(chan struct{})
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		first := true
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getTargets":
				reply(ctx, conn, r, pagesReply("Ready"))
			case "Target.getTargetInfo":
				reply(ctx, conn, r, map[string]any{"targetInfo": map[string]any{"targetId": "one", "type": "page", "url": "about:blank"}})
			case "Target.attachToTarget":
				reply(ctx, conn, r, map[string]string{"sessionId": "session"})
			case "Page.enable":
				if first {
					first = false
					close(enabling)
				} else {
					reply(ctx, conn, r, map[string]any{})
				}
			case "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.navigate":
				event(ctx, conn, r.Session, "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "new", "name": "load"})
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
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { done <- p.Navigate(ctx, "https://example.org/") }()
	<-enabling
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("initialization cancellation: %v", err)
	}
	again, err := c.Page(testContext(t), "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Navigate(testContext(t), "https://example.org/new"); err != nil {
		t.Fatalf("new caller operation on live page: %v", err)
	}
}

func TestPageSelectionDoesNotAccumulateDisappearedTabs(t *testing.T) {
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		for i := 0; ; i++ {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			reply(ctx, conn, r, map[string]any{"targetInfos": []any{map[string]any{"targetId": fmt.Sprintf("page-%d", i), "type": "page", "url": "about:blank"}}})
		}
	})
	for range 300 {
		if _, err := c.Page(testContext(t), ""); err != nil {
			t.Fatalf("select sole live page: %v", err)
		}
	}
}

func TestPageCanReattachAfterExplicitNewOperation(t *testing.T) {
	sessions, navigations := 0, 0
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getTargets":
				reply(ctx, conn, r, pagesReply("Ready"))
			case "Target.getTargetInfo":
				reply(ctx, conn, r, map[string]any{"targetInfo": map[string]any{"targetId": "one", "type": "page", "url": "about:blank"}})
			case "Target.attachToTarget":
				sessions++
				reply(ctx, conn, r, map[string]string{"sessionId": fmt.Sprintf("session-%d", sessions)})
			case "Page.enable", "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.navigate":
				navigations++
				if navigations == 1 {
					event(ctx, conn, "", "Target.detachedFromTarget", map[string]string{"sessionId": r.Session})
					continue
				}
				if navigations > 2 {
					t.Error("navigation was replayed")
				}
				event(ctx, conn, r.Session, "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "new", "name": "load"})
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
	var e *cdp.Error
	if err := p.Navigate(testContext(t), "https://example.org/"); !errors.As(err, &e) || e.Code != "page" {
		t.Fatalf("detached navigation: %v", err)
	}
	if err := p.Navigate(testContext(t), "https://example.org/new"); err != nil {
		t.Fatalf("new explicit navigation: %v", err)
	}
}
