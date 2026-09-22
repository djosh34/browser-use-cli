package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ControlsOptions enables extra, evidence-based DOM discovery with Verbose.
type ControlsOptions struct{ Verbose bool }

// SelectOption describes a native select option.
type SelectOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// ControlState records applicable native/accessibility state. Checked is empty,
// "true", "false", or "mixed". Password values are never included.
type ControlState struct {
	Checked         string   `json:"checked,omitempty"`
	Expanded        *bool    `json:"expanded,omitempty"`
	Selected        *bool    `json:"selected,omitempty"`
	Required        bool     `json:"required,omitempty"`
	Readonly        bool     `json:"readonly,omitempty"`
	Value           *string  `json:"value,omitempty"`
	SelectedOptions []string `json:"selectedOptions,omitempty"`
}

// Control is a captured candidate, not a guarantee of later actionability.
// Name is the semantic name; Context is a separate local contextual hint.
type Control struct {
	Target    ControlRef     `json:"target"`
	Role      string         `json:"role"`
	Name      string         `json:"name"`
	Context   string         `json:"context"`
	Href      string         `json:"href,omitempty"`
	Frame     FrameInfo      `json:"frame"`
	Offscreen bool           `json:"offscreen"`
	Source    string         `json:"source"`
	State     ControlState   `json:"state"`
	Options   []SelectOption `json:"options,omitempty"`
}

// ControlsResult holds one observation shared by String and encoding/json.
type ControlsResult struct {
	Page     PageInfo  `json:"page"`
	Controls []Control `json:"controls"`
	Warnings []string  `json:"warnings,omitempty"`
}

func (r ControlsResult) String() string {
	var b strings.Builder
	fmt.Fprintln(&b, r.Page)
	for _, c := range r.Controls {
		fmt.Fprintf(&b, "\n%s %q", c.Role, c.Name)
		if c.Context != "" {
			fmt.Fprintf(&b, " | %s", c.Context)
		}
		if c.Href != "" {
			fmt.Fprintf(&b, " | %s", c.Href)
		}
		fmt.Fprintf(&b, " [frame %s", c.Frame.ID)
		if c.Offscreen {
			b.WriteString(", offscreen")
		}
		if c.Source != "semantic" {
			b.WriteString(", DOM")
		}
		b.WriteString("]\n")
		state, _ := json.Marshal(c.State)
		if string(state) != "{}" {
			fmt.Fprintf(&b, "  state: %s\n", state)
		}
		for _, option := range c.Options {
			fmt.Fprintf(&b, "  option %q = %q", option.Label, option.Value)
			if option.Disabled {
				b.WriteString(" [disabled]")
			}
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s\n", c.Target)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	return strings.TrimRight(b.String(), "\n")
}

type axValue struct {
	Value json.RawMessage `json:"value"`
}

func (v axValue) text() string  { var s string; json.Unmarshal(v.Value, &s); return s }
func (v axValue) boolean() bool { var b bool; json.Unmarshal(v.Value, &b); return b }

type axNode struct {
	Backend    int64   `json:"backendDOMNodeId"`
	Ignored    bool    `json:"ignored"`
	Role       axValue `json:"role"`
	Name       axValue `json:"name"`
	Value      axValue `json:"value"`
	Properties []struct {
		Name  string  `json:"name"`
		Value axValue `json:"value"`
	} `json:"properties"`
}

func (n axNode) property(name string) axValue {
	for _, p := range n.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return axValue{}
}

// Controls captures semantic native and accessibility-supported controls without
// scrolling. Disabled, inert, and hidden nodes are excluded.
func (p *Page) Controls(ctx context.Context, opts ControlsOptions) (ControlsResult, error) {
	if err := p.lock(ctx); err != nil {
		return ControlsResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return ControlsResult{}, err
	}
	info, err := p.info(ctx)
	if err != nil {
		return ControlsResult{}, err
	}
	documents, warnings, err := p.documents(ctx)
	if err != nil {
		return ControlsResult{}, err
	}
	result := ControlsResult{Page: info, Controls: []Control{}, Warnings: warnings}
	viewports := map[string]viewportRect{}
	frameOffscreen := map[string]bool{}
	for _, doc := range documents {
		var tree struct {
			Nodes []axNode `json:"nodes"`
		}
		if err := p.client.call(ctx, doc.session, "Accessibility.getFullAXTree", map[string]string{"frameId": doc.frame.ID}, &tree); err != nil {
			return ControlsResult{}, err
		}
		ax := map[int64]axNode{}
		for _, node := range tree.Nodes {
			if node.Backend != 0 {
				ax[node.Backend] = node
			}
		}
		viewport, err := p.viewport(ctx, doc)
		if err != nil {
			return ControlsResult{}, err
		}
		viewports[doc.frame.ID] = viewport
		frameOffscreen[doc.frame.ID] = frameOffscreen[doc.parent]
		if doc.ownerBounds != nil {
			frameOffscreen[doc.frame.ID] = frameOffscreen[doc.frame.ID] || outsideViewport(*doc.ownerBounds, viewports[doc.parent])
		}
		s := doc.snapshot
		parent := map[int]int{}
		for index, node := range s.Nodes {
			for _, child := range node.Children {
				parent[child] = index
			}
		}
		var walk func(int, bool, clipRegion) error
		walk = func(index int, blocked bool, clip clipRegion) error {
			if index < 0 || index >= len(s.Nodes) {
				return failure("protocol", "invalid control node tree")
			}
			n := s.Nodes[index]
			layout := s.layout(n)
			blocked = blocked || n.hasAttr("inert") || n.attr("aria-disabled") == "true" || s.suppresses(layout)
			a := ax[n.Backend]
			role := a.Role.text()
			native := nativeRole(n)
			if role == "" || role == "generic" || role == "none" {
				role = native
			}
			available := !blocked && !n.hasAttr("disabled") && !a.property("disabled").boolean() && layout != nil && layout.Bounds.Width > 0 && layout.Bounds.Height > 0 && !clip.excludes(layout.Bounds) && s.style(layout, "visibility") != "hidden" && s.style(layout, "visibility") != "collapse"
			semantic := (native != "" || interactiveRole(role))
			extra := opts.Verbose && !semantic && s.domTarget(index, parent)
			if available && ((semantic && (!a.Ignored || native != "")) || extra) {
				ref, err := makeReference(p.id, doc, documents, n.Backend)
				if err != nil {
					return err
				}
				bounds := layout.Bounds
				c := Control{Target: ref, Role: role, Name: strings.TrimSpace(a.Name.text()), Frame: doc.frame, Source: "semantic",
					Offscreen: frameOffscreen[doc.frame.ID] || outsideViewport(bounds, viewport),
					Context:   s.controlContext(index, parent), State: controlState(n, a),
				}
				if extra {
					c.Role = "clickable"
					c.Source = "dom"
					c.Context = s.plainText(index)
				}
				if n.Name == "A" || n.Name == "AREA" {
					baseURL := s.Nodes[doc.root].BaseURL
					if baseURL == "" {
						baseURL = doc.frame.URL
					}
					if base, err := url.Parse(baseURL); err == nil {
						if href, err := url.Parse(n.attr("href")); err == nil {
							c.Href = base.ResolveReference(href).String()
						}
					}
				}
				if n.Name == "SELECT" {
					c.Options, c.State.SelectedOptions = s.selectOptions(index)
					value := ""
					if len(c.State.SelectedOptions) > 0 {
						value = c.State.SelectedOptions[0]
					}
					c.State.Value = &value
				}
				result.Controls = append(result.Controls, c)
			}
			if n.Name == "SELECT" {
				return nil
			}
			for _, child := range n.Children {
				if err := walk(child, blocked, s.childClip(clip, layout)); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(doc.root, false, clipRegion{}); err != nil {
			return ControlsResult{}, err
		}
	}
	if err := p.validateDocuments(ctx, documents); err != nil {
		return ControlsResult{}, err
	}
	return result, nil
}

// Extra targets need direct click evidence, or a pointer-styled list item
// under a local delegated list listener. A pointer cursor, focusability, or a
// document-wide listener alone is not enough.
func (s *domSnapshot) domTarget(index int, parents map[int]int) bool {
	n := s.Nodes[index]
	for ancestor, ok := parents[index]; ok; ancestor, ok = parents[ancestor] {
		parent := s.Nodes[ancestor]
		if nativeRole(parent) != "" || interactiveRole(parent.attr("role")) {
			return false
		}
	}
	if n.Type != 1 {
		return false
	}
	switch n.Name {
	case "HTML", "BODY", "FORM", "MAIN", "SECTION", "ARTICLE", "NAV", "UL", "OL", "TABLE", "LABEL", "SELECT", "OPTION":
		return false
	}
	text := s.plainText(index)
	if text == "" || len([]rune(text)) > 160 {
		return false
	}
	var nested func(int) bool
	nested = func(i int) bool {
		for _, child := range s.Nodes[i].Children {
			if child < 0 || child >= len(s.Nodes) {
				return true
			}
			c := s.Nodes[child]
			if c.Clickable || nativeRole(c) != "" || interactiveRole(c.attr("role")) || nested(child) {
				return true
			}
		}
		return false
	}
	if nested(index) {
		return false
	}
	if n.Clickable {
		return true
	}
	if n.Name == "LI" && s.style(s.layout(n), "cursor") == "pointer" {
		if i, ok := parents[index]; ok {
			parent := s.Nodes[i]
			return (parent.Name == "UL" || parent.Name == "OL") && parent.Clickable
		}
	}
	return false
}

func (n snapshotNode) hasAttr(name string) bool {
	for _, a := range n.Attributes {
		if a.Name == name {
			return true
		}
	}
	return false
}
func nativeRole(n snapshotNode) string {
	switch n.Name {
	case "A", "AREA":
		if n.hasAttr("href") {
			return "link"
		}
	case "BUTTON", "SUMMARY":
		return "button"
	case "TEXTAREA":
		return "textbox"
	case "SELECT":
		return "combobox"
	case "INPUT":
		switch strings.ToLower(n.attr("type")) {
		case "hidden":
			return ""
		case "checkbox":
			return "checkbox"
		case "radio":
			return "radio"
		case "button", "submit", "reset", "image":
			return "button"
		case "range":
			return "slider"
		case "number":
			return "spinbutton"
		default:
			return "textbox"
		}
	}
	if n.hasAttr("contenteditable") && n.attr("contenteditable") != "false" {
		return "textbox"
	}
	return ""
}
func interactiveRole(role string) bool {
	switch role {
	case "button", "link", "checkbox", "radio", "textbox", "searchbox", "combobox", "listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio", "slider", "spinbutton", "switch", "tab", "treeitem":
		return true
	}
	return false
}
func controlState(n snapshotNode, a axNode) ControlState {
	s := ControlState{Required: n.hasAttr("required") || a.property("required").boolean(), Readonly: n.hasAttr("readonly") || a.property("readonly").boolean()}
	for _, name := range []string{"expanded", "selected"} {
		v := a.property(name)
		if v.Value != nil {
			b := v.boolean()
			if name == "expanded" {
				s.Expanded = &b
			} else {
				s.Selected = &b
			}
		}
	}
	s.Checked = a.property("checked").text()
	if n.Name == "INPUT" && (strings.EqualFold(n.attr("type"), "checkbox") || strings.EqualFold(n.attr("type"), "radio")) && s.Checked == "" {
		s.Checked = fmt.Sprint(n.InputChecked)
	}
	if n.Name == "INPUT" && !strings.EqualFold(n.attr("type"), "password") {
		value := n.InputValue
		s.Value = &value
	}
	if n.Name == "TEXTAREA" {
		value := n.TextValue
		s.Value = &value
	}
	if n.hasAttr("contenteditable") && n.attr("contenteditable") != "false" {
		value := a.Value.text()
		s.Value = &value
	}
	return s
}
func (s *domSnapshot) plainText(index int) string {
	if index < 0 || index >= len(s.Nodes) {
		return ""
	}
	n := s.Nodes[index]
	if n.Name == "SCRIPT" || n.Name == "STYLE" || n.Name == "SELECT" || (n.Name == "INPUT" && strings.EqualFold(n.attr("type"), "password")) {
		return ""
	}
	var b strings.Builder
	if n.Type == 3 {
		b.WriteString(n.Value)
	}
	for _, child := range n.Children {
		b.WriteString(s.plainText(child))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Legacy forms often put a field and its visible text on one BR-delimited
// line without an associated label. Keep that context separate from its AX name.
func (s *domSnapshot) nearbyFieldText(index int, parents map[int]int) string {
	switch s.Nodes[index].Name {
	case "INPUT", "TEXTAREA", "SELECT":
	default:
		return ""
	}
	parent, ok := parents[index]
	if !ok {
		return ""
	}
	siblings := s.Nodes[parent].Children
	position := -1
	for i, child := range siblings {
		if child == index {
			position = i
			break
		}
	}
	if position < 0 {
		return ""
	}
	boundary := func(i int) bool {
		n := s.Nodes[i]
		switch n.Name {
		case "BR", "INPUT", "TEXTAREA", "SELECT", "BUTTON", "A":
			return true
		}
		switch s.style(s.layout(n), "display") {
		case "block", "flex", "grid", "table-row", "list-item":
			return true
		}
		return false
	}
	left, right := position, position+1
	for left > 0 && !boundary(siblings[left-1]) {
		left--
	}
	for right < len(siblings) && !boundary(siblings[right]) {
		right++
	}
	var text strings.Builder
	for _, child := range siblings[left:right] {
		if child != index {
			text.WriteString(s.plainText(child))
			text.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(text.String()), " ")
}
func (s *domSnapshot) controlContext(index int, parents map[int]int) string {
	context := s.plainText(index)
	if local := s.nearbyFieldText(index, parents); local != "" {
		context = local
	} else {
		for depth := 0; depth < 3; depth++ {
			parent, ok := parents[index]
			if !ok {
				break
			}
			n := s.Nodes[parent]
			if n.Name == "BODY" || n.Name == "HTML" || n.Name == "FORM" || n.Name == "TABLE" {
				break
			}
			candidate := s.plainText(parent)
			if len([]rune(candidate)) > 240 {
				// Keep a bounded excerpt of a local paragraph/card instead of
				// falling back to an indistinguishable repeated link label.
				switch n.Name {
				case "P", "LI", "TR", "ARTICLE", "LABEL":
					context = candidate
				}
				break
			}
			if candidate != "" {
				context = candidate
			}
			index = parent
			if n.Name == "ARTICLE" || n.Name == "LI" || n.Name == "TR" || n.Name == "P" || n.Name == "LABEL" {
				break
			}
		}
	}
	runes := []rune(context)
	if len(runes) > 240 {
		// Keep both the opening topic and text next to a trailing control.
		return string(runes[:96]) + "..." + string(runes[len(runes)-141:])
	}
	return context
}
func (s *domSnapshot) selectOptions(root int) ([]SelectOption, []string) {
	options := []SelectOption{}
	selected := []string{}
	var walk func(int, bool)
	walk = func(index int, disabled bool) {
		if index < 0 || index >= len(s.Nodes) {
			return
		}
		n := s.Nodes[index]
		disabled = disabled || n.hasAttr("disabled")
		if n.Name == "OPTION" {
			label := n.attr("label")
			if label == "" {
				label = s.plainText(index)
			}
			value := n.attr("value")
			if !n.hasAttr("value") {
				value = s.plainText(index)
			}
			options = append(options, SelectOption{value, label, disabled})
			if n.OptionSelected {
				selected = append(selected, value)
			}
		}
		for _, child := range n.Children {
			walk(child, disabled)
		}
	}
	walk(root, false)
	return options, selected
}
