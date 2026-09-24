package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ReadOptions filters a page-wide observation without changing control numbers.
type ReadOptions struct {
	Selector     string
	ControlsOnly bool
}

// Node is native accessible content. A zero ID denotes context, not a control.
// State retains explicit false values and native mixed/grammar/spelling tokens.
type Node struct {
	ID            ControlID      `json:"id,omitempty"`
	Role          string         `json:"role"`
	Name          string         `json:"name,omitempty"`
	Text          string         `json:"text,omitempty"`
	Description   string         `json:"description,omitempty"`
	Value         *string        `json:"value,omitempty"`
	URL           string         `json:"url,omitempty"`
	Level         int            `json:"level,omitempty"`
	State         map[string]any `json:"state,omitempty"`
	Children      []*Node        `json:"children,omitempty"`
	doc           string
	backend       int64
	scopeBackends []int64
}

// ReadResult is one captured value; String performs no browser I/O.
type ReadResult struct {
	Page     PageInfo `json:"page"`
	Complete bool     `json:"complete"`
	Tree     *Node    `json:"tree"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r ReadResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Page %d · %s · %s\n", r.Page.ID, r.Page.Title, r.Page.URL)
	if !r.Complete {
		b.WriteString("Warning: incomplete accessibility observation\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "Warning: %s\n", w)
	}
	var render func(*Node, string, string, string)
	render = func(n *Node, prefix, branch, next string) {
		if n == nil {
			return
		}
		b.WriteString(prefix + branch)
		if n.ID != 0 {
			fmt.Fprintf(&b, "[%d] ", n.ID)
		}
		b.WriteString(n.Role)
		if n.Name != "" {
			fmt.Fprintf(&b, " %q", n.Name)
		}
		if n.Text != "" {
			fmt.Fprintf(&b, " %q", n.Text)
		}
		if n.Value != nil {
			fmt.Fprintf(&b, " value=%q", *n.Value)
		}
		if n.Level != 0 {
			fmt.Fprintf(&b, " level=%d", n.Level)
		}
		keys := make([]string, 0, len(n.State))
		for k := range n.State {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v, _ := json.Marshal(n.State[k])
			fmt.Fprintf(&b, " %s=%s", k, v)
		}
		b.WriteByte('\n')
		if n.Description != "" {
			fmt.Fprintf(&b, "%s%sdescription: %q\n", prefix, next, n.Description)
		}
		if n.URL != "" {
			fmt.Fprintf(&b, "%s%surl: %s\n", prefix, next, n.URL)
		}
		for i, c := range n.Children {
			branch, indent := "├─ ", "│  "
			if i == len(n.Children)-1 {
				branch, indent = "└─ ", "   "
			}
			render(c, prefix+next, branch, indent)
		}
	}
	if r.Tree != nil {
		b.WriteByte('\n')
		render(r.Tree, "", "", "")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// Read captures the full native AX hierarchy, including offscreen content.
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
	docs, warnings, err := p.documents(ctx)
	if err != nil {
		return ReadResult{}, err
	}
	tree, _, axWarnings, err := p.captureAX(ctx, docs)
	if err != nil {
		return ReadResult{}, err
	}
	warnings = append(warnings, axWarnings...)
	if opts.Selector != "" {
		selected := map[string]map[int64]bool{}
		exposed := map[string]map[int64]bool{}
		var indexExposed func(*Node)
		indexExposed = func(n *Node) {
			if n == nil {
				return
			}
			if exposed[n.doc] == nil {
				exposed[n.doc] = map[int64]bool{}
			}
			if n.backend != 0 {
				exposed[n.doc][n.backend] = true
			}
			for _, backend := range n.scopeBackends {
				if backend != 0 {
					exposed[n.doc][backend] = true
				}
			}
			for _, child := range n.Children {
				indexExposed(child)
			}
		}
		indexExposed(tree)
		matched := false
		for i, doc := range docs {
			ids, ok, e := p.selectorMatches(ctx, doc, opts.Selector, exposed[doc.frame.ID])
			if e != nil {
				var problem *Error
				if i == 0 || collectionFatal(ctx, e) || errors.As(e, &problem) && problem.Code == "invalid_input" {
					return ReadResult{}, e
				}
				warnings = append(warnings, "An embedded document was unavailable during CSS scoping")
				tree = withoutDocument(tree, doc.frame.ID)
				continue
			}
			matched = matched || ok
			selected[doc.frame.ID] = ids
		}
		if !matched {
			if len(warnings) > 0 {
				return ReadResult{}, failure("unavailable", "no selector match was captured and some documents were unavailable")
			}
			return ReadResult{}, failure("invalid_input", "selector matched no DOM region")
		}
		tree = filterAX(tree, func(n *Node) bool {
			if selected[n.doc][n.backend] {
				return true
			}
			for _, backend := range n.scopeBackends {
				if selected[n.doc][backend] {
					return true
				}
			}
			return false
		}, true)
	}
	// Selector collection runs after AX capture. Revalidate before publishing,
	// so a navigation cannot combine old AX with a new document's CSS matches.
	for i, doc := range docs {
		if err := p.validateDocuments(ctx, []documentCapture{doc}); err != nil {
			if i == 0 || collectionFatal(ctx, err) {
				return ReadResult{}, err
			}
			warnings = append(warnings, "An embedded document changed during observation")
			tree = withoutDocument(tree, doc.frame.ID)
		}
	}
	if opts.ControlsOnly {
		tree = filterAX(tree, func(n *Node) bool { return n.ID != 0 }, false)
	}
	return ReadResult{Page: info, Complete: len(warnings) == 0, Tree: tree, Warnings: warnings}, nil
}

func collectionFatal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var e *Error
	return errors.As(err, &e) && (e.Code == "overflow" || e.Code == "connection")
}

func withoutDocument(n *Node, doc string) *Node {
	if n == nil || n.doc == doc {
		return nil
	}
	var children []*Node
	for _, c := range n.Children {
		if kept := withoutDocument(c, doc); kept != nil {
			children = append(children, kept)
		}
	}
	n.Children = children
	return n
}

func filterAX(n *Node, keep func(*Node) bool, subtree bool) *Node {
	if n == nil {
		return nil
	}
	own := keep(n)
	if own && subtree {
		return n
	}
	var children []*Node
	for _, c := range n.Children {
		if kept := filterAX(c, keep, subtree); kept != nil {
			children = append(children, kept)
		}
	}
	if !own && len(children) == 0 {
		return nil
	}
	copy := *n
	copy.Children = children
	return &copy
}
