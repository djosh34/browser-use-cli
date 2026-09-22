package cdp

import (
	"context"
	"encoding/json"
	"errors"
)

const inputFrameLimit = 256
const inputEventLimit = 4096

// One bounded observation belongs to one serialized input operation. There is
// no event subscription API, background waiter, or persistent action state.
type inputObservation struct {
	before     PagesResult
	documents  []documentCapture
	events     chan response
	states     []*sessionState
	baseline   map[string]string
	background map[string]bool
	processed  int
}
type inputNavigation struct {
	loader        string
	done, stopped bool
}

type inputLifecycle struct {
	FrameID string `json:"frameId"`
	Loader  string `json:"loaderId"`
	Name    string `json:"name"`
	Frame   struct {
		ID          string `json:"id"`
		Loader      string `json:"loaderId"`
		Unreachable string `json:"unreachableUrl"`
	} `json:"frame"`
}

func (a *inputObservation) rememberFrame(frame string) error {
	if frame == "" {
		return nil
	}
	if _, known := a.baseline[frame]; !known {
		if len(a.baseline) >= inputFrameLimit {
			return failure("overflow", "too many frames during input")
		}
		a.baseline[frame] = ""
	}
	return nil
}
func (a *inputObservation) lifecycle(ev response) (inputLifecycle, error) {
	var event inputLifecycle
	a.processed++
	if a.processed > inputEventLimit {
		return event, failure("overflow", "too many lifecycle events during input")
	}
	if ev.Method == "Page.javascriptDialogOpening" {
		return event, failure("dialog", "a native dialog prevents completion")
	}
	if json.Unmarshal(ev.Params, &event) != nil {
		return event, failure("protocol", "invalid input lifecycle event")
	}
	if ev.Method == "Page.frameNavigated" {
		event.FrameID = event.Frame.ID
		event.Loader = event.Frame.Loader
	}
	return event, a.rememberFrame(event.FrameID)
}

// Events already queued before input belong to preflight/background work.
// Keep their loader baseline through commit/load, rather than adopting a
// pre-existing iframe request merely because it commits after the keystroke.
func (a *inputObservation) startInput(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case ev := <-a.events:
			event, err := a.lifecycle(ev)
			if err != nil {
				return err
			}
			frame := event.FrameID
			if frame == "" {
				continue
			}
			if event.Loader != "" {
				a.baseline[frame] = event.Loader
			}
			switch ev.Method {
			case "Page.frameStartedLoading", "Page.frameNavigated":
				a.background[frame] = true
			case "Page.lifecycleEvent":
				if event.Name == "init" {
					a.background[frame] = true
				}
				if event.Name == "load" {
					delete(a.background, frame)
				}
			case "Page.frameStoppedLoading":
				delete(a.background, frame)
			}
		default:
			return nil
		}
	}
}

func (p *Page) beginInput(ctx context.Context) (*inputObservation, error) {
	documents, warnings, err := p.documents(ctx)
	if err != nil {
		return nil, err
	}
	if len(warnings) != 0 {
		return nil, failure("unavailable", "cannot observe every frame before input")
	}
	if len(documents) == 0 {
		return nil, failure("unavailable", "page has no available document")
	}
	before, err := p.client.Pages(ctx)
	if err != nil {
		return nil, err
	}
	a := &inputObservation{before: before, documents: documents, events: make(chan response, eventLimit), baseline: map[string]string{}, background: map[string]bool{}}
	for _, doc := range documents {
		if err := a.rememberFrame(doc.frame.ID); err != nil {
			return nil, err
		}
		a.baseline[doc.frame.ID] = doc.loader
	}
	if err := p.observeInputDocuments(ctx, a, documents); err != nil {
		p.endInput(a)
		return nil, err
	}
	return a, nil
}
func (p *Page) observeInputDocuments(ctx context.Context, a *inputObservation, documents []documentCapture) error {
	var err error
	added := []string{}
	p.client.mu.Lock()
	for _, doc := range documents {
		state := p.client.sessions[doc.session]
		if state != nil && state.events == a.events {
			continue
		}
		if len(a.states) >= sessionLimit+1 {
			err = failure("overflow", "too many input frame-session transitions")
			break
		}
		if state == nil || state.detached {
			err = failure("page", "frame detached before input")
			break
		}
		if state.dialogOpen {
			err = failure("dialog", "a native dialog prevents completion")
			break
		}
		state.events = a.events
		state.inputEvents = true
		a.states = append(a.states, state)
		added = append(added, doc.session)
	}
	p.client.mu.Unlock()
	if err != nil {
		return err
	}
	for _, session := range added {
		if session == p.state.session {
			continue
		}
		if err := p.client.call(ctx, session, "Page.setLifecycleEventsEnabled", map[string]bool{"enabled": true}, nil); err != nil {
			return err
		}
	}
	return nil
}
func (p *Page) endInput(a *inputObservation) {
	p.client.mu.Lock()
	defer p.client.mu.Unlock()
	for _, state := range a.states {
		if state.events == a.events {
			state.events = nil
			state.inputEvents = false
		}
	}
}

// Input can move a frame into or out of a remote renderer. Refresh only the
// completion contexts and lifecycle observation, never the mutation.
func (p *Page) settleInputFrame(ctx context.Context, a *inputObservation, actor documentCapture) error {
	documents, warnings, err := p.documents(ctx)
	if err != nil {
		return err
	}
	if len(warnings) != 0 {
		return failure("unavailable", "input frame changed while verifying completion")
	}
	if err := p.observeInputDocuments(ctx, a, documents); err != nil {
		return err
	}
	for _, doc := range documents {
		if doc.frame.ID == actor.frame.ID {
			if doc.frame.ID == a.documents[0].frame.ID {
				return nil
			} // Root task/layout already settled.
			return p.settleFrame(ctx, doc)
		}
	}
	return nil // Input may have removed its own frame.
}

func (p *Page) completeInput(ctx context.Context, a *inputObservation, actor documentCapture) (ActionResult, error) {
	initial := a.baseline
	root := a.documents[0]
	navigations := map[string]*inputNavigation{}
	handle := func(ev response) error {
		event, err := a.lifecycle(ev)
		if err != nil {
			return err
		}
		frame := event.FrameID
		if frame == "" {
			return nil
		}
		if a.background[frame] {
			if ev.Method == "Page.frameStartedLoading" {
				delete(a.background, frame)
			} else {
				if event.Loader != "" {
					initial[frame] = event.Loader
				}
				if ev.Method == "Page.frameStoppedLoading" || ev.Method == "Page.lifecycleEvent" && event.Name == "load" {
					delete(a.background, frame)
				}
				return nil
			}
		}
		nav := navigations[frame]
		switch ev.Method {
		case "Page.frameStartedLoading":
			if nav == nil {
				navigations[frame] = &inputNavigation{}
			}
		case "Page.frameNavigated", "Page.lifecycleEvent":
			if event.Frame.Unreachable != "" {
				return failure("navigation", "input navigation could not load its destination")
			}
			if event.Loader == "" || event.Loader == initial[frame] {
				return nil
			}
			if nav == nil {
				nav = &inputNavigation{}
				navigations[frame] = nav
			}
			if nav.loader != event.Loader {
				nav.loader = event.Loader
				nav.done = false
				nav.stopped = false
			}
			if ev.Method == "Page.lifecycleEvent" && event.Name == "load" {
				nav.done = true
			}
		case "Page.navigatedWithinDocument":
			navigations[frame] = &inputNavigation{done: true}
		case "Page.frameStoppedLoading":
			if nav != nil {
				nav.stopped = true
			}
		}
		return nil
	}
	drain := func() error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			select {
			case ev := <-a.events:
				if err := handle(ev); err != nil {
					return err
				}
			default:
				return nil
			}
		}
	}
	pending := func() (bool, error) {
		// Loading a replacement main document supersedes its old descendants.
		if nav := navigations[root.frame.ID]; nav != nil && nav.done && nav.loader != "" {
			return false, nil
		}
		for _, nav := range navigations {
			if !nav.done {
				if nav.stopped && nav.loader == "" {
					return false, failure("navigation", "input navigation stopped without loading a document")
				}
				return true, nil
			}
		}
		return false, nil
	}
	failedSettles := 0
	for {
		settleErr := p.settleFrame(ctx, root)
		// Keyboard input or a root-page handler can navigate any descendant.
		// Refresh lifecycle coverage even when the original actor was the root.
		if settleErr == nil {
			settleErr = p.settleInputFrame(ctx, a, actor)
		}
		if err := drain(); err != nil {
			return ActionResult{}, err
		}
		if ctx.Err() != nil {
			return ActionResult{}, ctx.Err()
		}
		if p.client.ctx.Err() != nil {
			return ActionResult{}, p.client.connectionError()
		}
		var typed *Error
		if errors.As(settleErr, &typed) && (typed.Code == "dialog" || typed.Code == "overflow") {
			return ActionResult{}, settleErr
		}
		waiting, err := pending()
		if err != nil {
			return ActionResult{}, err
		}
		if !waiting {
			if settleErr == nil {
				return p.actionResult(ctx, a.before)
			}
			failedSettles++
			if len(navigations) == 0 || failedSettles >= 2 {
				return ActionResult{}, settleErr
			}
			continue
		}
		for waiting {
			select {
			case <-ctx.Done():
				return ActionResult{}, ctx.Err()
			case <-p.client.ctx.Done():
				return ActionResult{}, p.client.connectionError()
			case <-p.state.gone:
				return ActionResult{}, failure("page", "page detached during input completion")
			case ev := <-a.events:
				if err := handle(ev); err != nil {
					return ActionResult{}, err
				}
			}
			if err := drain(); err != nil {
				return ActionResult{}, err
			}
			waiting, err = pending()
			if err != nil {
				return ActionResult{}, err
			}
		}
		// A completed load gets its own immediate task/layout turn. Input itself
		// was dispatched exactly once, even when the old execution context vanished.
	}
}
