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
					case "DOMSnapshot.getSnapshot":
						reply(ctx, conn, r, map[string]any{"domNodes": []any{map[string]any{"nodeType": 9, "backendNodeId": 1, "frameId": "one", "documentURL": "about:blank"}}})
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
									// Pace each event through a WebSocket round trip: this is
									// cumulative lifecycle growth, not a burst overflowing the queue.
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
			p, err := c.Page(testContext(t), "")
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
