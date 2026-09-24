package cdp

import (
	"context"
	"encoding/json"
)

type domNode struct {
	ID         int64     `json:"nodeId"`
	Backend    int64     `json:"backendNodeId"`
	Type       int       `json:"nodeType"`
	Frame      string    `json:"frameId"`
	ShadowType string    `json:"shadowRootType"`
	Children   []domNode `json:"children"`
	Shadows    []domNode `json:"shadowRoots"`
	Document   *domNode  `json:"contentDocument"`
}

// CSS is the only DOM scoping operation. Descendant identities select exposed
// AX roots; their AX (not DOM) descendants are then retained, including aria-owns.
func (p *Page) selectorMatches(ctx context.Context, doc documentCapture, selector string) (map[int64]bool, bool, error) {
	world, err := p.isolatedWorld(ctx, doc)
	if err != nil {
		return nil, false, err
	}
	quoted, _ := json.Marshal(selector)
	var syntax struct {
		Result struct {
			Value *bool `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	err = p.client.call(ctx, doc.session, "Runtime.evaluate", map[string]any{"contextId": world, "returnByValue": true, "expression": "(s=>{try{document.querySelector(s);return true}catch{return false}})(" + string(quoted) + ")"}, &syntax)
	if err != nil {
		return nil, false, err
	}
	if syntax.Exception != nil || syntax.Result.Value == nil {
		return nil, false, failure("unavailable", "document changed during selector validation")
	}
	if !*syntax.Result.Value {
		return nil, false, failure("invalid_input", "invalid CSS selector")
	}
	var document struct {
		Root domNode `json:"root"`
	}
	if err := p.client.call(ctx, doc.session, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true}, &document); err != nil {
		return nil, false, err
	}
	root := &document.Root
	// getDocument returns the session root; find this same-process document.
	if root.Frame != doc.frame.ID {
		var find func(*domNode) *domNode
		find = func(n *domNode) *domNode {
			if n.Type == 9 && n.Frame == doc.frame.ID {
				return n
			}
			if n.Document != nil {
				if n.Frame == doc.frame.ID {
					return n.Document
				}
				if found := find(n.Document); found != nil {
					return found
				}
			}
			for i := range n.Children {
				if found := find(&n.Children[i]); found != nil {
					return found
				}
			}
			return nil
		}
		if found := find(root); found != nil {
			root = found
		} else {
			tree, e := p.frameTree(ctx, doc.session)
			if e != nil {
				return nil, false, e
			}
			if tree.Frame.ID != doc.frame.ID {
				return nil, false, failure("unavailable", "selector document disappeared")
			}
		}
	}
	nodes := map[int64]*domNode{}
	scopes := []int64{}
	var walk func(*domNode)
	walk = func(n *domNode) {
		if n.ShadowType == "user-agent" {
			return
		}
		nodes[n.ID] = n
		if n.Type == 9 || n.Type == 11 {
			scopes = append(scopes, n.ID)
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
		for i := range n.Shadows {
			walk(&n.Shadows[i])
		}
	}
	walk(root)
	matched := false
	backends := map[int64]bool{}
	var descend func(*domNode)
	descend = func(n *domNode) {
		if n.ShadowType == "user-agent" {
			return
		}
		backends[n.Backend] = true
		for i := range n.Children {
			descend(&n.Children[i])
		}
		for i := range n.Shadows {
			descend(&n.Shadows[i])
		}
	}
	for _, scope := range scopes {
		var result struct {
			Nodes []int64 `json:"nodeIds"`
		}
		if err := p.client.call(ctx, doc.session, "DOM.querySelectorAll", map[string]any{"nodeId": scope, "selector": selector}, &result); err != nil {
			if collectionFatal(ctx, err) {
				return nil, false, err
			}
			return nil, false, failure("unavailable", "document changed during selector collection")
		}
		for _, id := range result.Nodes {
			n, ok := nodes[id]
			if !ok {
				return nil, false, failure("unavailable", "selector match disappeared")
			}
			matched = true
			descend(n)
		}
	}
	return backends, matched, nil
}
