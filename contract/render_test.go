package contract

import (
	"strings"
	"testing"
)

func TestRenderText(t *testing.T) {
	c := validContract()
	out := RenderText(c)

	for _, want := range []string{
		"modulex",                 // project name
		".github/workflows/*.yml", // a protected path
		"go-test",                 // a command name
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderText output missing %q; got:\n%s", want, out)
		}
	}
}
