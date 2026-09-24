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

// A hostile protocol peer pins the connection/resource bound, which cannot be
// generated reliably by ordinary page JavaScript. Input behavior is covered by
// the real-Chrome tests in actions_test.go.
func TestInputBoundsSerialLifecycleEvents(t *testing.T) {
	for _, unique := range []bool{true, false} {
		t.Run(fmt.Sprintf("distinct_frames=%t", unique), func(t *testing.T) {
			publisherDone := make(chan struct{})
			c := connectPeer(t, func(ctx context.Context, conn *websocket.Conn) {
				started := false
				for {
					r, err := receive(ctx, conn)
					if err != nil {
						return
					}
					switch r.Method {
					case "Target.getTargets":
						reply(ctx, conn, r, pagesReply("Page"))
					case "Target.attachToTarget":
						reply(ctx, conn, r, map[string]string{"sessionId": "session"})
					case "Page.enable", "Page.setLifecycleEventsEnabled", "Page.bringToFront":
						reply(ctx, conn, r, map[string]any{})
					case "Page.getFrameTree":
						reply(ctx, conn, r, map[string]any{"frameTree": map[string]any{"frame": map[string]string{"id": "one", "loaderId": "original", "url": "about:blank"}}})
					case "DOM.getDocument":
						reply(ctx, conn, r, map[string]any{"root": map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one"}})
					case "Page.createIsolatedWorld":
						reply(ctx, conn, r, map[string]int{"executionContextId": 1})
					case "Runtime.evaluate":
						reply(ctx, conn, r, map[string]any{"result": map[string]any{"type": "boolean", "value": true}})
					case "Input.dispatchKeyEvent":
						reply(ctx, conn, r, map[string]any{})
						var params struct {
							Type string `json:"type"`
						}
						json.Unmarshal(r.Params, &params)
						if params.Type == "keyUp" && !started {
							started = true
							go func() {
								defer close(publisherDone)
								for i := 0; i < 10000; i++ {
									frame := "churn"
									if unique {
										frame = fmt.Sprintf("churn-%d", i)
									}
									if err := conn.Write(ctx, websocket.MessageText, []byte(fmt.Sprintf(`{"method":"Page.frameStartedLoading","sessionId":"session","params":{"frameId":%q}}`, frame))); err != nil {
										return
									}
									// Pace cumulative growth rather than merely overflowing one queue.
									if err := conn.Ping(ctx); err != nil {
										return
									}
								}
							}()
						}
					default:
						t.Errorf("unexpected method: %s", r.Method)
						return
					}
				}
			})
			p, err := c.Page(testContext(t), 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Press(testContext(t), "Enter")
			c.Close()
			select {
			case <-publisherDone:
			case <-testContext(t).Done():
				t.Fatal("event publisher did not stop")
			}
			var typed *cdp.Error
			if !errors.As(err, &typed) || typed.Code != "overflow" {
				t.Fatalf("serial lifecycle stream was not bounded: %v", err)
			}
		})
	}
}
