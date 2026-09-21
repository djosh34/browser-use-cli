package cdp

import (
	"context"
	"encoding/json"
	"net/url"
)

const eventLimit = 256
const sessionLimit = 256

type pageState struct {
	gate       chan struct{}
	session    string // Protected by Client.mu, as are the event/dialog fields.
	events     chan response
	dialog     chan struct{}
	dialogOpen bool
	detached   bool
}

// Page is bound to one Client and tab. Multiple Page handles for the same tab
// share a lock and session. Closing the Client invalidates all of its Pages.
type Page struct {
	client *Client
	id     PageID
	state  *pageState
}

// Page selects an explicit eligible tab, or the sole eligible tab for an empty
// ID. An absent explicit ID never falls back to another page.
func (c *Client) Page(ctx context.Context, id PageID) (*Page, error) {
	pages, err := c.Pages(ctx)
	if err != nil {
		return nil, err
	}
	if id == "" {
		if len(pages.Pages) != 1 {
			return nil, failure("ambiguous", "select a page explicitly; there is not exactly one eligible tab")
		}
		id = pages.Pages[0].ID
	}
	for _, info := range pages.Pages {
		if info.ID == id {
			return c.pageHandle(id)
		}
	}
	return nil, failure("page", "the selected page is unavailable or ineligible")
}
func (c *Client) pageHandle(id PageID) (*Page, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.pages[id]
	if state == nil {
		if len(c.pages) >= sessionLimit {
			return nil, failure("overflow", "too many attached pages in this client")
		}
		state = &pageState{gate: make(chan struct{}, 1), dialog: make(chan struct{})}
		c.pages[id] = state
	}
	return &Page{c, id, state}, nil
}
func (p *Page) lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.client.ctx.Done():
		return p.client.connectionError()
	case p.state.gate <- struct{}{}:
		return nil
	}
}
func (p *Page) unlock() { <-p.state.gate }

// Info captures the tab's current metadata and checks that it is still eligible.
func (p *Page) Info(ctx context.Context) (PageInfo, error) {
	if err := p.lock(ctx); err != nil {
		return PageInfo{}, err
	}
	defer p.unlock()
	return p.info(ctx)
}
func (p *Page) info(ctx context.Context) (PageInfo, error) {
	var result struct {
		Target targetInfo `json:"targetInfo"`
	}
	if err := p.client.call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": p.id}, &result); err != nil {
		return PageInfo{}, err
	}
	if result.Target.ID != p.id || !result.Target.eligible() {
		return PageInfo{}, failure("page", "the selected page is unavailable or ineligible")
	}
	return result.Target.info(), nil
}
func (p *Page) attach(ctx context.Context) error {
	if _, err := p.info(ctx); err != nil {
		return err
	}
	c := p.client
	c.mu.Lock()
	attached := p.state.session != ""
	detached := p.state.detached
	c.mu.Unlock()
	if detached {
		return failure("page", "the selected page session has detached")
	}
	if attached {
		return nil
	}
	var result struct {
		Session string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": p.id, "flatten": true}, &result); err != nil {
		return err
	}
	if result.Session == "" {
		return failure("protocol", "browser did not return an attached session")
	}
	c.mu.Lock()
	p.state.session = result.Session
	c.sessions[result.Session] = p.state
	c.mu.Unlock()
	for _, method := range []string{"Page.enable", "Page.setLifecycleEventsEnabled"} {
		var params any
		if method == "Page.setLifecycleEventsEnabled" {
			params = map[string]bool{"enabled": true}
		}
		if err := c.call(ctx, result.Session, method, params, nil); err != nil {
			// A partially initialized session must not be reused as though it were ready.
			c.mu.Lock()
			p.state.detached = true
			c.mu.Unlock()
			return err
		}
	}
	return nil
}

// Open reuses the sole eligible tab. With zero or multiple eligible tabs it
// creates a new tab. It waits for the requested document to load.
func (c *Client) Open(ctx context.Context, address string) (*Page, error) {
	if err := navigationURL(address); err != nil {
		return nil, err
	}
	// Serialize the selection/create rule within this Client.
	select {
	case c.openGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.connectionError()
	}
	defer func() { <-c.openGate }()
	pages, err := c.Pages(ctx)
	if err != nil {
		return nil, err
	}
	var id PageID
	if len(pages.Pages) == 1 {
		id = pages.Pages[0].ID
	} else {
		var created struct {
			ID PageID `json:"targetId"`
		}
		if err := c.call(ctx, "", "Target.createTarget", map[string]string{"url": "about:blank"}, &created); err != nil {
			return nil, err
		}
		if created.ID == "" {
			return nil, failure("protocol", "browser did not return the new page identity")
		}
		id = created.ID
	}
	p, err := c.pageHandle(id)
	if err != nil {
		return nil, err
	}
	if err := p.Navigate(ctx, address); err != nil {
		return nil, err
	}
	return p, nil
}

func navigationURL(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme == "" {
		return failure("invalid_input", "navigation requires an absolute URL")
	}
	// Script URLs do not have document-load semantics and browser-internal pages
	// must not become accidental automation targets.
	switch u.Scheme {
	case "http", "https":
		if u.Host != "" {
			return nil
		}
	case "about":
		if address == "about:blank" {
			return nil
		}
	case "file", "data":
		return nil
	}
	return failure("invalid_input", "unsupported navigation URL")
}

// Navigate waits for the main-frame load belonging to this navigation, including
// redirects. It does not wait for network idle or future application timers.
func (p *Page) Navigate(ctx context.Context, address string) error {
	if err := navigationURL(address); err != nil {
		return err
	}
	if err := p.lock(ctx); err != nil {
		return err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return err
	}
	events, err := p.observe()
	if err != nil {
		return err
	}
	defer p.unobserve()
	var result struct {
		Frame    string `json:"frameId"`
		Loader   string `json:"loaderId"`
		Error    string `json:"errorText"`
		Download bool   `json:"isDownload"`
	}
	if err := p.client.call(ctx, p.state.session, "Page.navigate", map[string]string{"url": address}, &result); err != nil {
		return err
	}
	if result.Error != "" {
		return failure("navigation", "browser could not navigate to the requested document")
	}
	if result.Download {
		return failure("navigation", "navigation became a download, not a document")
	}
	if result.Frame == "" {
		return failure("protocol", "navigation did not return a frame identity")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.client.ctx.Done():
			return p.client.connectionError()
		case ev := <-events:
			switch ev.Method {
			case "Page.javascriptDialogOpening":
				return failure("dialog", "a native dialog prevents completion")
			case "Target.detachedFromTarget", "Inspector.detached":
				return failure("page", "page detached during navigation")
			case "Page.lifecycleEvent":
				var e struct {
					Frame  string `json:"frameId"`
					Loader string `json:"loaderId"`
					Name   string `json:"name"`
				}
				if json.Unmarshal(ev.Params, &e) != nil {
					return failure("protocol", "invalid lifecycle event")
				}
				if result.Loader != "" && e.Frame == result.Frame && e.Loader == result.Loader && e.Name == "load" {
					return nil
				}
			case "Page.navigatedWithinDocument":
				var e struct {
					Frame string `json:"frameId"`
				}
				if json.Unmarshal(ev.Params, &e) != nil {
					return failure("protocol", "invalid same-document event")
				}
				if result.Loader == "" && e.Frame == result.Frame {
					return nil
				}
			}
		}
	}
}
func (p *Page) observe() (chan response, error) {
	c := p.client
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.state.dialogOpen {
		return nil, failure("dialog", "a native dialog prevents completion")
	}
	if p.state.detached {
		return nil, failure("page", "page session is detached")
	}
	events := make(chan response, eventLimit)
	p.state.events = events
	return events, nil
}
func (p *Page) unobserve() { c := p.client; c.mu.Lock(); p.state.events = nil; c.mu.Unlock() }

// routeEvent handles only state needed by page operations. It is not a public
// event subscription mechanism. The reader never blocks on a page operation.
func (c *Client) routeEvent(ev response) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.sessions[ev.Session]
	if ev.Method == "Target.detachedFromTarget" {
		var p struct {
			Session string `json:"sessionId"`
		}
		if json.Unmarshal(ev.Params, &p) != nil {
			return failure("protocol", "invalid detach event")
		}
		state = c.sessions[p.Session]
	}
	if state == nil {
		return nil
	}
	switch ev.Method {
	case "Page.javascriptDialogOpening":
		if !state.dialogOpen {
			state.dialogOpen = true
			close(state.dialog)
		}
	case "Page.javascriptDialogClosed":
		state.dialogOpen = false
		state.dialog = make(chan struct{})
	case "Target.detachedFromTarget", "Inspector.detached":
		state.detached = true
	case "Page.lifecycleEvent", "Page.navigatedWithinDocument", "Page.frameNavigated", "Page.frameStartedLoading", "Page.frameStoppedLoading":
	default:
		return nil
	}
	if state.events != nil {
		select {
		case state.events <- ev:
		default:
			return failure("overflow", "page event queue overflow")
		}
	}
	return nil
}
