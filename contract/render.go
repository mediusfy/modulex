package contract

import (
	"fmt"
	"strings"
)

// RenderText renders c as a human-readable, multi-line summary, suitable
// for pasting into a PR description or printing to a terminal. This is the
// "human-readable agent guidance can be derived from the contract" piece
// of the acceptance criteria — the same spirit as verify.RenderText,
// adapted to a Contract's shape (projects, boundaries, protected paths,
// ...) rather than verification results.
//
// RenderText does not validate c; call c.Validate() first if that matters
// to the caller.
func RenderText(c Contract) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Modulex Agent Contract (schema %s)\n\n", c.SchemaVersion)

	renderProjects(&b, c.Projects)
	renderInstructions(&b, c.Instructions)
	renderBoundaries(&b, c.Boundaries)
	renderCommands(&b, c.Commands)
	renderVerification(&b, c.Verification)
	renderPathList(&b, "Protected paths (require explicit human approval to modify):", c.ProtectedPaths)
	renderPathList(&b, "Generated paths (do not hand-edit):", c.GeneratedPaths)
	renderNameList(&b, "Required tools", c.RequiredTools)
	renderServices(&b, c.OptionalServices)
	renderNameList(&b, "Required credentials (names only, no values)", c.RequiredCredentials)

	if c.HandoffFormat != "" {
		fmt.Fprintf(&b, "Handoff format: %s\n", c.HandoffFormat)
	}

	return b.String()
}

// renderNameList writes a single-line "label: a, b" section followed by a
// blank line; it writes nothing when names is empty.
func renderNameList(b *strings.Builder, label string, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(b, "%s: %s\n\n", label, strings.Join(names, ", "))
}

// renderPathList writes a titled bullet list followed by a blank line; it
// writes nothing when paths is empty.
func renderPathList(b *strings.Builder, title string, paths []string) {
	if len(paths) == 0 {
		return
	}
	b.WriteString(title + "\n")
	for _, p := range paths {
		fmt.Fprintf(b, "  - %s\n", p)
	}
	b.WriteString("\n")
}

func renderProjects(b *strings.Builder, projects []Project) {
	if len(projects) == 0 {
		return
	}
	b.WriteString("Projects:\n")
	for _, p := range projects {
		fmt.Fprintf(b, "  - %s (%s)", p.Name, p.Path)
		if p.ModulePath != "" {
			fmt.Fprintf(b, " [module: %s]", p.ModulePath)
		}
		b.WriteString("\n")
		if p.Description != "" {
			fmt.Fprintf(b, "      %s\n", p.Description)
		}
		for _, root := range p.CompositionRoots {
			fmt.Fprintf(b, "      composition root: %s\n", root)
		}
	}
	b.WriteString("\n")
}

func renderInstructions(b *strings.Builder, in InstructionPrecedence) {
	if len(in.Files) == 0 {
		return
	}
	b.WriteString("Instruction files (precedence order):\n")
	for _, f := range in.Files {
		fmt.Fprintf(b, "  %d. %s", f.Priority, f.Path)
		if f.Notes != "" {
			fmt.Fprintf(b, " — %s", f.Notes)
		}
		b.WriteString("\n")
	}
	if in.Rule != "" {
		fmt.Fprintf(b, "  precedence rule: %s\n", in.Rule)
	}
	b.WriteString("\n")
}

func renderBoundaries(b *strings.Builder, boundaries []Boundary) {
	if len(boundaries) == 0 {
		return
	}
	b.WriteString("Boundaries:\n")
	for _, bound := range boundaries {
		fmt.Fprintf(b, "  - %s", bound.Name)
		if bound.Rule != "" {
			fmt.Fprintf(b, ": %s", bound.Rule)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func renderCommands(b *strings.Builder, commands []CommandDecl) {
	if len(commands) == 0 {
		return
	}
	b.WriteString("Commands:\n")
	for _, cmd := range commands {
		fmt.Fprintf(b, "  [%s] %s (%s)", strings.ToUpper(string(cmd.Class)), cmd.Name, cmd.Command)
		if cmd.Reason != "" {
			fmt.Fprintf(b, " — %s", cmd.Reason)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func renderVerification(b *strings.Builder, v VerificationDecl) {
	if len(v.Focused) == 0 && len(v.Full) == 0 {
		return
	}
	b.WriteString("Verification:\n")
	for _, chk := range v.Focused {
		fmt.Fprintf(b, "  focused: %s (%s)\n", chk.Name, chk.Command)
	}
	for _, chk := range v.Full {
		fmt.Fprintf(b, "  full: %s (%s)\n", chk.Name, chk.Command)
	}
	b.WriteString("\n")
}

func renderServices(b *strings.Builder, services []OptionalService) {
	if len(services) == 0 {
		return
	}
	b.WriteString("Optional services:\n")
	for _, s := range services {
		fmt.Fprintf(b, "  - %s", s.Name)
		if s.Description != "" {
			fmt.Fprintf(b, ": %s", s.Description)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}
