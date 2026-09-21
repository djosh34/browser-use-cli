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
		s := doc.snapshot
		parent := map[int]int{}
		for index, node := range s.Nodes {
			for _, child := range node.Children {
				parent[child] = index
			}
		}
		var walk func(int, bool) error
		walk = func(index int, blocked bool) error {
			if index < 0 || index >= len(s.Nodes) {
				return failure("protocol", "invalid control node tree")
			}
			n := s.Nodes[index]
			layout := s.layout(n)
			blocked = blocked || n.hasAttr("inert") || n.attr("aria-disabled") == "true" || s.style(layout, "display") == "none" || s.style(layout, "opacity") == "0" || s.style(layout, "content-visibility") == "hidden"
			a := ax[n.Backend]
			role := a.Role.text()
			native := nativeRole(n)
			if role == "" || role == "generic" || role == "none" {
				role = native
			}
			available := !blocked && !n.hasAttr("disabled") && !a.property("disabled").boolean() && layout != nil && s.style(layout, "visibility") != "hidden" && s.style(layout, "visibility") != "collapse"
			semantic := (native != "" || interactiveRole(role))
			if available && semantic && (!a.Ignored || native != "") {
				ref, err := makeReference(p.id, doc, documents, n.Backend)
				if err != nil {
					return err
				}
				bounds := layout.Bounds
				c := Control{Target: ref, Role: role, Name: strings.TrimSpace(a.Name.text()), Frame: doc.frame, Source: "semantic",
					Offscreen: bounds.X+bounds.Width <= viewport.X || bounds.Y+bounds.Height <= viewport.Y || bounds.X >= viewport.X+viewport.Width || bounds.Y >= viewport.Y+viewport.Height,
					Context:   s.controlContext(index, parent), State: controlState(n, a),
				}
				if n.Name == "A" || n.Name == "AREA" {
					if base, err := url.Parse(doc.frame.URL); err == nil {
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
			for _, child := range n.Children {
				if err := walk(child, blocked); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(doc.root, false); err != nil {
			return ControlsResult{}, err
		}
	}
	return result, nil
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
	if n.Name == "INPUT" && (n.attr("type") == "checkbox" || n.attr("type") == "radio") && s.Checked == "" {
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
func (s *domSnapshot) controlContext(index int, parents map[int]int) string {
	context := s.plainText(index)
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
	runes := []rune(context)
	if len(runes) > 240 {
		return string(runes[:237]) + "..."
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
