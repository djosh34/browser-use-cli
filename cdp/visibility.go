package cdp

// clipRegion applies explicit hidden/clip overflow, not scrollable auto/scroll
// viewports. Observation still includes content reachable by ordinary scrolling.
type clipRegion struct {
	left, right, top, bottom float64
	horizontal, vertical     bool
}

func (c clipRegion) excludes(r viewportRect) bool {
	return (c.horizontal && (r.X+r.Width <= c.left || r.X >= c.right)) || (c.vertical && (r.Y+r.Height <= c.top || r.Y >= c.bottom))
}
func (s *domSnapshot) childClip(c clipRegion, layout *snapshotLayout) clipRegion {
	if layout == nil {
		return c
	}
	bounds := layout.Bounds
	clips := func(name string) bool { value := s.style(layout, name); return value == "hidden" || value == "clip" }
	if clips("overflow-x") {
		left, right := bounds.X, bounds.X+bounds.Width
		if c.horizontal {
			left = max(left, c.left)
			right = min(right, c.right)
		}
		c.left, c.right, c.horizontal = left, right, true
	}
	if clips("overflow-y") {
		top, bottom := bounds.Y, bounds.Y+bounds.Height
		if c.vertical {
			top = max(top, c.top)
			bottom = min(bottom, c.bottom)
		}
		c.top, c.bottom, c.vertical = top, bottom, true
	}
	return c
}
