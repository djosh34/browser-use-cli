package cdp

import "context"

func (p *Page) isolatedWorld(ctx context.Context, doc documentCapture) (int64, error) {
	var world struct {
		ID int64 `json:"executionContextId"`
	}
	if err := p.client.call(ctx, doc.session, "Page.createIsolatedWorld", map[string]any{"frameId": doc.frame.ID, "worldName": "browser-use-cli"}, &world); err != nil {
		return 0, err
	}
	if world.ID == 0 {
		return 0, failure("unavailable", "frame has no execution context")
	}
	return world.ID, nil
}

// Cross a rendering boundary and a task, including native selection events.
// Hidden/offscreen documents can throttle animation frames: the private timer
// bounds that wait, rather than waiting for background animation indefinitely.
func (p *Page) settleFrame(ctx context.Context, doc documentCapture) error {
	world, err := p.isolatedWorld(ctx, doc)
	if err != nil {
		return err
	}
	expression := `new Promise(resolve=>{
 let frame=0,timer=0;
 const done=()=>{cancelAnimationFrame(frame);clearTimeout(timer);setTimeout(()=>{document.documentElement?.getBoundingClientRect();resolve(true)},0)};
 if(document.hidden)done();else{frame=requestAnimationFrame(done);timer=setTimeout(done,100)}
})`
	var result runtimeResult
	if err := p.client.call(ctx, doc.session, "Runtime.evaluate", map[string]any{"contextId": world, "expression": expression, "awaitPromise": true, "returnByValue": true}, &result); err != nil {
		return err
	}
	if result.Exception != nil {
		return failure("unavailable", "frame changed during input completion")
	}
	return nil
}
