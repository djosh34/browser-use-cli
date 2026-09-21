package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func TestReadDistinguishesSelectorRacesFromInvalidInput(t *testing.T) {
	for _, mode := range []string{"stale selector node", "unavailable frame"} {
		t.Run(mode, func(t *testing.T) {
			c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
				for {
					r, err := receive(ctx, conn)
					if err != nil {
						return
					}
					protocolError := func() {
						conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"sessionId":%q,"error":{"code":-32000,"message":"Node or target is gone"}}`, r.ID, r.Session)))
					}
					switch r.Method {
					case "Target.getTargets":
						reply(ctx, conn, r, pagesReply("Page"))
					case "Target.attachToTarget":
						var params struct {
							ID string `json:"targetId"`
						}
						json.Unmarshal(r.Params, &params)
						if params.ID == "missing" {
							protocolError()
						} else {
							reply(ctx, conn, r, map[string]string{"sessionId": "session"})
						}
					case "Page.enable", "Page.setLifecycleEventsEnabled":
						reply(ctx, conn, r, map[string]any{})
					case "Page.getFrameTree":
						tree := map[string]any{"frame": map[string]string{"id": "one", "loaderId": "original", "url": "https://example.org/"}}
						if mode == "unavailable frame" {
							tree["childFrames"] = []any{map[string]any{"frame": map[string]string{"id": "missing", "parentId": "one", "loaderId": "child", "url": "https://other.example/"}}}
						}
						reply(ctx, conn, r, map[string]any{"frameTree": tree})
					case "DOMSnapshot.getSnapshot":
						reply(ctx, conn, r, map[string]any{"domNodes": []any{map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one", "documentURL": "https://example.org/"}}})
					case "DOM.getDocument":
						reply(ctx, conn, r, map[string]any{"root": map[string]int{"nodeType": 9, "nodeId": 1, "backendNodeId": 1}})
					case "DOM.querySelectorAll":
						if mode == "stale selector node" {
							protocolError()
						} else {
							reply(ctx, conn, r, map[string]any{"nodeIds": []int{}})
						}
					case "Page.createIsolatedWorld":
						reply(ctx, conn, r, map[string]int{"executionContextId": 1})
					case "Runtime.evaluate":
						reply(ctx, conn, r, map[string]any{"result": map[string]any{"type": "boolean", "value": true}})
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
			_, err = p.Read(testContext(t), cdp.ReadOptions{Selector: ".valid"})
			var e *cdp.Error
			if !errors.As(err, &e) || e.Code != "unavailable" {
				t.Fatalf("valid selector race: %v", err)
			}
		})
	}
}

func TestControlsRejectDocumentChangeDuringCollection(t *testing.T) {
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		loader := "original"
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getTargets":
				reply(ctx, conn, r, pagesReply("Original"))
			case "Target.attachToTarget":
				reply(ctx, conn, r, map[string]string{"sessionId": "session"})
			case "Page.enable", "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.getFrameTree":
				reply(ctx, conn, r, map[string]any{"frameTree": map[string]any{"frame": map[string]string{"id": "one", "loaderId": loader, "url": "https://example.org/"}}})
			case "DOMSnapshot.getSnapshot":
				reply(ctx, conn, r, map[string]any{"domNodes": []any{map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one", "documentURL": "https://example.org/"}}})
			case "Accessibility.getFullAXTree":
				loader = "replacement" // Simulate an independent navigation during capture.
				reply(ctx, conn, r, map[string]any{"nodes": []any{}})
			case "Page.createIsolatedWorld":
				reply(ctx, conn, r, map[string]int{"executionContextId": 1})
			case "Runtime.evaluate":
				reply(ctx, conn, r, map[string]any{"result": map[string]any{"type": "object", "value": map[string]int{"x": 0, "y": 0, "width": 800, "height": 600}}})
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
	_, err = p.Controls(testContext(t), cdp.ControlsOptions{})
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "unavailable" {
		t.Fatalf("mixed-document collection succeeded: %v", err)
	}
}
