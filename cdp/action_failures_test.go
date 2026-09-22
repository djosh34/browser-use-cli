package cdp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

func TestPressDoesNotWaitForPreexistingFrameNavigation(t *testing.T) {
	c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
		typed := false
		for {
			r, err := receive(ctx, conn)
			if err != nil {
				return
			}
			switch r.Method {
			case "Target.getTargets":
				title := "Before"
				if typed {
					title = "Typed"
				}
				reply(ctx, conn, r, pagesReply(title))
			case "Target.attachToTarget":
				reply(ctx, conn, r, map[string]string{"sessionId": "session"})
			case "Page.enable", "Page.setLifecycleEventsEnabled":
				reply(ctx, conn, r, map[string]any{})
			case "Page.getFrameTree":
				loader := "child-old"
				if typed {
					loader = "background"
				}
				reply(ctx, conn, r, map[string]any{"frameTree": map[string]any{"frame": map[string]string{"id": "one", "loaderId": "original", "url": "about:blank"}, "childFrames": []any{map[string]any{"frame": map[string]string{"id": "child", "parentId": "one", "loaderId": loader, "url": "https://other.example/"}}}}})
			case "DOMSnapshot.getSnapshot":
				reply(ctx, conn, r, map[string]any{"domNodes": []any{map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one", "documentURL": "about:blank", "childNodeIndexes": []int{1}}, map[string]any{"nodeType": 1, "nodeName": "IFRAME", "backendNodeId": 2, "frameId": "child", "layoutNodeIndex": 0, "contentDocumentIndex": 2}, map[string]any{"nodeType": 9, "backendNodeId": 3, "frameId": "child", "documentURL": "https://other.example/"}}, "layoutTreeNodes": []any{map[string]any{"domNodeIndex": 1, "boundingBox": map[string]int{"x": 10, "y": 10, "width": 100, "height": 100}}}})
			case "Page.bringToFront":
				// An independent iframe request starts before any keyboard input.
				conn.Write(ctx, websocket.MessageText, []byte(`{"method":"Page.frameStartedLoading","sessionId":"session","params":{"frameId":"child"}}`))
				reply(ctx, conn, r, map[string]any{})
			case "Input.dispatchKeyEvent":
				if !typed {
					typed = true
					conn.Write(ctx, websocket.MessageText, []byte(`{"method":"Page.frameNavigated","sessionId":"session","params":{"frame":{"id":"child","loaderId":"background"}}}`))
				}
				// The background document commits but its load intentionally never completes.
				reply(ctx, conn, r, map[string]any{})
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
	ctx, cancel := context.WithTimeout(testContext(t), 500*time.Millisecond)
	defer cancel()
	result, err := p.Press(ctx, "Enter")
	if err != nil || result.Page.Title != "Typed" {
		t.Fatalf("unrelated loading frame delayed input: %s %v", result, err)
	}
}

func TestPressRejectsIncompleteFrameObservation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprintf("unavailable_before_input=%t", before), func(t *testing.T) {
			input := make(chan struct{}, 1)
			c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
				missing := before
				for {
					r, err := receive(ctx, conn)
					if err != nil {
						return
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
							conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"id":%d,"sessionId":%q,"error":{"code":-32000,"message":"Frame unavailable"}}`, r.ID, r.Session)))
						} else {
							reply(ctx, conn, r, map[string]string{"sessionId": "session"})
						}
					case "Page.enable", "Page.setLifecycleEventsEnabled", "Page.bringToFront":
						reply(ctx, conn, r, map[string]any{})
					case "Page.getFrameTree":
						tree := map[string]any{"frame": map[string]string{"id": "one", "loaderId": "original", "url": "about:blank"}}
						if missing {
							tree["childFrames"] = []any{map[string]any{"frame": map[string]string{"id": "missing", "parentId": "one", "loaderId": "child", "url": "https://other.example/"}}}
						}
						reply(ctx, conn, r, map[string]any{"frameTree": tree})
					case "DOMSnapshot.getSnapshot":
						reply(ctx, conn, r, map[string]any{"domNodes": []any{map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one", "documentURL": "about:blank"}}})
					case "Page.createIsolatedWorld":
						reply(ctx, conn, r, map[string]int{"executionContextId": 1})
					case "Runtime.evaluate":
						reply(ctx, conn, r, map[string]any{"result": map[string]any{"type": "boolean", "value": true}})
					case "Input.dispatchKeyEvent":
						select {
						case input <- struct{}{}:
						default:
						}
						missing = true
						reply(ctx, conn, r, map[string]any{})
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
			_, err = p.Press(testContext(t), "Enter")
			var typed *cdp.Error
			if !errors.As(err, &typed) || typed.Code != "unavailable" {
				t.Fatalf("incomplete frame coverage reported success: %v", err)
			}
			select {
			case <-input:
				if before {
					t.Fatal("keyboard input sent with known incomplete coverage")
				}
			default:
				if !before {
					t.Fatal("fixture did not make frame unavailable after input")
				}
			}
		})
	}
}
