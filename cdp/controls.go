package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type axValue struct {
	Value json.RawMessage `json:"value"`
}

func (v axValue) text() string  { var s string; _ = json.Unmarshal(v.Value, &s); return s }
func (v axValue) boolean() bool { var b bool; _ = json.Unmarshal(v.Value, &b); return b }

type axProperty struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}
type axNode struct {
	ID          string       `json:"nodeId"`
	Parent      string       `json:"parentId"`
	Children    []string     `json:"childIds"`
	Backend     int64        `json:"backendDOMNodeId"`
	Ignored     bool         `json:"ignored"`
	Reasons     []axProperty `json:"ignoredReasons"`
	Role        axValue      `json:"role"`
	Name        axValue      `json:"name"`
	Description axValue      `json:"description"`
	Value       axValue      `json:"value"`
	Properties  []axProperty `json:"properties"`
}

func (n axNode) property(name string) axValue {
	for _, p := range n.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return axValue{}
}

type controlTarget struct {
	doc     documentCapture
	chain   []documentCapture
	backend int64
}

// captureAX is shared by Read and fresh action lookup. IDs are assigned only
// after frame splicing and before any display filters; bindings are native only.
func (p *Page) captureAX(ctx context.Context, docs []documentCapture) (*Node, map[ControlID]controlTarget, []string, error) {
	if len(docs) == 0 {
		return nil, nil, nil, failure("unavailable", "no main document")
	}
	roots := map[string]*Node{}
	byDoc := map[string]documentCapture{}
	warnings := []string{}
	for i, doc := range docs {
		byDoc[doc.frame.ID] = doc
		var result struct {
			Nodes []axNode `json:"nodes"`
		}
		err := p.client.call(ctx, doc.session, "Accessibility.getFullAXTree", map[string]string{"frameId": doc.frame.ID}, &result)
		if err == nil && len(result.Nodes) == 0 {
			err = failure("unavailable", "browser returned no accessibility document")
		}
		var root *Node
		if err == nil {
			root, err = p.normalizeAX(ctx, doc, result.Nodes)
		}
		if err != nil {
			if i == 0 || collectionFatal(ctx, err) || p.client.ctx.Err() != nil {
				return nil, nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("Accessibility for embedded document %s is unavailable", doc.frame.URL))
			continue
		}
		// A child race is partial, whereas replacing the main document invalidates
		// the observation. Validate each captured document before publishing it.
		if err = p.validateDocuments(ctx, []documentCapture{doc}); err != nil {
			if i == 0 || collectionFatal(ctx, err) {
				return nil, nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("Embedded document %s changed during capture", doc.frame.URL))
			continue
		}
		roots[doc.frame.ID] = root
	}
	main := roots[docs[0].frame.ID]
	// Append only at the native accessible owner, never at a guessed DOM parent.
	for _, doc := range docs[1:] {
		child := roots[doc.frame.ID]
		if child == nil {
			continue
		}
		parent, ok := byDoc[doc.parent]
		if !ok {
			warnings = append(warnings, "An embedded document has no captured parent")
			continue
		}
		var owner struct {
			Backend int64 `json:"backendNodeId"`
		}
		if err := p.client.call(ctx, parent.session, "DOM.getFrameOwner", map[string]string{"frameId": doc.frame.ID}, &owner); err != nil {
			if collectionFatal(ctx, err) {
				return nil, nil, nil, err
			}
			warnings = append(warnings, "An embedded document could not be placed in the accessibility tree")
			continue
		}
		var splice func(*Node) bool
		splice = func(n *Node) bool {
			if n == nil {
				return false
			}
			if n.doc == parent.frame.ID && n.backend == owner.Backend {
				n.Children = append(n.Children, child)
				return true
			}
			for _, c := range n.Children {
				if splice(c) {
					return true
				}
			}
			return false
		}
		// An AX-hidden embedding intentionally exposes no child document.
		splice(roots[parent.frame.ID])
	}
	targets := map[ControlID]controlTarget{}
	var next ControlID
	var number func(*Node)
	number = func(n *Node) {
		if n == nil {
			return
		}
		if n.ID != 0 {
			next++
			n.ID = next
			doc := byDoc[n.doc]
			chain := []documentCapture{doc}
			for parent := doc.parent; parent != ""; {
				ancestor, ok := byDoc[parent]
				if !ok {
					break
				}
				chain = append([]documentCapture{ancestor}, chain...)
				parent = ancestor.parent
			}
			targets[next] = controlTarget{doc: doc, chain: chain, backend: n.backend}
		}
		for _, c := range n.Children {
			number(c)
		}
	}
	number(main)
	return main, targets, warnings, nil
}

func (p *Page) normalizeAX(ctx context.Context, doc documentCapture, nodes []axNode) (*Node, error) {
	index := map[string]axNode{}
	rootID := ""
	for _, n := range nodes {
		if n.ID == "" {
			return nil, failure("protocol", "accessibility node has no identity")
		}
		index[n.ID] = n
		if n.Role.text() == "RootWebArea" || n.Role.text() == "WebArea" {
			if rootID == "" {
				rootID = n.ID
			}
		}
	}
	if rootID == "" {
		return nil, failure("unavailable", "accessibility document root is unavailable")
	}
	seen := map[string]bool{}
	var walk func(string, bool, bool) ([]*Node, error)
	walk = func(id string, editableAncestor, staticAncestor bool) ([]*Node, error) {
		n, ok := index[id]
		if !ok {
			return nil, failure("protocol", "accessibility tree has a missing child")
		}
		if seen[id] {
			return nil, failure("protocol", "accessibility tree repeats a child identity")
		}
		seen[id] = true
		if n.Ignored {
			for _, r := range n.Reasons {
				switch r.Name {
				// Explicit accessibility-hidden/inert subtrees are exclusion
				// boundaries. Other ignored reasons describe this wrapper,
				// not necessarily its descendants (e.g. an active modal).
				case "ariaHiddenElement", "ariaHiddenSubtree", "inertElement", "inertSubtree":
					return nil, nil
				}
			}
		}
		role := n.Role.text()
		if role == "InlineTextBox" && staticAncestor {
			return nil, nil
		}
		editable := n.property("editable").text() != "" && n.property("editable").text() != "false"
		out := &Node{Role: role, Name: n.Name.text(), Description: n.Description.text(), doc: doc.frame.ID, backend: n.Backend}
		protected := n.property("protected").boolean() || n.property("password").boolean()
		if len(n.Value.Value) > 0 && n.Backend != 0 && !protected {
			var described struct {
				Node struct {
					Attributes []string `json:"attributes"`
				} `json:"node"`
			}
			if err := p.client.call(ctx, doc.session, "DOM.describeNode", map[string]any{"backendNodeId": n.Backend}, &described); err != nil {
				return nil, err
			}
			for i := 0; i+1 < len(described.Node.Attributes); i += 2 {
				if strings.EqualFold(described.Node.Attributes[i], "type") && strings.EqualFold(described.Node.Attributes[i+1], "password") {
					protected = true
				}
			}
		}
		if !protected && len(n.Value.Value) > 0 {
			value := n.Value.text()
			if value == "" && string(n.Value.Value) != "\"\"" && string(n.Value.Value) != "null" {
				value = string(n.Value.Value)
			}
			out.Value = &value
		}
		if role == "RootWebArea" || role == "WebArea" {
			out.Role = "document"
			out.URL = doc.frame.URL
		}
		if role == "StaticText" || role == "InlineTextBox" {
			out.Role = "text"
			out.Text = out.Name
			out.Name = ""
		}
		for _, property := range n.Properties {
			var v any
			if json.Unmarshal(property.Value.Value, &v) != nil {
				continue
			}
			switch property.Name {
			case "url":
				out.URL = property.Value.text()
			case "level":
				if level, ok := v.(float64); ok {
					out.Level = int(level)
				}
			case "disabled", "readonly", "required", "checked", "pressed", "selected", "expanded", "focused", "focusable", "editable", "multiline", "multiselectable", "orientation", "autocomplete", "hasPopup", "invalid", "busy", "modal", "settable", "valuemin", "valuemax", "valuetext", "live", "atomic", "relevant", "roledescription", "keyshortcuts":
				if v == "true" {
					v = true
				} else if v == "false" {
					v = false
				}
				if out.State == nil {
					out.State = map[string]any{}
				}
				out.State[property.Name] = v
			}
		}
		if !n.Ignored && interactiveAX(role, n, editable && !editableAncestor) {
			out.ID = 1
		}
		for _, child := range n.Children {
			children, err := walk(child, editableAncestor || (!n.Ignored && editable), staticAncestor || role == "StaticText")
			if err != nil {
				return nil, err
			}
			out.Children = append(out.Children, children...)
		}
		if n.Ignored {
			return out.Children, nil
		}
		if protected {
			out.Value = nil
			out.Children = nil
		}
		if len(out.Children) == 1 {
			child := out.Children[0]
			if child.Role == "text" && child.ID == 0 && child.Description == "" && child.Value == nil && len(child.Children) == 0 && flatAX(child.Text) == flatAX(out.Name) && out.Name != "" {
				// Keep the identity of mechanically folded text for explicit CSS
				// scopes; its content is now represented by this parent's name.
				out.scopeBackends = append(out.scopeBackends, child.backend)
				out.scopeBackends = append(out.scopeBackends, child.scopeBackends...)
				out.Children = nil
			}
		}
		return []*Node{out}, nil
	}
	roots, err := walk(rootID, false, false)
	if err != nil {
		return nil, err
	}
	if len(roots) != 1 {
		return nil, failure("unavailable", "accessibility document has no exposed root")
	}
	return roots[0], nil
}

func interactiveAX(role string, n axNode, editableRoot bool) bool {
	switch strings.ToLower(role) {
	case "button", "link", "checkbox", "radio", "textbox", "searchbox", "combobox", "listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "slider", "spinbutton", "scrollbar", "switch", "tab", "treeitem", "colorwell", "date", "datetime", "inputtime", "disclosuretriangle", "disclosuretrianglegrouped", "popupbutton":
		return true
	case "gridcell":
		return n.property("focusable").boolean() || n.property("editable").text() != "" && n.property("editable").text() != "false"
	case "separator":
		return n.property("focusable").boolean()
	}
	return editableRoot && role != "StaticText" && role != "InlineTextBox"
}

func flatAX(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ", "\f", " ").Replace(s)
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == ' ' }), " ")
}
