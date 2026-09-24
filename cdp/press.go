package cdp

import (
	"context"
	"strings"
	"unicode/utf8"
)

type keyStroke struct {
	key, code, text   string
	virtual, modifier int
}

var namedKeys = map[string]keyStroke{
	"Enter": {"Enter", "Enter", "\r", 13, 0}, "Tab": {"Tab", "Tab", "", 9, 0},
	"Escape": {"Escape", "Escape", "", 27, 0}, "Backspace": {"Backspace", "Backspace", "", 8, 0},
	"Delete": {"Delete", "Delete", "", 46, 0}, "Insert": {"Insert", "Insert", "", 45, 0},
	"Home": {"Home", "Home", "", 36, 0}, "End": {"End", "End", "", 35, 0},
	"PageUp": {"PageUp", "PageUp", "", 33, 0}, "PageDown": {"PageDown", "PageDown", "", 34, 0},
	"ArrowLeft": {"ArrowLeft", "ArrowLeft", "", 37, 0}, "ArrowUp": {"ArrowUp", "ArrowUp", "", 38, 0},
	"ArrowRight": {"ArrowRight", "ArrowRight", "", 39, 0}, "ArrowDown": {"ArrowDown", "ArrowDown", "", 40, 0},
	"Space": {" ", "Space", " ", 32, 0},
	"Alt":   {"Alt", "AltLeft", "", 18, 1}, "Control": {"Control", "ControlLeft", "", 17, 2},
	"Meta": {"Meta", "MetaLeft", "", 91, 4}, "Shift": {"Shift", "ShiftLeft", "", 16, 8},
}

func parseKey(input string) ([]keyStroke, error) {
	invalid := func() ([]keyStroke, error) {
		return nil, failure("invalid_input", "expected a named key or one character with optional Control, Alt, Meta, or Shift modifiers")
	}
	if !utf8.ValidString(input) || input == "" {
		return invalid()
	}
	parts := strings.Split(input, "+")
	if input == "+" {
		parts = []string{input}
	}
	keys := []keyStroke{}
	modifiers := 0
	for _, name := range parts[:len(parts)-1] {
		if name == "Ctrl" {
			name = "Control"
		}
		if name == "Cmd" {
			name = "Meta"
		}
		key, ok := namedKeys[name]
		if !ok || key.modifier == 0 || modifiers&key.modifier != 0 {
			return invalid()
		}
		modifiers |= key.modifier
		keys = append(keys, key)
	}
	last := parts[len(parts)-1]
	key, ok := namedKeys[last]
	if !ok {
		if utf8.RuneCountInString(last) != 1 {
			return invalid()
		}
		r, _ := utf8.DecodeRuneInString(last)
		if r < 32 || r == 127 {
			return invalid()
		}
		key = keyStroke{key: last, text: last}
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			upper := strings.ToUpper(last)
			key.code = "Key" + upper
			key.virtual = int(upper[0])
			if modifiers&8 != 0 {
				key.key = upper
				key.text = upper
			}
			if modifiers&(1|2|4) != 0 {
				key.key = strings.ToLower(last)
				if modifiers&8 != 0 {
					key.key = upper
				}
			}
		case r >= '0' && r <= '9':
			key.code = "Digit" + last
			key.virtual = int(r)
		case r == ' ':
			key = namedKeys["Space"]
		}
	}
	if key.modifier != 0 && modifiers&key.modifier != 0 {
		return invalid()
	}
	if modifiers&(1|2|4) != 0 {
		key.text = ""
	}
	return append(keys, key), nil
}

// Press sends one key (for example Enter or ArrowDown) or modifier combination
// (for example Control+A or Shift+Tab) to the tab's focused element.
func (p *Page) Press(ctx context.Context, key string) (_ ActionResult, err error) {
	keys, err := parseKey(key)
	if err != nil {
		return ActionResult{}, err
	}
	if err := p.lock(ctx); err != nil {
		return ActionResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return ActionResult{}, err
	}
	observation, err := p.beginInput(ctx, "press", 0)
	if err != nil {
		return ActionResult{}, err
	}
	defer p.endInput(observation)
	defer func() { err = inputError(err, observation.warnings) }()
	if err := p.client.call(ctx, p.state.session, "Page.bringToFront", nil, nil); err != nil {
		return ActionResult{}, err
	}
	// Foregrounding can resume independent iframe work. Settle it before
	// establishing the keyboard-input boundary.
	if err := p.settleFrame(ctx, observation.documents[0]); err != nil {
		return ActionResult{}, err
	}
	if err := observation.startInput(ctx); err != nil {
		return ActionResult{}, err
	}
	modifiers := 0
	for _, key := range keys {
		modifiers |= key.modifier
		if err := p.dispatchKey(ctx, key, "keyDown", modifiers); err != nil {
			return ActionResult{}, err
		}
	}
	for i := len(keys) - 1; i >= 0; i-- {
		modifiers &^= keys[i].modifier
		if err := p.dispatchKey(ctx, keys[i], "keyUp", modifiers); err != nil {
			return ActionResult{}, err
		}
	}
	return p.completeInput(ctx, observation, observation.documents[0])
}
func (p *Page) dispatchKey(ctx context.Context, key keyStroke, kind string, modifiers int) error {
	text := ""
	if kind == "keyDown" {
		text = key.text
	}
	return p.client.call(ctx, p.state.session, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": key.key, "code": key.code, "text": text, "windowsVirtualKeyCode": key.virtual, "modifiers": modifiers}, nil)
}
