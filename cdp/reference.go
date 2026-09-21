package cdp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
)

// ControlRef is a self-contained target emitted by Controls. Store and pass it
// unchanged; its encoding is private and does not contain endpoint credentials.
type ControlRef string

type frameRef struct {
	ID       string `json:"i"`
	Loader   string `json:"l"`
	Document int64  `json:"d"`
}
type controlRef struct {
	Page   PageID     `json:"p"`
	Frames []frameRef `json:"f"`
	Node   int64      `json:"n"`
}

// PageID extracts local routing information, without I/O. Actions separately
// validate the live browser, complete frame/document chain, and node identity.
func (r ControlRef) PageID() (PageID, error) {
	decoded, err := r.decode()
	return decoded.Page, err
}
func (r ControlRef) decode() (controlRef, error) {
	var target controlRef
	invalid := func() (controlRef, error) {
		return controlRef{}, failure("invalid_input", "malformed or unsupported control reference")
	}
	if len(r) > 16384 || !strings.HasPrefix(string(r), "cdp1.") {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(r), "cdp1."))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&target) != nil || target.Page == "" || len(target.Page) > 128 || target.Node <= 0 || len(target.Frames) == 0 || len(target.Frames) > 64 {
		return invalid()
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return invalid()
	}
	seen := map[string]bool{}
	for _, frame := range target.Frames {
		if frame.ID == "" || len(frame.ID) > 128 || frame.Loader == "" || len(frame.Loader) > 128 || frame.Document <= 0 || seen[frame.ID] {
			return invalid()
		}
		seen[frame.ID] = true
	}
	return target, nil
}
func makeReference(page PageID, doc documentCapture, documents []documentCapture, node int64) (ControlRef, error) {
	chain := []frameRef{}
	visited := map[string]bool{}
	for {
		if visited[doc.frame.ID] || len(chain) >= 64 {
			return "", failure("overflow", "frame reference chain exceeds its bound")
		}
		visited[doc.frame.ID] = true
		chain = append(chain, frameRef{doc.frame.ID, doc.loader, doc.snapshot.Nodes[doc.root].Backend})
		if doc.parent == "" {
			break
		}
		found := false
		for _, parent := range documents {
			if parent.frame.ID == doc.parent {
				doc = parent
				found = true
				break
			}
		}
		if !found {
			return "", failure("unavailable", "a parent document is unavailable for the control reference")
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	data, err := json.Marshal(controlRef{page, chain, node})
	if err != nil {
		return "", failure("protocol", "cannot encode control identity")
	}
	r := ControlRef("cdp1." + base64.RawURLEncoding.EncodeToString(data))
	if _, err := r.PageID(); err != nil {
		return "", err
	}
	return r, nil
}
