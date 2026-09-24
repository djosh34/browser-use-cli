package cdp

import (
	"context"
	"fmt"
)

type frameInfo struct{ ID, URL string }
type frameTree struct {
	Frame struct {
		ID     string `json:"id"`
		Parent string `json:"parentId"`
		Loader string `json:"loaderId"`
		URL    string `json:"url"`
	} `json:"frame"`
	Children []frameTree `json:"childFrames"`
}
type documentCapture struct {
	frame                   frameInfo
	parent, loader, session string
	backend                 int64
}

func (p *Page) frameTree(ctx context.Context, session string) (frameTree, error) {
	var result struct {
		Tree frameTree `json:"frameTree"`
	}
	err := p.client.call(ctx, session, "Page.getFrameTree", nil, &result)
	if err == nil && result.Tree.Frame.ID == "" {
		err = failure("protocol", "browser returned no frame tree")
	}
	return result.Tree, err
}

// Documents carry native binding identities, never a layout-derived observation.
func (p *Page) documents(ctx context.Context) ([]documentCapture, []string, error) {
	root, err := p.frameTree(ctx, p.state.session)
	if err != nil {
		return nil, nil, err
	}
	var docs []documentCapture
	var warnings []string
	seen := map[string]bool{}
	var collect func(frameTree, string, string) error
	collect = func(tree frameTree, session, parent string) error {
		id := tree.Frame.ID
		if seen[id] {
			return nil
		}
		seen[id] = true
		if len(seen) > sessionLimit {
			return failure("overflow", "too many documents in this page")
		}
		if tree.Frame.Parent != "" {
			parent = tree.Frame.Parent
		}
		docs = append(docs, documentCapture{frame: frameInfo{id, tree.Frame.URL}, parent: parent, loader: tree.Frame.Loader, session: session})
		for _, child := range tree.Children {
			if err := collect(child, session, id); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(root, p.state.session, ""); err != nil {
		return nil, nil, err
	}
	// DOM is used only to locate embedded documents omitted by Page.getFrameTree
	// (notably OOPIFs), not to discover accessible content or controls.
	for i := 0; i < len(docs); i++ {
		doc := docs[i]
		if i > 0 && doc.loader == "" {
			session, e := p.frameSession(ctx, doc.frame.ID)
			if e != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				warnings = append(warnings, "An embedded document could not be attached")
				continue
			}
			tree, e := p.frameTree(ctx, session)
			if e != nil {
				warnings = append(warnings, "An embedded document could not be captured")
				continue
			}
			docs[i].session = session
			docs[i].loader = tree.Frame.Loader
			docs[i].frame.URL = tree.Frame.URL
			doc = docs[i]
			for _, child := range tree.Children {
				if err := collect(child, session, doc.frame.ID); err != nil {
					return nil, nil, err
				}
			}
		}
		var result struct {
			Root domNode `json:"root"`
		}
		if err := p.client.call(ctx, doc.session, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true}, &result); err != nil {
			if i == 0 || ctx.Err() != nil {
				return nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("An embedded document (%s) could not be inspected", doc.frame.URL))
			continue
		}
		var walk func(domNode, string) error
		walk = func(n domNode, parent string) error {
			if n.Type == 9 && n.Frame != "" {
				parent = n.Frame
			}
			if n.Frame != "" && n.Type == 1 && !seen[n.Frame] {
				seen[n.Frame] = true
				if len(seen) > sessionLimit {
					return failure("overflow", "too many documents in this page")
				}
				docs = append(docs, documentCapture{frame: frameInfo{ID: n.Frame}, parent: parent})
			}
			for _, c := range n.Children {
				if err := walk(c, parent); err != nil {
					return err
				}
			}
			for _, c := range n.Shadows {
				if err := walk(c, parent); err != nil {
					return err
				}
			}
			if n.Document != nil {
				if err := walk(*n.Document, n.Frame); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(result.Root, doc.frame.ID); err != nil {
			return nil, nil, err
		}
	}
	available := docs[:0]
	for _, doc := range docs {
		if doc.session != "" {
			available = append(available, doc)
		}
	}
	return available, warnings, nil
}

// Recheck after selector/AX capture too, so results never silently combine
// a snapshot from one document with semantic data from its replacement.
func (p *Page) validateDocuments(ctx context.Context, documents []documentCapture) error {
	loaders := map[string]map[string]string{}
	for _, doc := range documents {
		current, ok := loaders[doc.session]
		if !ok {
			tree, err := p.frameTree(ctx, doc.session)
			if err != nil {
				return err
			}
			current = map[string]string{}
			var walk func(frameTree)
			walk = func(t frameTree) {
				current[t.Frame.ID] = t.Frame.Loader
				for _, child := range t.Children {
					walk(child)
				}
			}
			walk(tree)
			loaders[doc.session] = current
		}
		if current[doc.frame.ID] != doc.loader {
			return failure("unavailable", "a document changed during collection; observe the page again")
		}
	}
	return nil
}

func (p *Page) frameSession(ctx context.Context, id string) (string, error) {
	c := p.client
	c.mu.Lock()
	state := p.state.frames[id]
	if state != nil && state.detached {
		delete(c.sessions, state.session)
		delete(p.state.frames, id)
		state = nil
	}
	if state != nil {
		session := state.session
		initialized := state.initialized
		c.mu.Unlock()
		if !initialized {
			if err := c.call(ctx, session, "Page.enable", nil, nil); err != nil {
				return "", err
			}
			c.mu.Lock()
			state.initialized = true
			c.mu.Unlock()
		}
		return session, nil
	}
	if len(p.state.frames) >= sessionLimit {
		c.mu.Unlock()
		return "", failure("overflow", "too many frame sessions in this page")
	}
	c.mu.Unlock()
	var result struct {
		Session string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": id, "flatten": true}, &result); err != nil {
		return "", err
	}
	if result.Session == "" {
		return "", failure("protocol", "browser did not return a frame session")
	}
	state = &sessionState{root: &p.state.sessionState, session: result.Session, gone: make(chan struct{})}
	c.mu.Lock()
	p.state.frames[id] = state
	c.sessions[result.Session] = state
	c.mu.Unlock()
	if err := c.call(ctx, result.Session, "Page.enable", nil, nil); err != nil {
		return "", err
	}
	c.mu.Lock()
	state.initialized = true
	c.mu.Unlock()
	return result.Session, nil
}
