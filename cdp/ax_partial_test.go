//go:build integration

package cdp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/djosh34/browser-use-cli/cdp"
)

// A transport fault injector fronts real Chrome. It changes no captured AX
// payload and tests only the public library's treatment of an unavailable CDP
// operation, rather than replacing discovery with a mock implementation.
func axFaultEndpoint(t *testing.T, endpoint string, intercept func(string, json.RawMessage) bool, after ...func(string)) string {
	t.Helper()
	response, err := http.Get(endpoint + "/json/version")
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	err = json.NewDecoder(response.Body).Decode(&version)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		upstream, _, err := websocket.Dial(ctx, version.URL, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		upstream.SetReadLimit(64 << 20)
		downstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer downstream.CloseNow()
		downstream.SetReadLimit(64 << 20)
		var pending sync.Map
		go func() {
			defer cancel()
			for {
				kind, data, err := upstream.Read(ctx)
				if err != nil {
					return
				}
				var response struct {
					ID int64 `json:"id"`
				}
				_ = json.Unmarshal(data, &response)
				if method, ok := pending.LoadAndDelete(response.ID); ok {
					for _, hook := range after {
						hook(method.(string))
					}
				}
				if downstream.Write(ctx, kind, data) != nil {
					return
				}
			}
		}()
		for {
			kind, data, err := downstream.Read(ctx)
			if err != nil {
				return
			}
			var request struct {
				ID      int64           `json:"id"`
				Method  string          `json:"method"`
				Session string          `json:"sessionId"`
				Params  json.RawMessage `json:"params"`
			}
			if json.Unmarshal(data, &request) != nil {
				return
			}
			if intercept(request.Method, request.Params) {
				message, _ := json.Marshal(map[string]any{"id": request.ID, "sessionId": request.Session, "error": map[string]any{"code": -32000, "message": "Injected document unavailability"}})
				if downstream.Write(ctx, websocket.MessageText, message) != nil {
					return
				}
				continue
			}
			pending.Store(request.ID, request.Method)
			if upstream.Write(ctx, kind, data) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestAXPartialChildFailureIsProminentAndMainFailureErrors(t *testing.T) {
	fixture := observationFixture(t)
	endpoint := chrome(t, "--window-size=1440,1000", "--ozone-override-screen-size=1440,1000", "--force-device-scale-factor=1", "about:blank")
	owner := browserClient(t, endpoint)
	if _, err := owner.Open(testContext(t), fixture.URL); err != nil {
		t.Fatal(err)
	}
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			calls := 0
			c := browserClient(t, axFaultEndpoint(t, endpoint, func(method string, _ json.RawMessage) bool {
				if method != "Accessibility.getFullAXTree" {
					return false
				}
				calls++
				return calls == failAt
			}))
			p, err := c.Page(testContext(t), 0)
			if err != nil {
				t.Fatal(err)
			}
			r, err := p.Read(testContext(t), cdp.ReadOptions{})
			if failAt == 1 {
				if err == nil {
					t.Fatal("main AX failure returned successful observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Complete || len(r.Warnings) == 0 || !strings.Contains(r.String(), "Warning: incomplete") {
				t.Fatalf("partial advertised complete: %s", r)
			}
			if axNamed(t, r, "Root button").ID != 1 {
				t.Fatal("available main content lost")
			}
		})
	}
}

func TestAXSelectorCannotCombineReplacementDocumentWithOldTree(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<title>Original</title><button>Original</button>`)
	}))
	defer fixture.Close()
	endpoint := chrome(t, "--window-size=1440,1000", "--ozone-override-screen-size=1440,1000", "about:blank")
	owner := browserClient(t, endpoint)
	other, err := owner.Open(testContext(t), fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	once := false
	c := browserClient(t, axFaultEndpoint(t, endpoint, func(string, json.RawMessage) bool { return false }, func(method string) {
		if method == "DOM.querySelectorAll" && !once {
			once = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := other.Navigate(ctx, fixture.URL+"/replacement"); err != nil {
				t.Error(err)
			}
		}
	}))
	p, err := c.Page(testContext(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Read(testContext(t), cdp.ReadOptions{Selector: "button"})
	if err == nil {
		t.Fatal("returned AX from the old document with replacement selector identities")
	}
}
