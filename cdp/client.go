package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

const (
	messageLimit = 64 << 20
	pendingLimit = 64
)

type response struct {
	ID      int64           `json:"id"`
	Session string          `json:"sessionId"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}
type pendingCall struct {
	session string
	reply   chan response
}

// Client owns one browser connection. Its methods are safe for concurrent use.
// Calls on the same page are serialized; other clients are not globally locked.
type Client struct {
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	next    int64
	pending map[int64]pendingCall
	err     error
}

// Connect discovers or directly connects to a browser-level endpoint. ctx bounds
// establishment only. The returned connection lives until Close or failure.
func Connect(ctx context.Context, endpoint string) (*Client, error) {
	endpoint, err := endpointURL(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return failure("connection", "WebSocket redirect refused") },
	}})
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, failure("connection", "browser WebSocket connection failed")
	}
	conn.SetReadLimit(messageLimit)
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, ctx: lifetime, cancel: cancel, done: make(chan struct{}), pending: make(map[int64]pendingCall)}
	go c.read()
	// This browser-only command also rejects page-level connections behind proxies.
	var contexts struct {
		IDs []string `json:"browserContextIds"`
	}
	if err := c.call(ctx, "", "Target.getBrowserContexts", nil, &contexts); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Close releases the connection and waits for its reader. It sends no browser
// or tab close command. Repeated Close calls are harmless.
func (c *Client) Close() error {
	c.fail(failure("connection", "client is closed"))
	<-c.done
	return nil
}
func (c *Client) fail(err error) {
	c.mu.Lock()
	first := c.err == nil
	if first {
		c.err = err
		c.cancel()
	}
	c.mu.Unlock()
	if first {
		c.conn.CloseNow()
	}
}
func (c *Client) connectionError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
func (c *Client) read() {
	defer close(c.done)
	for {
		_, data, err := c.conn.Read(c.ctx)
		if err != nil {
			c.fail(failure("connection", "browser connection lost"))
			return
		}
		var msg response
		if json.Unmarshal(data, &msg) != nil || (msg.ID == 0 && msg.Method == "") || (msg.ID != 0 && (msg.Method != "" || (msg.Result == nil) == (msg.Error == nil))) {
			c.fail(failure("protocol", "malformed browser response"))
			return
		}
		if msg.ID == 0 {
			continue
		}
		c.mu.Lock()
		call, ok := c.pending[msg.ID]
		if ok && call.session == msg.Session {
			delete(c.pending, msg.ID)
			call.reply <- msg
		}
		c.mu.Unlock()
		if ok && call.session != msg.Session {
			c.fail(failure("protocol", "response session mismatch"))
			return
		}
	}
}
func (c *Client) call(ctx context.Context, session, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if len(c.pending) >= pendingLimit {
		c.mu.Unlock()
		return failure("overflow", "64 calls are already in flight")
	}
	c.next++
	id := c.next
	replies := make(chan response, 1)
	c.pending[id] = pendingCall{session, replies}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	data, err := json.Marshal(struct {
		ID      int64  `json:"id"`
		Session string `json:"sessionId,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{id, session, method, params})
	if err != nil {
		return failure("invalid_input", "cannot encode browser request")
	}
	if err = c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		c.fail(failure("connection", "browser write failed; outcome may be uncertain"))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.connectionError()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return c.connectionError()
	case msg := <-replies:
		if msg.Error != nil {
			return failure("protocol", fmt.Sprintf("%s failed (CDP %d)", method, msg.Error.Code))
		}
		if out != nil && json.Unmarshal(msg.Result, out) != nil {
			c.fail(failure("protocol", "invalid browser result"))
			return c.connectionError()
		}
		return nil
	}
}

type targetInfo struct {
	ID    PageID `json:"targetId"`
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (t targetInfo) eligible() bool {
	if t.Type != "page" {
		return false
	}
	for _, prefix := range []string{"chrome:", "chrome-untrusted:", "devtools:", "chrome-extension:", "edge:"} {
		if strings.HasPrefix(strings.ToLower(t.URL), prefix) {
			return false
		}
	}
	return true
}
func (t targetInfo) info() PageInfo { return PageInfo{t.ID, t.URL, t.Title} }

// Pages lists ordinary page targets, excluding browser-internal tabs.
func (c *Client) Pages(ctx context.Context) (PagesResult, error) {
	var result struct {
		Targets []targetInfo `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", nil, &result); err != nil {
		return PagesResult{}, err
	}
	pages := PagesResult{Pages: []PageInfo{}}
	for _, t := range result.Targets {
		if t.eligible() {
			pages.Pages = append(pages.Pages, t.info())
		}
	}
	sort.Slice(pages.Pages, func(i, j int) bool { return pages.Pages[i].ID < pages.Pages[j].ID })
	return pages, nil
}
