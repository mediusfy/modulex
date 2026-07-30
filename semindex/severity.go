package semindex

// Severity is how a caller should act on a Diagnosis: report it and move
// on, or treat it as a blocking failure. Modeled separately from Status
// because the same Status can warrant different treatment in different
// repositories or workflows — per ADR-0032's "Mismatch is a visible
// warning or failure according to contract policy," severity is a policy
// decision, not something Diagnose or Status hardcodes.
type Severity string

const (
	// SeverityOK means the diagnosis needs no action — it was StatusOK.
	SeverityOK Severity = "ok"
	// SeverityWarn means the diagnosis is non-OK but policy says to only
	// report it, not block.
	SeverityWarn Severity = "warn"
	// SeverityBlock means the diagnosis is non-OK and policy says it
	// should block the workflow (e.g. an agent should refuse to trust the
	// index's results, or a CI check should fail).
	SeverityBlock Severity = "block"
)

// Policy controls how EvaluateSeverity turns a non-OK Diagnosis into a
// Severity. Each field defaults to false (the zero value), meaning "warn,
// don't block" — the safer default for a caller that hasn't made an
// explicit policy decision yet: Severity never escalates a diagnosis to
// SeverityBlock without an explicit opt-in.
//
// This package deliberately does not require or import
// github.com/mediusfy/modulex/contract to express this. Forcing every
// caller of EvaluateSeverity to first construct a full contract.Contract
// value (parse YAML, populate required fields, call Validate) just to get
// a yes/no severity decision would be a far heavier dependency than the
// question warrants, and would pull contract — and, transitively,
// provenance — into what is otherwise a standalone, dependency-light leaf
// package. A caller that already has a contract.Contract (or any other
// policy source) is free to derive a Policy value from it — e.g. from a
// per-index-name entry in a future contract schema extension — before
// calling EvaluateSeverity; this package only needs the resulting yes/no
// decisions, never the schema they came from.
type Policy struct {
	// TreatMismatchAsFailure, if true, makes a StatusMismatch diagnosis
	// evaluate to SeverityBlock instead of SeverityWarn.
	TreatMismatchAsFailure bool
	// TreatMissingAsFailure, if true, makes a StatusMissing diagnosis
	// evaluate to SeverityBlock instead of SeverityWarn.
	TreatMissingAsFailure bool
	// TreatUnverifiableAsFailure, if true, makes a StatusUnverifiable
	// diagnosis evaluate to SeverityBlock instead of SeverityWarn.
	// Defaults to false: a real third-party index tool that has not
	// adopted this package's marker convention (or gained a custom
	// RootReader) will legitimately be StatusUnverifiable forever, and
	// treating that as a hard failure by default would make incremental
	// adoption impractical.
	TreatUnverifiableAsFailure bool
}

// EvaluateSeverity applies policy to d, returning SeverityOK for a passing
// diagnosis (StatusOK) and, for every other Status, SeverityWarn or
// SeverityBlock according to whichever of policy's fields corresponds to
// d.Status.
//
// EvaluateSeverity is a free function taking Policy as a plain parameter,
// not a required argument to Diagnose, so a caller who just wants a
// straightforward yes/no severity check can supply a zero-value Policy{}
// (or, for the common case of treating any non-OK diagnosis as a hard
// failure, a Policy with every field set to true) without constructing
// anything else.
func EvaluateSeverity(d Diagnosis, policy Policy) Severity {
	switch d.Status {
	case StatusOK:
		return SeverityOK
	case StatusMismatch:
		if policy.TreatMismatchAsFailure {
			return SeverityBlock
		}
		return SeverityWarn
	case StatusMissing:
		if policy.TreatMissingAsFailure {
			return SeverityBlock
		}
		return SeverityWarn
	case StatusUnverifiable:
		if policy.TreatUnverifiableAsFailure {
			return SeverityBlock
		}
		return SeverityWarn
	default:
		// An unrecognized Status (e.g. a future addition this version of
		// EvaluateSeverity doesn't know about yet) is treated as a warning,
		// never silently as SeverityOK.
		return SeverityWarn
	}
}
