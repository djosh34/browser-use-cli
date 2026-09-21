package cdp

import (
	"context"
	"encoding/json"
)

type viewportRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func outsideViewport(bounds, viewport viewportRect) bool {
	return bounds.X+bounds.Width <= viewport.X || bounds.Y+bounds.Height <= viewport.Y || bounds.X >= viewport.X+viewport.Width || bounds.Y >= viewport.Y+viewport.Height
}

func (p *Page) viewport(ctx context.Context, doc documentCapture) (viewportRect, error) {
	var world struct {
		ID int64 `json:"executionContextId"`
	}
	if err := p.client.call(ctx, doc.session, "Page.createIsolatedWorld", map[string]any{"frameId": doc.frame.ID, "worldName": "browser-use-cli"}, &world); err != nil {
		return viewportRect{}, err
	}
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	err := p.client.call(ctx, doc.session, "Runtime.evaluate", map[string]any{
		"contextId": world.ID, "expression": "({x:scrollX,y:scrollY,width:innerWidth,height:innerHeight})", "returnByValue": true,
	}, &result)
	if err != nil {
		return viewportRect{}, err
	}
	if result.Exception != nil {
		return viewportRect{}, failure("unavailable", "frame context changed during collection")
	}
	var viewport viewportRect
	if json.Unmarshal(result.Result.Value, &viewport) != nil || viewport.Width <= 0 || viewport.Height <= 0 {
		return viewportRect{}, failure("unavailable", "frame has no viewport")
	}
	return viewport, nil
}
