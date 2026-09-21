package cdp

import (
	"context"
	"encoding/json"
	"errors"
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

// selectorMatches asks Chrome to parse CSS in each document/shadow scope. The
// returned identities refer to the same snapshot nodes used by text collection.
func (p *Page) selectorMatches(ctx context.Context, doc documentCapture, selector string) (map[int64]bool, error) {
	session := doc.session
	world, err := p.isolatedWorld(ctx, doc)
	if err != nil {
		return nil, err
	}
	quoted, _ := json.Marshal(selector)
	var syntax struct {
		Result struct {
			Value *bool `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	err = p.client.call(ctx, session, "Runtime.evaluate", map[string]any{"contextId": world, "returnByValue": true, "expression": "(s=>{try{document.querySelector(s);return true}catch{return false}})(" + string(quoted) + ")"}, &syntax)
	if err != nil {
		return nil, err
	}
	if syntax.Exception != nil || syntax.Result.Value == nil {
		return nil, failure("unavailable", "document changed during selector validation")
	}
	if !*syntax.Result.Value {
		return nil, failure("invalid_input", "invalid CSS selector")
	}
	var document struct {
		Root domNode `json:"root"`
	}
	if err := p.client.call(ctx, session, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true}, &document); err != nil {
		return nil, err
	}
	nodes := map[int64]int64{}
	scopes := []int64{}
	var walk func(domNode)
	walk = func(n domNode) {
		if n.ShadowType == "user-agent" {
			return
		}
		nodes[n.ID] = n.Backend
		if n.Type == 9 || n.Type == 11 {
			scopes = append(scopes, n.ID)
		}
		for _, child := range n.Children {
			walk(child)
		}
		for _, shadow := range n.Shadows {
			walk(shadow)
		}
		if n.Document != nil {
			walk(*n.Document)
		}
	}
	walk(document.Root)
	matched := map[int64]bool{}
	for _, scope := range scopes {
		var result struct {
			Nodes []int64 `json:"nodeIds"`
		}
		if err := p.client.call(ctx, session, "DOM.querySelectorAll", map[string]any{"nodeId": scope, "selector": selector}, &result); err != nil {
			var e *Error
			if errors.As(err, &e) && e.Code == "protocol" {
				return nil, failure("unavailable", "document changed during selector collection")
			}
			return nil, err
		}
		for _, node := range result.Nodes {
			backend, ok := nodes[node]
			if !ok {
				return nil, failure("unavailable", "document changed during selector collection")
			}
			matched[backend] = true
		}
	}
	return matched, nil
}
