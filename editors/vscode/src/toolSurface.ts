// Typed mirror of editors/tool-surface.json — the shared, editor-agnostic
// description of the read-only modulex MCP server's tool surface (ADR-0035
// plan step 5). The authoritative schemas are tools/mcpserver/*.go; update
// both this file and editors/tool-surface.json when they change.

export const TOOL = {
  discoverRepository: "discover_repository",
  readContract: "read_contract",
  recommendVerification: "recommend_verification",
  runVerification: "run_verification",
  reviewDiff: "review_diff",
  createHandoff: "create_handoff",
} as const;

export type VerificationStatus =
  "pass" | "fail" | "skipped" | "unavailable" | "approval_required";

// provenance.VerificationResult (snake_case json tags).
export interface VerificationResult {
  name: string;
  category: string;
  status: VerificationStatus;
  duration_ns?: number;
  message?: string;
  reason?: string;
}

// verify.CheckSpec has no json tags, so recommend_verification's plan uses
// Go-default PascalCase field names.
export interface PlanCheckSpec {
  Name: string;
  Command: string;
  Category: string;
  Reason?: string;
  RequiredTool?: string;
  Networked?: boolean;
}

export interface Plan {
  FocusedChecks: PlanCheckSpec[];
  FullGates: PlanCheckSpec[];
}

// tools/mcpserver CheckSpecIn (snake_case json tags) — run_verification input.
export interface CheckSpecIn {
  name: string;
  command: string;
  category: string;
  reason?: string;
  required_tool?: string;
  networked?: boolean;
}

export interface ReviewDiffResult {
  results: VerificationResult[];
}

export interface RunVerificationResult {
  results: VerificationResult[];
  approval_status?: Record<string, VerificationStatus>;
}

export interface RecommendVerificationResult {
  plan: Plan;
}

export interface ReadContractResult {
  present: boolean;
  contract?: unknown;
  validation_errors?: string[];
}

export interface DiscoverRepositoryResult {
  repository: unknown;
}

export interface CreateHandoffResult {
  envelope: unknown;
}

// planCheckToCheckSpecIn maps a PascalCase plan check (verify.CheckSpec) to
// the snake_case CheckSpecIn shape run_verification expects.
export function planCheckToCheckSpecIn(c: PlanCheckSpec): CheckSpecIn {
  const spec: CheckSpecIn = {
    name: c.Name,
    command: c.Command,
    category: c.Category,
  };
  if (c.Reason) {
    spec.reason = c.Reason;
  }
  if (c.RequiredTool) {
    spec.required_tool = c.RequiredTool;
  }
  if (c.Networked) {
    spec.networked = c.Networked;
  }
  return spec;
}
