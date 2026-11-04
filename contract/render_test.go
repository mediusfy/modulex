package contract

import (
	"strings"
	"testing"
)

func TestRenderText(t *testing.T) {
	tests := []struct {
		name      string
		contract  Contract
		wantInOut []string
	}{
		{
			name:     "valid contract output mentions project, protected path and command",
			contract: validContract(),
			wantInOut: []string{
				"modulex",                 // project name
				".github/workflows/*.yml", // a protected path
				"go-test",                 // a command name
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RenderText(tt.contract)
			for _, want := range tt.wantInOut {
				if !strings.Contains(out, want) {
					t.Errorf("RenderText output missing %q; got:\n%s", want, out)
				}
			}
		})
	}
}
