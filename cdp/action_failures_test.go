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
