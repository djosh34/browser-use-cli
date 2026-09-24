package cdp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
	conn     *websocket.Conn
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	next     int64
	pending  map[int64]pendingCall
	err      error
	pages    map[string]*pageState
	sessions map[string]*sessionState
	openGate chan struct{}
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
	conn.SetReadLimit(messageLimit + 1)
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{conn: conn, ctx: lifetime, cancel: cancel, done: make(chan struct{}), pending: make(map[int64]pendingCall), pages: make(map[string]*pageState), sessions: make(map[string]*sessionState), openGate: make(chan struct{}, 1)}
	go c.read()
	// This browser-only command also rejects page-level connections behind proxies.
	var contexts struct {
		IDs []string `json:"browserContextIds"`
	}
	if err := c.call(ctx, "", "Target.getBrowserContexts", nil, &contexts); err != nil {
		c.Close()
		return nil, err
	}
	if contexts.IDs == nil {
		c.Close()
		return nil, failure("protocol", "browser endpoint did not return browser contexts")
	}
	// Force creation of newly remote frame agent hosts before navigation.
	// A default filter preserves ordinary page/iframe listing; discovery
	// events themselves need no subscription or additional reader state.
	if err := c.call(ctx, "", "Target.setDiscoverTargets", map[string]bool{"discover": true}, nil); err != nil {
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
		kind, reader, err := c.conn.Reader(c.ctx)
		if err != nil {
			c.fail(failure("connection", "browser connection lost"))
			return
		}
		if kind != websocket.MessageText {
			c.fail(failure("protocol", "browser sent a non-text message"))
			return
		}
		data, err := io.ReadAll(io.LimitReader(reader, messageLimit+1))
		if len(data) > messageLimit {
			c.fail(failure("overflow", "browser message exceeds 64 MiB"))
			return
		}
		if err != nil {
			c.fail(failure("connection", "browser connection lost"))
			return
		}
		var msg response
		if json.Unmarshal(data, &msg) != nil || (msg.ID == 0 && msg.Method == "") || (msg.ID != 0 && (msg.Method != "" || (msg.Result == nil) == (msg.Error == nil) || (msg.Result != nil && !bytes.HasPrefix(bytes.TrimSpace(msg.Result), []byte("{"))))) {
			c.fail(failure("protocol", "malformed browser response"))
			return
		}
		if msg.ID == 0 {
			if err := c.routeEvent(msg); err != nil {
				c.fail(err)
				return
			}
			continue
		}
		c.mu.Lock()
		call, ok := c.pending[msg.ID]
		// Chrome emits a session-not-found routing error at browser scope,
		// even for a request originally addressed to a flattened session.
		matches := call.session == msg.Session || (call.session != "" && msg.Session == "" && msg.Error != nil && msg.Error.Code == -32001)
		if ok && matches {
			delete(c.pending, msg.ID)
			call.reply <- msg
		}
		c.mu.Unlock()
		if ok && !matches {
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
	if session != "" && c.sessions[session] == nil {
		c.mu.Unlock()
		return failure("page", "page session is unavailable")
	}
	var dialog, gone <-chan struct{}
	if state := c.sessions[session]; state != nil {
		if state.detached {
			c.mu.Unlock()
			return failure("page", "page session is detached")
		}
		dialogState := state
		if state.root != nil {
			dialogState = state.root
		}
		if dialogState.dialogOpen {
			c.mu.Unlock()
			return failure("dialog", "a native dialog prevents completion")
		}
		dialog = dialogState.dialog
		gone = state.gone
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
	case <-dialog:
		return failure("dialog", "a native dialog prevents completion")
	case <-gone:
		return failure("page", "page session detached during the operation")
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
	ID    string `json:"targetId"`
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
func (t targetInfo) info(id PageID) PageInfo { return PageInfo{id, t.URL, t.Title} }

// Pages lists ordinary page targets, excluding browser-internal tabs.
func (c *Client) Pages(ctx context.Context) (PagesResult, error) {
	targets, err := c.pageTargets(ctx)
	if err != nil {
		return PagesResult{}, err
	}
	result := PagesResult{Pages: make([]PageInfo, len(targets))}
	for i, target := range targets {
		result.Pages[i] = target.info(PageID(i + 1))
	}
	return result, nil
}

// Only native identities persist in session state. Public ordinals are derived
// from this fresh snapshot and are never used as registry keys.
func (c *Client) pageTargets(ctx context.Context) ([]targetInfo, error) {
	c.mu.Lock()
	known := maps.Clone(c.pages)
	c.mu.Unlock()
	var result struct {
		Targets []targetInfo `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", nil, &result); err != nil {
		return nil, err
	}
	if result.Targets == nil {
		return nil, failure("protocol", "browser result has no page list")
	}
	pages := []targetInfo{}
	for _, t := range result.Targets {
		if t.ID == "" {
			return nil, failure("protocol", "browser returned an empty target identity")
		}
		delete(known, t.ID)
		if t.eligible() {
			pages = append(pages, t)
		}
	}
	// Retire only handles that existed before this snapshot request. A
	// concurrent Open may have created a page after Chrome captured the list.
	c.mu.Lock()
	for id, state := range known {
		if c.pages[id] != state {
			continue
		}
		delete(c.pages, id)
		delete(c.sessions, state.session)
		for _, frame := range state.frames {
			delete(c.sessions, frame.session)
			if !frame.detached {
				frame.detached = true
				close(frame.gone)
			}
		}
		clear(state.frames)
		if !state.detached {
			state.detached = true
			close(state.gone)
		}
	}
	c.mu.Unlock()
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	return pages, nil
}
