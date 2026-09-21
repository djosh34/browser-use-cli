// Package cdp controls an existing Chrome browser over the DevTools Protocol.
// Closing a Client disconnects; it never closes the browser or its tabs.
package cdp

import (
	"fmt"
	"strings"
)

// Error describes a failure without including endpoint credentials or raw
// protocol data. Context cancellation and deadlines retain their standard errors.
// Code distinguishes invalid_input, connection, protocol, overflow, page,
// ambiguous, navigation, dialog, unavailable, stale, blocked, javascript,
// and unsupported failures.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func failure(code, message string) error { return &Error{Code: code, Message: message} }

// PageID identifies a browser tab, not a position in the tab list.
type PageID string

// PageInfo is a captured tab identity and its current metadata.
type PageInfo struct {
	ID    PageID `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

func (p PageInfo) String() string { return fmt.Sprintf("%s\t%s\t%s", p.ID, p.Title, p.URL) }

// PagesResult contains the eligible tabs, ordered by ID.
type PagesResult struct {
	Pages []PageInfo `json:"pages"`
}

func (r PagesResult) String() string {
	lines := make([]string, len(r.Pages))
	for i, p := range r.Pages {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}
