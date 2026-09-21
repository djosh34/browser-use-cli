package cdp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/djosh34/browser-use-cli/cdp"
)

func TestMalformedReferencesFailLocally(t *testing.T) {
	for _, value := range []string{"", "not-a-reference", "cdp2.anything", "cdp1.!", "cdp1.e30", strings.Repeat("a", 17000)} {
		id, err := cdp.ControlRef(value).PageID()
		var e *cdp.Error
		if id != "" || !errors.As(err, &e) || e.Code != "invalid_input" {
			t.Errorf("malformed reference returned %q, %v", id, err)
		}
	}
}
