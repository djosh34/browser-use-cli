//go:build integration

package cdp_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func TestChromeEvalPreservesSpecialValuesAndRejectsLossyCopies(t *testing.T) {
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Page(testContext(t), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ expression, kind, text string }{
		{"undefined", "undefined", "undefined"}, {"NaN", "number", "NaN"}, {"Infinity", "number", "Infinity"}, {"-Infinity", "number", "-Infinity"}, {"-0", "number", "-0"}, {"123456789012345678901234567890n", "bigint", "123456789012345678901234567890n"}, {"null", "object", "null"},
	} {
		result, err := p.Eval(testContext(t), tc.expression)
		if err != nil || result.Type != tc.kind || result.String() != tc.text {
			t.Errorf("special %s: %+v %v", tc.expression, result, err)
		}
	}
	for _, expression := range []string{`(()=>{})`, `Symbol('x')`, `(()=>{const o={};o.self=o;return o})()`, `({f:()=>{}})`, `({x:undefined})`, `({x:NaN})`, `({x:-0})`, `({x:1n})`, `new Date()`, `document.body`, `({get value(){throw new Error('private-copy-error')}})`, `({nested:new Proxy({},{ownKeys(){throw new Error('private-copy-error')}})})`} {
		_, err := p.Eval(testContext(t), expression)
		var e *cdp.Error
		if !errors.As(err, &e) || e.Code != "unsupported" || strings.Contains(err.Error(), "private-copy-error") {
			t.Errorf("lossy value %s: %v", expression, err)
		}
	}
}

func TestChromeEvalCopiesValuesAndReportsExceptions(t *testing.T) {
	c := browserClient(t, chrome(t, "about:blank"))
	p, err := c.Page(testContext(t), "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Eval(testContext(t), `({greeting:'日本語',values:[1,true,null]})`)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Greeting string `json:"greeting"`
		Values   []any  `json:"values"`
	}
	if result.Type != "object" || json.Unmarshal(result.Value, &value) != nil || value.Greeting != "日本語" || len(value.Values) != 3 || value.Values[0] != float64(1) || value.Values[1] != true || value.Values[2] != nil {
		t.Fatalf("copied value: %+v", result)
	}
	_, err = p.Eval(testContext(t), `throw new Error('private-expression-content')`)
	var e *cdp.Error
	if !errors.As(err, &e) || e.Code != "javascript" || strings.Contains(err.Error(), "private-expression-content") {
		t.Fatalf("exception diagnostic: %v", err)
	}
	c.Close()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var copied cdp.EvalResult
	if err := json.Unmarshal(data, &copied); err != nil {
		t.Fatal(err)
	}
	if copied.String() != result.String() {
		t.Fatal("captured eval rendering differs after disconnect")
	}
}
