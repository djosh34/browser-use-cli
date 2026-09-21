// Package cdp implements the disposable browser-cli prototype.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Client is sequential. Unrelated events are discarded by this prototype.
type Client struct {
	ws *websocket.Conn
	id int
}

type PageInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (p PageInfo) String() string { return fmt.Sprintf("%s  %s\n%s", p.ID, p.Title, p.URL) }

type Page struct {
	client      *Client
	id, session string
}

func Connect(ctx context.Context, endpoint string) (*Client, error) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(endpoint, "/")+"/json/version", nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
			return nil, err
		}
		endpoint = version.WebSocketDebuggerURL
	}
	ws, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(32 << 20)
	return &Client{ws: ws}, nil
}

func (c *Client) Close() error { return c.ws.CloseNow() }

func (c *Client) call(ctx context.Context, session, method string, params, result any) error {
	c.id++
	msg, err := json.Marshal(map[string]any{"id": c.id, "sessionId": session, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
		return err
	}
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return err
		}
		if response.ID != c.id {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("%s: %s (%d)", method, response.Error.Message, response.Error.Code)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
}

func (c *Client) Pages(ctx context.Context) ([]PageInfo, error) {
	var response struct {
		Targets []struct {
			ID    string `json:"targetId"`
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", map[string]any{}, &response); err != nil {
		return nil, err
	}
	pages := []PageInfo{}
	for _, t := range response.Targets {
		if t.Type == "page" {
			pages = append(pages, PageInfo{t.ID, t.URL, t.Title})
		}
	}
	return pages, nil
}

func (c *Client) Page(ctx context.Context, id string) (*Page, error) {
	if id == "" {
		pages, err := c.Pages(ctx)
		if err != nil {
			return nil, err
		}
		if len(pages) != 1 {
			return nil, fmt.Errorf("found %d pages; use --page with an ID from pages", len(pages))
		}
		id = pages[0].ID
	}
	var attached struct {
		Session string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": id, "flatten": true}, &attached); err != nil {
		return nil, err
	}
	return &Page{c, id, attached.Session}, nil
}

func (c *Client) Open(ctx context.Context, url string) (*Page, error) {
	var created struct {
		ID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank"}, &created); err != nil {
		return nil, err
	}
	p, err := c.Page(ctx, created.ID)
	if err != nil {
		return nil, err
	}
	return p, p.Navigate(ctx, url)
}

func (p *Page) call(ctx context.Context, method string, params, result any) error {
	return p.client.call(ctx, p.session, method, params, result)
}

func (p *Page) Navigate(ctx context.Context, url string) error {
	if err := p.call(ctx, "Page.enable", map[string]any{}, nil); err != nil {
		return err
	}
	var r struct {
		Error string `json:"errorText"`
	}
	if err := p.call(ctx, "Page.navigate", map[string]any{"url": url}, &r); err != nil {
		return err
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	for {
		var loaded bool
		err := p.Eval(ctx, `document.readyState !== 'loading' && location.href !== 'about:blank'`, &loaded)
		if err == nil && loaded {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func runtimeValue(data json.RawMessage, result any) error {
	var r struct {
		Result struct {
			Value          json.RawMessage `json:"value"`
			Description    string          `json:"description"`
			Unserializable string          `json:"unserializableValue"`
			Type           string          `json:"type"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if len(r.Exception) > 0 {
		return fmt.Errorf("JavaScript exception: %s", r.Exception)
	}
	if r.Result.Unserializable != "" {
		return fmt.Errorf("non-JSON JavaScript value: %s", r.Result.Unserializable)
	}
	if result == nil {
		return nil
	}
	if len(r.Result.Value) == 0 {
		return fmt.Errorf("JavaScript returned %s without a JSON value", r.Result.Type)
	}
	return json.Unmarshal(r.Result.Value, result)
}

func (p *Page) Eval(ctx context.Context, expression string, result any) error {
	var data json.RawMessage
	if err := p.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &data); err != nil {
		return err
	}
	return runtimeValue(data, result)
}

func (p *Page) invoke(ctx context.Context, object, function string, args []any, result any) error {
	arguments := []map[string]any{}
	for _, arg := range args {
		arguments = append(arguments, map[string]any{"value": arg})
	}
	var data json.RawMessage
	if err := p.call(ctx, "Runtime.callFunctionOn", map[string]any{"objectId": object, "functionDeclaration": function, "arguments": arguments, "returnByValue": true, "awaitPromise": true}, &data); err != nil {
		return err
	}
	return runtimeValue(data, result)
}

func (p *Page) document(ctx context.Context) (loader string, err error) {
	var r struct {
		Tree struct {
			Frame struct {
				Loader string `json:"loaderId"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	err = p.call(ctx, "Page.getFrameTree", map[string]any{}, &r)
	return r.Tree.Frame.Loader, err
}

func (p *Page) object(ctx context.Context, backend int) (string, error) {
	var r struct {
		Object struct {
			ID string `json:"objectId"`
		} `json:"object"`
	}
	err := p.call(ctx, "DOM.resolveNode", map[string]any{"backendNodeId": backend, "objectGroup": "prototype"}, &r)
	return r.Object.ID, err
}

type State struct {
	Page  PageInfo `json:"page"`
	Ready string   `json:"ready"`
	Focus string   `json:"focus"`
	Value string   `json:"value,omitempty"`
}

func (s State) String() string {
	return fmt.Sprintf("%s\nready=%s focus=%s value=%q", s.Page.String(), s.Ready, s.Focus, s.Value)
}

func (p *Page) State(ctx context.Context) (State, error) {
	var state State
	err := p.Eval(ctx, `(()=>{const e=document.activeElement;return {page:{url:location.href,title:document.title},ready:document.readyState,focus:e?.tagName||'',value:e?.type==='password'?'[redacted]':e?.value||''}})()`, &state)
	state.Page.ID = p.id
	return state, err
}
