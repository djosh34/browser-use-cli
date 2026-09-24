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
	p := navigationPeer(t, func(ctx context.Context, conn *websocket.Conn, r request) {
		for range 300 {
			for _, method := range []string{"Page.frameNavigated", "Page.frameStartedLoading", "Page.frameStoppedLoading"} {
				event(ctx, conn, "session", method, map[string]string{"frameId": "child"})
			}
		}
		event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "old", "name": "load"})
		event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "new", "name": "load"})
		reply(ctx, conn, r, map[string]string{"frameId": "one", "loaderId": "new"})
	})
	if err := p.Navigate(testContext(t), "https://example.org/new"); err != nil {
		t.Fatal(err)
	}
	info, err := p.Info(testContext(t))
	if err != nil || info.ID != 1 {
		t.Fatalf("info: %+v %v", info, err)
	}
}

func TestNavigationDoesNotAcceptUnrelatedLoad(t *testing.T) {
	navigated := make(chan struct{}, 1)
	release := make(chan struct{})
	p := navigationPeer(t, func(ctx context.Context, conn *websocket.Conn, r request) {
		event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "one", "loaderId": "old", "name": "load"})
		event(ctx, conn, "session", "Page.lifecycleEvent", map[string]string{"frameId": "child", "loaderId": "new", "name": "load"})
		reply(ctx, conn, r, map[string]string{"frameId": "one", "loaderId": "new"})
		navigated <- struct{}{}
		<-release
	})
	defer close(release)
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { done <- p.Navigate(ctx, "https://example.org/") }()
	<-navigated
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong document satisfied wait: %v", err)
	}
}
