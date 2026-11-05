package modulex

// Structured-logging attribute keys emitted by the Manager. They are named
// constants (CODING_STANDARDS §7) so a key is never retyped as a literal.
const (
	logKeyService      = "service"
	logKeySpanName     = "span_name"
	logKeyTraceID      = "trace_id"
	logKeySpanID       = "span_id"
	logKeyParentSpanID = "parent_span_id"
	logKeyTask         = "task"
	logKeyPanic        = "panic"
	logKeyModule       = "module"
	logKeyPhase        = "phase"
	logKeyError        = "error"
	logKeyTaskCount    = "task_count"
)
