package cdp

import (
	"context"
	"fmt"
	"strings"
)

// ReadOptions narrows Read to CSS selector matches. Empty selects the whole page.
type ReadOptions struct{ Selector string }

// FrameInfo identifies the source document of captured content.
type FrameInfo struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// ReadSection contains rendered text from one frame, in document order.
type ReadSection struct {
	Frame FrameInfo `json:"frame"`
	Text  string    `json:"text"`
}

// ReadResult is one captured observation. String does no browser I/O.
type ReadResult struct {
	Page     PageInfo      `json:"page"`
	Sections []ReadSection `json:"sections"`
	Warnings []string      `json:"warnings,omitempty"`
}

func (r ReadResult) String() string {
	var b strings.Builder
	fmt.Fprintln(&b, r.Page)
	for _, s := range r.Sections {
		fmt.Fprintf(&b, "\n[frame %s %s]\n%s\n", s.Frame.ID, s.Frame.URL, s.Text)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", warning)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Read captures all currently rendered text, including offscreen content. It
// neither scrolls nor triggers lazy loading and does not expose password values.
func (p *Page) Read(ctx context.Context, opts ReadOptions) (ReadResult, error) {
	if err := p.lock(ctx); err != nil {
		return ReadResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return ReadResult{}, err
	}
	info, err := p.info(ctx)
	if err != nil {
		return ReadResult{}, err
	}
	documents, warnings, err := p.documents(ctx)
	if err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{Page: info, Sections: []ReadSection{}, Warnings: warnings}
	matches := map[string]map[int64]bool{}
	for _, doc := range documents {
		var selected map[int64]bool
		if opts.Selector != "" {
			var ok bool
			selected, ok = matches[doc.session]
			if !ok {
				selected, err = p.selectorMatches(ctx, doc.session, opts.Selector)
				if err != nil {
					return ReadResult{}, err
				}
				matches[doc.session] = selected
			}
		}
		text, matched, err := doc.snapshot.readDocument(doc.root, selected)
		if err != nil {
			return ReadResult{}, err
		}
		if opts.Selector != "" && !matched {
			continue
		}
		result.Sections = append(result.Sections, ReadSection{Frame: doc.frame, Text: text})
	}
	if len(result.Sections) == 0 {
		if opts.Selector != "" {
			return ReadResult{}, failure("invalid_input", "selector matched no rendered region")
		}
		return ReadResult{}, failure("unavailable", "browser returned no readable document")
	}
	return result, nil
}

type snapshotNode struct {
	Backend    int64  `json:"backendNodeId"`
	Type       int    `json:"nodeType"`
	Name       string `json:"nodeName"`
	Value      string `json:"nodeValue"`
	Children   []int  `json:"childNodeIndexes"`
	Document   *int   `json:"contentDocumentIndex"`
	Layout     *int   `json:"layoutNodeIndex"`
	Attributes []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"attributes"`
	Frame          string `json:"frameId"`
	URL            string `json:"documentURL"`
	InputValue     string `json:"inputValue"`
	InputChecked   bool   `json:"inputChecked"`
	Clickable      bool   `json:"isClickable"`
	OptionSelected bool   `json:"optionSelected"`
	TextValue      string `json:"textValue"`
}

func (n snapshotNode) attr(name string) string {
	for _, a := range n.Attributes {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

type snapshotLayout struct {
	Node   int          `json:"domNodeIndex"`
	Text   string       `json:"layoutText"`
	Style  *int         `json:"styleIndex"`
	Bounds viewportRect `json:"boundingBox"`
}
type domSnapshot struct {
	Nodes   []snapshotNode   `json:"domNodes"`
	Layouts []snapshotLayout `json:"layoutTreeNodes"`
	Styles  []struct {
		Properties []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"properties"`
	} `json:"computedStyles"`
}

func (p *Page) snapshot(ctx context.Context, session string) (domSnapshot, error) {
	var s domSnapshot
	err := p.client.call(ctx, session, "DOMSnapshot.getSnapshot", map[string]any{
		"computedStyleWhitelist":     []string{"display", "visibility", "opacity", "cursor", "content-visibility"},
		"includeUserAgentShadowTree": false,
	}, &s)
	if err != nil {
		return s, err
	}
	if len(s.Nodes) == 0 {
		return s, failure("unavailable", "document changed during collection")
	}
	return s, nil
}
func (s *domSnapshot) style(l *snapshotLayout, name string) string {
	if l == nil || l.Style == nil || *l.Style < 0 || *l.Style >= len(s.Styles) {
		return ""
	}
	for _, p := range s.Styles[*l.Style].Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}
func (s *domSnapshot) layout(n snapshotNode) *snapshotLayout {
	if n.Layout == nil || *n.Layout < 0 || *n.Layout >= len(s.Layouts) {
		return nil
	}
	return &s.Layouts[*n.Layout]
}
func (s *domSnapshot) readDocument(root int, selected map[int64]bool) (string, bool, error) {
	var b strings.Builder
	matched := false
	seen := make(map[int]bool)
	var walk func(int, bool) error
	walk = func(index int, within bool) error {
		if index < 0 || index >= len(s.Nodes) || seen[index] {
			return failure("protocol", "invalid snapshot node tree")
		}
		seen[index] = true
		n := s.Nodes[index]
		within = within || selected[n.Backend]
		layout := s.layout(n)
		if s.style(layout, "display") == "none" || s.style(layout, "opacity") == "0" || s.style(layout, "content-visibility") == "hidden" || (n.Name == "INPUT" && strings.EqualFold(n.attr("type"), "password")) {
			return nil
		}
		visible := layout != nil && s.style(layout, "visibility") != "hidden" && s.style(layout, "visibility") != "collapse"
		block := false
		if visible && within {
			matched = true
			switch n.Name {
			case "H1", "H2", "H3", "H4", "H5", "H6":
				fmt.Fprintf(&b, "\n%s ", strings.Repeat("#", int(n.Name[1]-'0')))
				block = true
			case "LI":
				b.WriteString("\n* ")
				block = true
			case "P", "DIV", "SECTION", "ARTICLE", "MAIN", "HEADER", "FOOTER", "NAV", "UL", "OL", "TABLE", "TR", "BLOCKQUOTE", "PRE":
				b.WriteByte('\n')
				block = true
			case "BR":
				b.WriteByte('\n')
			case "TD", "TH":
				b.WriteString(" | ")
			}
			if n.Type == 3 {
				b.WriteString(layout.Text)
			}
			if n.Name == "INPUT" {
				b.WriteString(n.InputValue)
			}
			if n.Name == "TEXTAREA" {
				b.WriteString(n.TextValue)
				return nil
			}
		}
		for _, child := range n.Children {
			if err := walk(child, within); err != nil {
				return err
			}
		}
		if block {
			b.WriteByte('\n')
		}
		return nil
	}
	if err := walk(root, selected == nil); err != nil {
		return "", false, err
	}
	// Preserve inline text and table separators; collapse only empty block lines.
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), matched, nil
}
