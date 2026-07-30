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
//
// This derives approval policy entirely from data already declared in
// contract.Contract.Commands (each a contract.CommandDecl with a Class
// field) — it adds no new field to contract.Contract, per this ticket's
// scope: modifying contract/*.go is out of bounds, and the contract
// package's existing per-command Class is already exactly what ADR-0032's
// "Approval-required command classes are defined by the repository
// contract" acceptance criterion asks for.
//
// # Fail closed on an unknown command — the caller's responsibility
//
// If commandName does not appear in c.Commands at all, RequiresApproval
// returns (false, ErrCommandNotFound). It does not itself return (true,
// err): the boolean result is reserved exclusively for "the contract says
// this command's class is/isn't approval-required", so it never silently
// asserts a policy the contract didn't declare. This mirrors
// discovery.ClassifyCommand and verify's own "unknown = safest assumption"
// precedent (see docs/planning/agent-discovery-guide.md and
// docs/planning/agent-verification-guide.md), but the fail-safe action
// itself is the caller's job:
//
//	needsApproval, err := approval.RequiresApproval(c, "release")
//	if err != nil {
//	    // Unknown to the contract: fail closed. Treat exactly like
//	    // needsApproval == true rather than proceeding unchecked.
//	    needsApproval = true
//	}
//
// A caller that checks only `if err == nil && needsApproval` and otherwise
// proceeds unchecked has reintroduced the exact "missing approval fails
// open" defect this package exists to prevent — the fail-closed handling
// documented above is not automatic and must be applied by every caller.
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
