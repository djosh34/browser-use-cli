package cdp

import (
	"context"
	"fmt"
)

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
	frame       FrameInfo
	parent      string
	loader      string
	session     string
	snapshot    *domSnapshot
	root        int
	ownerBounds *viewportRect
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

// documents walks only frames belonging to this tab. Same-process documents
// share a snapshot; out-of-process frames need their own flattened CDP session.
func (p *Page) documents(ctx context.Context) ([]documentCapture, []string, error) {
	root, err := p.frameTree(ctx, p.state.session)
	if err != nil {
		return nil, nil, err
	}
	known := map[string]frameTree{}
	order := []string{}
	var addTree func(frameTree, string)
	addTree = func(tree frameTree, parent string) {
		if tree.Frame.Parent == "" {
			tree.Frame.Parent = parent
		}
		if _, ok := known[tree.Frame.ID]; !ok {
			order = append(order, tree.Frame.ID)
		}
		known[tree.Frame.ID] = tree
		for _, child := range tree.Children {
			addTree(child, tree.Frame.ID)
		}
	}
	addTree(root, "")
	captures := map[string]documentCapture{}
	hidden := map[string]bool{}
	ownerBounds := map[string]*viewportRect{}
	warnings := []string{}
	for index := 0; index < len(order); index++ {
		id := order[index]
		if hidden[id] {
			continue
		}
		if _, ok := captures[id]; ok {
			continue
		}
		session := p.state.session
		if index > 0 {
			session, err = p.frameSession(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil, ctx.Err()
				}
				if p.client.ctx.Err() != nil {
					return nil, nil, p.client.connectionError()
				}
				warnings = append(warnings, fmt.Sprintf("frame %s is unavailable", id))
				continue
			}
			tree, err := p.frameTree(ctx, session)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("frame %s changed during collection", id))
				continue
			}
			addTree(tree, known[id].Frame.Parent)
		}
		snapshot, err := p.snapshot(ctx, session)
		if err != nil {
			if index == 0 || ctx.Err() != nil {
				return nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("frame %s could not be captured", id))
			continue
		}
		// Chrome's frame tree and snapshot omit remote documents. Resolve
		// their owner elements to find the embedded frame identity, rather
		// than guessing from URLs or attaching unrelated browser targets.
		for rootIndex, node := range snapshot.Nodes {
			if node.Type != 9 || hidden[node.Frame] {
				continue
			}
			var owners func(int, bool) error
			owners = func(i int, suppressed bool) error {
				if i < 0 || i >= len(snapshot.Nodes) {
					return failure("protocol", "invalid frame owner tree")
				}
				n := snapshot.Nodes[i]
				layout := snapshot.layout(n)
				suppressed = suppressed || snapshot.style(layout, "display") == "none" || snapshot.style(layout, "opacity") == "0" || snapshot.style(layout, "content-visibility") == "hidden"
				if n.Name == "IFRAME" || n.Name == "FRAME" {
					frameID := n.Frame
					if frameID == "" {
						var described struct {
							Node struct {
								Frame string `json:"frameId"`
							} `json:"node"`
						}
						if err := p.client.call(ctx, session, "DOM.describeNode", map[string]any{"backendNodeId": n.Backend}, &described); err != nil {
							return err
						}
						frameID = described.Node.Frame
					}
					if frameID == "" {
						if !suppressed && layout != nil {
							warnings = append(warnings, fmt.Sprintf("an embedded frame in %s is unavailable", node.Frame))
						}
						return nil
					}
					if _, ok := known[frameID]; !ok {
						var remote frameTree
						remote.Frame.ID = frameID
						addTree(remote, node.Frame)
					}
					hidden[frameID] = suppressed || layout == nil || layout.Bounds.Width <= 0 || layout.Bounds.Height <= 0 || snapshot.style(layout, "visibility") == "hidden" || snapshot.style(layout, "visibility") == "collapse"
					if layout != nil {
						bounds := layout.Bounds
						ownerBounds[frameID] = &bounds
					}
				}
				for _, child := range n.Children {
					if err := owners(child, suppressed); err != nil {
						return err
					}
				}
				return nil
			}
			if err := owners(rootIndex, false); err != nil {
				return nil, nil, err
			}
		}
		after, err := p.frameTree(ctx, session)
		if err != nil {
			return nil, nil, err
		}
		stable := map[string]string{}
		var loaders func(frameTree)
		loaders = func(tree frameTree) {
			stable[tree.Frame.ID] = tree.Frame.Loader
			for _, child := range tree.Children {
				loaders(child)
			}
		}
		loaders(after)
		for i, node := range snapshot.Nodes {
			if node.Type != 9 {
				continue
			}
			frame, ok := known[node.Frame]
			if !ok || stable[node.Frame] != frame.Frame.Loader {
				if node.Frame == root.Frame.ID {
					return nil, nil, failure("unavailable", "main document changed during collection")
				}
				warnings = append(warnings, fmt.Sprintf("frame %s changed during collection", node.Frame))
				continue
			}
			captures[node.Frame] = documentCapture{FrameInfo{node.Frame, node.URL}, frame.Frame.Parent, frame.Frame.Loader, session, &snapshot, i, ownerBounds[node.Frame]}
		}
	}
	result := make([]documentCapture, 0, len(captures))
	// Follow parentage in discovery order so a remote descendant precedes
	// its parent's next sibling, irrespective of which session captured it.
	visited := map[string]bool{}
	var appendDocument func(string)
	appendDocument = func(id string) {
		if visited[id] || hidden[id] {
			return
		}
		visited[id] = true
		if doc, ok := captures[id]; ok {
			result = append(result, doc)
		}
		for _, child := range order {
			if known[child].Frame.Parent == id {
				appendDocument(child)
			}
		}
	}
	appendDocument(root.Frame.ID)
	p.client.mu.Lock()
	for id, state := range p.state.frames {
		_, exists := known[id]
		if !exists || state.detached {
			delete(p.state.frames, id)
			delete(p.client.sessions, state.session)
			if !state.detached {
				state.detached = true
				close(state.gone)
			}
		}
	}
	p.client.mu.Unlock()
	return result, warnings, nil
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
	state = &sessionState{session: result.Session, dialog: make(chan struct{}), gone: make(chan struct{})}
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
