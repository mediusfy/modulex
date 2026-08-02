package approval

import (
	"errors"
	"fmt"

	"github.com/mediusfy/modulex/contract"
	"github.com/mediusfy/modulex/provenance"
)

// ErrCommandNotFound is returned by [RequiresApproval] when commandName
// does not match any [github.com/mediusfy/modulex/contract.CommandDecl.Name]
// in the given contract. See [RequiresApproval]'s doc comment for why a
// caller must treat this as "requires approval", not as "does not require
// approval" — the (false, err) return shape keeps the boolean itself always
// strictly meaningful, but the error is not a green light.
var ErrCommandNotFound = errors.New("approval: command not found in contract")

// RequiresApproval reports whether commandName, as declared by c.Commands,
// requires human approval before running: true if its
// [github.com/mediusfy/modulex/provenance.CommandClass] is
// provenance.ClassApprovalRequired or provenance.ClassDestructive, false
// for provenance.ClassSafe, ClassMutating, or ClassNetworked.
func RequiresApproval(c contract.Contract, commandName string) (bool, error) {
	for _, cmd := range c.Commands {
		if cmd.Name != commandName {
			continue
		}
		switch cmd.Class {
		case provenance.ClassApprovalRequired, provenance.ClassDestructive:
			return true, nil
		default:
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: %q", ErrCommandNotFound, commandName)
}
