package cdp

import (
	"context"
	"encoding/json"
)

// EvalResult contains copied JSON or an explicit non-JSON JavaScript value.
// It contains no connection-scoped remote handle.
type EvalResult struct {
	Type                string          `json:"type"`
	Value               json.RawMessage `json:"value,omitempty"`
	UnserializableValue string          `json:"unserializableValue,omitempty"`
}

func (r EvalResult) String() string {
	if r.UnserializableValue != "" {
		return r.UnserializableValue
	}
	if r.Type == "undefined" {
		return "undefined"
	}
	return string(r.Value)
}

type remoteObject struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	Value          json.RawMessage `json:"value"`
	Unserializable string          `json:"unserializableValue"`
	ID             string          `json:"objectId"`
}
type runtimeResult struct {
	Result    remoteObject    `json:"result"`
	Exception json.RawMessage `json:"exceptionDetails"`
}

// Eval evaluates JavaScript in this page and awaits a returned promise. It does
// not replay evaluation after a timeout or disconnect. Exceptions are errors.
func (p *Page) Eval(ctx context.Context, expression string) (EvalResult, error) {
	if err := p.lock(ctx); err != nil {
		return EvalResult{}, err
	}
	defer p.unlock()
	if err := p.attach(ctx); err != nil {
		return EvalResult{}, err
	}
	session := p.state.session
	defer p.client.call(ctx, session, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": "browser-use-cli-eval"}, nil)
	var result runtimeResult
	if err := p.client.call(ctx, session, "Runtime.evaluate", map[string]any{
		"expression": expression, "awaitPromise": true, "objectGroup": "browser-use-cli-eval",
	}, &result); err != nil {
		return EvalResult{}, err
	}
	if result.Exception != nil {
		return EvalResult{}, failure("javascript", "JavaScript evaluation threw an exception")
	}
	value := result.Result
	if value.Type == "function" || value.Type == "symbol" || (value.ID != "" && value.Type != "object") || (value.Subtype != "" && value.Subtype != "array" && value.Subtype != "null") {
		return EvalResult{}, failure("unsupported", "JavaScript value cannot be copied as JSON")
	}
	if value.ID != "" {
		var copied runtimeResult
		if err := p.client.call(ctx, session, "Runtime.callFunctionOn", map[string]any{
			"objectId": value.ID, "functionDeclaration": copyJSONValue, "returnByValue": true,
		}, &copied); err != nil {
			return EvalResult{}, err
		}
		if copied.Exception != nil {
			return EvalResult{}, failure("javascript", "JavaScript value could not be copied")
		}
		var envelope struct {
			Supported *bool           `json:"supported"`
			Value     json.RawMessage `json:"value"`
		}
		if json.Unmarshal(copied.Result.Value, &envelope) != nil || envelope.Supported == nil {
			return EvalResult{}, failure("protocol", "browser returned an invalid copied value")
		}
		if !*envelope.Supported {
			return EvalResult{}, failure("unsupported", "JavaScript value cannot be copied as JSON without loss")
		}
		value.Value = envelope.Value
	}
	return EvalResult{Type: value.Type, Value: value.Value, UnserializableValue: value.Unserializable}, nil
}

// Copy properties once and reject values JSON would silently omit or replace.
// Shared acyclic objects are valid; a cycle in the current path is not.
const copyJSONValue = `function(){
 const path = new Set(), unsupported = {};
 function copy(value) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
  if (typeof value === 'number') {
   if (!Number.isFinite(value) || Object.is(value, -0)) throw unsupported;
   return value;
  }
  if (typeof value !== 'object' || path.has(value)) throw unsupported;
  const array = Array.isArray(value), proto = Object.getPrototypeOf(value);
  if (!array && proto !== Object.prototype && proto !== null) throw unsupported;
  if (Object.getOwnPropertySymbols(value).some(key => Object.prototype.propertyIsEnumerable.call(value,key))) throw unsupported;
  path.add(value);
  let result;
  if (array) {
   result = [];
   for (let i=0; i<value.length; i++) result.push(copy(value[i]));
   if (Object.keys(value).some(key => !/^(0|[1-9][0-9]*)$/.test(key) || Number(key)>=value.length)) throw unsupported;
  } else {
   result = Object.create(null);
   for (const key of Object.keys(value)) result[key] = copy(value[key]);
  }
  path.delete(value);
  return result;
 }
 try { return {supported:true,value:copy(this)}; }
 catch (error) { if (error === unsupported) return {supported:false}; throw error; }
}`
