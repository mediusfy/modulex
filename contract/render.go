package contract

import (
	"fmt"
	"strings"
)

// RenderText renders c as a human-readable, multi-line summary, suitable
// for pasting into a PR description or printing to a terminal. This is the
// "human-readable agent guidance can be derived from the contract" piece
// of ADR-0032's acceptance criteria — the same spirit as verify.RenderText,
// adapted to a Contract's shape (projects, boundaries, protected paths,
// ...) rather than verification results.
//
// RenderText does not validate c; call c.Validate() first if that matters
// to the caller.
func RenderText(c Contract) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Modulex Agent Contract (schema %s)\n\n", c.SchemaVersion)

	if len(c.Projects) > 0 {
		b.WriteString("Projects:\n")
		for _, p := range c.Projects {
			fmt.Fprintf(&b, "  - %s (%s)", p.Name, p.Path)
			if p.ModulePath != "" {
				fmt.Fprintf(&b, " [module: %s]", p.ModulePath)
			}
			b.WriteString("\n")
			if p.Description != "" {
				fmt.Fprintf(&b, "      %s\n", p.Description)
			}
			for _, root := range p.CompositionRoots {
				fmt.Fprintf(&b, "      composition root: %s\n", root)
			}
		}
		b.WriteString("\n")
	}

	if len(c.Instructions.Files) > 0 {
		b.WriteString("Instruction files (precedence order):\n")
		for _, f := range c.Instructions.Files {
			fmt.Fprintf(&b, "  %d. %s", f.Priority, f.Path)
			if f.Notes != "" {
				fmt.Fprintf(&b, " — %s", f.Notes)
			}
			b.WriteString("\n")
		}
		if c.Instructions.Rule != "" {
			fmt.Fprintf(&b, "  precedence rule: %s\n", c.Instructions.Rule)
		}
		b.WriteString("\n")
	}

	if len(c.Boundaries) > 0 {
		b.WriteString("Boundaries:\n")
		for _, bound := range c.Boundaries {
			fmt.Fprintf(&b, "  - %s", bound.Name)
			if bound.Rule != "" {
				fmt.Fprintf(&b, ": %s", bound.Rule)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(c.Commands) > 0 {
		b.WriteString("Commands:\n")
		for _, cmd := range c.Commands {
			fmt.Fprintf(&b, "  [%s] %s (%s)", strings.ToUpper(string(cmd.Class)), cmd.Name, cmd.Command)
			if cmd.Reason != "" {
				fmt.Fprintf(&b, " — %s", cmd.Reason)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(c.Verification.Focused) > 0 || len(c.Verification.Full) > 0 {
		b.WriteString("Verification:\n")
		for _, chk := range c.Verification.Focused {
			fmt.Fprintf(&b, "  focused: %s (%s)\n", chk.Name, chk.Command)
		}
		for _, chk := range c.Verification.Full {
			fmt.Fprintf(&b, "  full: %s (%s)\n", chk.Name, chk.Command)
		}
		b.WriteString("\n")
	}

	if len(c.ProtectedPaths) > 0 {
		b.WriteString("Protected paths (require explicit human approval to modify):\n")
		for _, p := range c.ProtectedPaths {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
		b.WriteString("\n")
	}

	if len(c.GeneratedPaths) > 0 {
		b.WriteString("Generated paths (do not hand-edit):\n")
		for _, p := range c.GeneratedPaths {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
		b.WriteString("\n")
	}

	if len(c.RequiredTools) > 0 {
		fmt.Fprintf(&b, "Required tools: %s\n\n", strings.Join(c.RequiredTools, ", "))
	}

	if len(c.OptionalServices) > 0 {
		b.WriteString("Optional services:\n")
		for _, s := range c.OptionalServices {
			fmt.Fprintf(&b, "  - %s", s.Name)
			if s.Description != "" {
				fmt.Fprintf(&b, ": %s", s.Description)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(c.RequiredCredentials) > 0 {
		fmt.Fprintf(&b, "Required credentials (names only, no values): %s\n\n", strings.Join(c.RequiredCredentials, ", "))
	}

	if c.HandoffFormat != "" {
		fmt.Fprintf(&b, "Handoff format: %s\n", c.HandoffFormat)
	}

	return b.String()
}
