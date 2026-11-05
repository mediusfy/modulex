package apperrors_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mediusfy/modulex/apperrors"
)

const (
	errFirst  apperrors.Error = "first failure"
	errSecond apperrors.Error = "second failure"
)

func TestError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		target     error
		wantMsg    string
		wantIs     bool
		wantAsType bool
	}{
		{
			name:       "message is the constant string",
			err:        errFirst,
			target:     errFirst,
			wantMsg:    "first failure",
			wantIs:     true,
			wantAsType: true,
		},
		{
			name:       "different sentinels do not match",
			err:        errFirst,
			target:     errSecond,
			wantMsg:    "first failure",
			wantIs:     false,
			wantAsType: true,
		},
		{
			name:       "wrapped sentinel still matches",
			err:        fmt.Errorf("%w: %v", errSecond, "detail"),
			target:     errSecond,
			wantMsg:    "second failure: detail",
			wantIs:     true,
			wantAsType: true,
		},
		{
			name:       "foreign error is not an apperrors.Error",
			err:        errors.New("plain"),
			target:     errFirst,
			wantMsg:    "plain",
			wantIs:     false,
			wantAsType: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if got := errors.Is(tt.err, tt.target); got != tt.wantIs {
				t.Errorf("errors.Is = %v, want %v", got, tt.wantIs)
			}
			var as apperrors.Error
			if got := errors.As(tt.err, &as); got != tt.wantAsType {
				t.Errorf("errors.As(apperrors.Error) = %v, want %v", got, tt.wantAsType)
			}
		})
	}
}
