# ADR-0035: PR Review Service, Optional Feature, and Editor Integration

## Status

Proposed.

Supersedes the `feature/pr-review-pipeline` branch's initial approach, which
wired modulex to a reusable workflow in the private `mediusfy/pr_pipeline`
repository. That approach is unshippable as written: `modulex` is public and
`pr_pipeline` is private, a public repository may not call a private
repository's reusable workflow, `pr_pipeline`'s Actions access policy is
`none`, and the referenced workflow file does not exist — a PR-event run has
already failed at workflow resolution in 0 seconds. This ADR records the
decision to make the PR-review capability a public service of its own, an
optional modulex feature, and the basis for an editor plugin, and to unblock
the integration by removing the public/private mismatch entirely.

## Context

Modulex already contains most of a deterministic PR-review engine, built for
ADR-0032 ("Agent-First Development Experience"):

- `review` — changed-file listing, secret scanning, protected-path
  enforcement, and the aggregate `Review` pass;
- `verify` — focused-check planning and full-gate execution;
- `contract` — the `modulex.agent.yaml` schema declaring commands, gates, and
  protected paths;
- `provenance` — a versioned, redacted, self-validating `Envelope` that is
  already the natural machine-readable result of a review;
- `discovery`, `semindex`, `approval`, `patchapply` — supporting leaf
  packages;
- `tools/mcpserver` — a read-only MCP server exposing `discover_repository`,
  `read_contract`, `recommend_verification`, `run_verification`,
  `review_diff`, and `create_handoff`;
- `tools/agentcli` — the `modulex agent` CLI (today only `generate` and
  `approve` subcommands).

These packages are standalone and technology-neutral. What does not yet exist
is a stable, first-class way to run the whole review from outside a Go process
and get one artifact back: the CLI has no `review` or `handoff` subcommand, so
the only non-Go entry point today is the MCP server, which targets an
interactive agent rather than a CI runner.

Separately, the project wants three things from this capability: a standalone
service any repository can adopt, an optional feature of modulex itself, and an
IntelliJ/VSCode plugin. The failed private-reusable-workflow approach conflates
"the review engine" with "the CI plumbing that runs it" and locks both inside a
private repository that public callers cannot reach.

## Decision

Modulex will treat the deterministic review engine as a versioned product with
several delivery adapters layered on top of it. The engine stays in modulex;
every adapter is thin, exactly as ADR-0032 requires ("the MCP server must call
the same domain and CLI APIs rather than implementing a second source of
repository logic").

| Delivery mode | Setup | Idle cost | Secret custody | Offline |
| --- | --- | --- | --- | --- |
| Reusable workflow | copy caller YAML | $0 (GitHub-run) | caller's Actions secrets | no |
| Hosted GCP App | install the App | fixed storage cents | installation-configured | no |
| Local editor plugin | install extension | $0 (local compute) | local environment | yes |

The CLI (`modulex agent review`/`handoff`) is the engine's entry point that the
reusable workflow and any script shell out to, not a separate delivery mode of
its own.

### 1. The engine lives in modulex, with a stable non-Go entry point

The `provenance.Envelope` is the wire contract between the engine and every
consumer: a redacted, validated, versioned JSON document. Modulex will add
`modulex agent review` and `modulex agent handoff` CLI subcommands that run
`review.Review` (plus `verify` and the git-ref plumbing the MCP tools already
use) and emit an `Envelope` to stdout, so CI and editors can shell out without
embedding Go or speaking MCP. The MCP `review_diff`/`create_handoff` tools and
these CLI subcommands are two faces of one code path, never two
implementations.

Because the `Envelope` is now the contract shared across the CLI, the reusable
workflow, the hosted App, and the editor plugins, its schema is a compatibility
surface. `provenance.SchemaVersion` already carries a SemVer version with a
documented bump policy (minor for additive fields, major for breaking changes);
this ADR additionally requires every consumer to validate the version and
degrade gracefully on an unknown minor (render known fields, ignore unknown
ones) rather than fail, so an older editor plugin keeps working against a newer
CLI/App release.

The engine is deterministic, read-only, and fail-closed by construction (an
unknown command classifies as approval-required, an inability to compute a diff
reports `unavailable`, never a pass). The AI-review layer is explicitly *not*
part of the engine: the engine's output is auditable without a model in the
loop.

### 2. `pr_pipeline` becomes a public service: a public reusable workflow

`mediusfy/pr_pipeline` will be made public and will expose a public
`workflow_call` reusable workflow that any repository — modulex included —
invokes. A public caller may call a public reusable workflow, which removes all
three blockers of the original approach (visibility mismatch, access policy,
missing file) without hosting anything.

The reusable workflow depends on modulex as a library/CLI, runs the engine to
produce an `Envelope`, and adds only the AI-review commentary and the
PR-comment posting on top. The AI layer is keyed by the *caller's* own secrets
(passed explicitly, never `secrets: inherit`), so `pr_pipeline` custodies no
third-party keys and each adopting repository controls its own model spend and
token scope.

The caller side in this repository will follow the repository's existing CI
conventions, which the current draft violates:

- pin the reusable workflow to a tag or commit SHA, never a mutable `@main`
  (matching `ci.yml`/`release.yml`/`scorecard.yml`);
- pass named secrets explicitly rather than `secrets: inherit`;
- declare a least-privilege top-level `permissions` block
  (`contents: read`, `pull-requests: write`);
- guard against fork PRs, which receive no secrets on a public repository, so
  external contributors never see an unfixable red check;
- trigger on `pull_request`, never `pull_request_target`: the latter runs in
  the base-repo context *with* secrets while checking out untrusted fork code,
  an RCE footgun. Model commentary on fork PRs stays disabled unless the repo
  deliberately gates it behind an environment approval;
- document the secret names the pipeline actually consumes.

### 3. The editor plugin drives the local MCP server

The IntelliJ and VSCode plugins talk to the local modulex MCP server
(`tools/mcpserver`) against the developer's checkout. This needs no hosted
backend, works offline, and reuses the read-only tool surface that ships today:
recommend/run verification before a push, `review_diff` against a base branch,
and `create_handoff` for a provenance artifact. The plugin is a client of the
same engine the CI service uses; a PR reviewed locally and in CI runs identical
check logic.

### 4. An optional hosted GitHub App on GCP, for repositories that want zero setup

The reusable workflow already satisfies scale-to-zero, tenant isolation, and
prior-PR memory for free: GitHub runs it only on a PR event (no idle cost, free
on public repos), runs each repository's Actions in isolation, and the PR
comment plus git history is the review memory. A GCP-hosted GitHub App is
therefore an adoption choice — zero setup for adopters, centralized control —
not a technical requirement for those properties.

For adopters who want it, modulex will offer a hosted GitHub App on Google
Cloud, designed to cost nothing while idle and to keep every installation's PRs
isolated and durable:

- **Scale to zero.** The App runs on Cloud Run with minimum instances 0. No PR
  activity means no running instance and no compute cost; a Go binary's cold
  start is within a webhook's tolerance because the webhook is acknowledged
  before the review runs (below). Durable state uses **Firestore, not Cloud
  SQL**: Cloud SQL bills an always-on instance and cannot scale to zero, which
  would defeat the requirement. The only non-zero idle costs are fixed cents —
  the container image in Artifact Registry and Firestore storage at rest.

- **Fast ack, async work.** The Cloud Run receiver verifies the webhook HMAC
  (`X-Hub-Signature-256`), deduplicates on the GitHub delivery ID (dedup
  records carry a native Firestore TTL, e.g. 24h, so they self-expire at zero
  storage cost with no cleanup worker), enqueues a job to Cloud Tasks, and
  returns 200 well inside GitHub's ~10-second budget. A
  Cloud Run worker (dispatched by Cloud Tasks, also scale-to-zero) runs the
  engine and the AI layer — which may take minutes — without holding the
  webhook open. Cloud Tasks provides durable retries so a transient failure
  re-runs rather than dropping the PR.

- **Hundreds of concurrent users, never mixed.** Cloud Run scales horizontally;
  each job is stateless and carries only its own `(installation, repo, PR)`
  identity. The App mints a short-lived installation token scoped to that one
  installation per job, so no request can read or write another tenant's repo.
  Firestore documents are partitioned by installation ID, accessed only
  server-side under a least-privilege service account with secrets in Secret
  Manager.

- **Remembers prior PRs.** A Firestore document per `(installation, repo, PR)`
  records the last-reviewed commit SHA, the prior review comment ID, and the
  handoff `Envelope`. This makes review incremental (only commits since the
  last review) and lets the App update its existing PR comment instead of
  posting a duplicate on every push.

- **No stale or overlapping reviews.** Rapid pushes to one PR produce several
  deliveries; the worker serializes per PR with a **Firestore lease**, not a
  per-PR Cloud Tasks queue — dynamic per-PR queues would hit Cloud Tasks'
  ~1,000-queues-per-project limit at scale. A transaction on the
  `(installation, repo, PR)` document holds `status` (idle/running),
  `target_sha`, and a lease expiry: a delivery arriving while a review is
  running only advances `target_sha`, and the running job, on completion,
  enqueues one follow-up if `target_sha` moved. Rapid force-pushes thus
  collapse to a single final review, and an older review can never overwrite a
  newer one. The lease expiry lets a crashed worker's PR be retried rather than
  wedged forever.

The hosted App is a third thin adapter over the same engine and the same
`Envelope`; it adds delivery and tenancy, never new check logic, and produces
identical review results to the reusable workflow for the same diff.

## Cost and metering

There are two distinct costs, tracked two different ways.

**Operator (GCP) cost is near-zero and bounded by free tiers.** With
min-instances 0, Cloud Run bills only per request and only while a review runs;
Cloud Tasks (first 1M ops/month free), Firestore (a free daily read/write/delete
allowance plus 1 GiB storage), Secret Manager, and Artifact Registry each cost
cents at this scale. Hundreds of light installations land within or just past
the free tiers, and there is no idle floor because nothing runs between PRs.
Track it with a Cloud Billing budget and alerts plus the BigQuery billing
export, with resources labeled per component.

**Model (AI) cost is the real expense — and the operator never pays it.**
Because the AI layer is keyed by each installation's own provider key
(decision 4), token spend is billed directly to that installation by its
provider, never to the service. Holding no central key means holding no
model-cost custody either.

**Per-installation usage is metered by the service, not by GCP.** GCP does not
attribute one shared service's cost per tenant, so the service keeps its own
ledger: the Firestore job record per `(installation, repo, PR)` that already
exists for isolation and memory is extended with a review count and the token
usage the model response reports. That gives each installation an accurate
"you reviewed N PRs / spent T tokens" view the operator never pays for, and is
the metering table any future paid tier would build on.

**Abuse is a cost vector even with caller-keyed models.** A runaway or hostile
installation still consumes the operator's Cloud Run/Firestore operations.
Per-installation rate limits and quotas — enforceable because every job already
carries a per-installation identity and token — cap that blast radius so one
tenant cannot loop the service past a free tier.

## Alternatives considered

### Hosted App as the *only* delivery mode

Ship only the hosted GCP App and drop the reusable workflow. Rejected: the
reusable workflow needs no hosting, already meets scale-to-zero/isolation/memory
for free, and is the natural fit for repositories that already run their own
Actions. The hosted App (decision 4) is additive, not a replacement — both are
first-class, thin adapters over one engine.

### Hosted App on an always-on datastore (Cloud SQL / AlloyDB / GKE)

A relational store or a Kubernetes deployment for the hosted App. Rejected: an
always-on database or node pool bills continuously and violates the
zero-idle-cost requirement. Firestore on Cloud Run keeps idle cost at fixed
storage cents; a relational store is reconsidered only if query needs outgrow
Firestore, and even then only via a serverless option.

### Centrally-held third-party model keys

Have the hosted App custody each adopter's model credentials. Rejected: the AI
layer is keyed by the installation's own configured secret, so the service
never becomes a custodian of third-party spend or a single high-value key
target.

### Reimplement or vendor the engine inside `pr_pipeline`

Give the service no modulex dependency. Rejected: it duplicates check logic
modulex already maintains and tests, and violates ADR-0032's single-source-of-
repository-logic rule — the two implementations would drift.

### Plugin over a hosted service API

Have the editor plugin call a hosted service rather than the local MCP server.
Rejected as the starting point: it depends on the hosted App existing first and
introduces an editor auth story, for a capability the local MCP server already
provides offline. A hosted API can augment the plugin later for full PR context.

### Keep the private reusable-workflow approach and fix the path

Rejected: even with the file created and the path corrected, a public repo
still cannot call a private repo's reusable workflow, and the access policy
still forbids it. The public/private mismatch is the root cause, not the path.

## Consequences

### Positive

- One audited, deterministic engine backs CI, the service, and the plugin.
- The public reusable workflow unblocks the integration with no hosting and no
  third-party secret custody.
- `provenance.Envelope` as the wire format means CI comments, editor panels,
  and audit logs all render the same validated artifact.
- The AI layer is optional and caller-keyed; the engine is useful and
  trustworthy without a model in the loop.
- The engine gains a first-class non-Go entry point (`modulex agent review`),
  closing a gap ADR-0032 left.

### Negative

- Making `pr_pipeline` public exposes its workflow source and requires a
  deliberate secrets review before the switch.
- Pinning the reusable workflow to a SHA/tag adds a version-bump step whenever
  `pr_pipeline` changes.
- The engine's output schema becomes a compatibility surface: once CI and a
  plugin consume `Envelope`, its versioning policy must be honored.
- Two editor plugins (IntelliJ, VSCode) are ongoing surface to build and keep
  in sync with the MCP tool schema.
- The hosted App adds operational surface: Cloud Run, Cloud Tasks, Firestore,
  Secret Manager, webhook signature/idempotency handling, and per-PR
  serialization all need building and monitoring — none of which the reusable
  workflow requires.
- Cloud Run min-instances 0 means cold starts on the first request after idle;
  acceptable here because review runs asynchronously, but a factor for any
  future synchronous endpoint.

## Non-goals

- Custodying adopters' model credentials: the AI layer is keyed by each
  installation's own configured secret, never a central key the service holds.
- An always-on datastore or Kubernetes deployment for the hosted App; the
  zero-idle-cost requirement rules those out (see Alternatives).
- Write-capable CI or editor actions (auto-fix, auto-merge); the engine and its
  MCP surface stay read-only per ADR-0032's MCP boundary.
- Replacing the repository's existing `ci.yml`/`release.yml` gates; PR review is
  additive, never a substitute for the full gates.

## Implementation plan

1. Add `modulex agent review` and `modulex agent handoff` CLI subcommands over
   the existing `review`/`verify`/`provenance` packages, emitting an
   `Envelope`, with table-driven tests.
2. Document the `Envelope` as the versioned review wire format and confirm the
   CLI and MCP paths share one implementation.
3. Make `pr_pipeline` public after a secrets/history review, and add a public
   `workflow_call` reusable workflow that depends on modulex and adds the
   caller-keyed AI-review + PR-comment layer.
4. Rewrite this repository's `.github/workflows/pr-review.yml` to call the
   public reusable workflow with a pinned ref, explicit named secrets, a
   least-privilege `permissions` block, and a fork-PR guard. This is a
   protected-path/CI change and requires explicit human approval per
   `docs/planning/agent-safety-policy.md`.
5. Build the VSCode plugin as a client of the local MCP server, then the
   IntelliJ plugin, sharing one description of the tool surface.
6. Build the optional hosted GitHub App on GCP: a Cloud Run receiver
   (HMAC-verify, dedupe on delivery ID, enqueue to Cloud Tasks, ack), a
   scale-to-zero Cloud Run worker running the engine + caller-keyed AI layer,
   Firestore state per `(installation, repo, PR)` with per-PR serialization,
   installation-scoped tokens, and Secret Manager. Deploy with min-instances 0
   and no always-on datastore so idle cost stays at fixed storage cents.

## Acceptance criteria

- `modulex agent review`/`handoff` produce a redacted, `Validate`-passing
  `Envelope`, covered by tests, and share code with the MCP tools.
- The `pr-review.yml` caller pins by SHA/tag, passes named secrets (no
  `secrets: inherit`), declares least-privilege permissions, and does not fail
  fork PRs.
- A PR-event run resolves and completes (no 0-second workflow-resolution
  failure).
- The engine remains read-only and fail-closed; no consumer can make it mutate
  a repository or external state.
- The editor plugin reviews a diff against a base ref via the local MCP server
  with no hosted backend.
- If built, the hosted App scales to zero: no running instance and no compute
  cost with no PR activity, and durable state is Firestore (no always-on
  database).
- Webhook deliveries are HMAC-verified and idempotent on the GitHub delivery
  ID; each job uses an installation-scoped token and cannot read or write
  another installation's data.
- Per-PR reviews are serialized and incremental via a Firestore lease: the App
  updates one comment rather than duplicating, and never posts a review for a
  superseded SHA, under hundreds of concurrent installations.
- The reusable workflow triggers on `pull_request`, never
  `pull_request_target`.
- Dedup records self-expire via Firestore TTL; per-installation usage is
  metered in the service's own store and rate-limited so one tenant cannot
  exhaust shared quotas; the operator's GCP spend is covered by a billing
  budget with alerts.
- `make test-arch`, `make build`, `make lint`, and `make test` pass.
- Any `.github/workflows/*.yml` change carries documented human approval.

## Related decisions and work

- [ADR-0032: Agent-First Development Experience](adr-0032-agent-first-development-experience.md)
- [ADR-0031: Modulex Value and Specialization Roadmap](adr-0031-modulex-value-and-specialization-roadmap.md)
- `docs/planning/agent-safety-policy.md` — protected paths and the CI-change
  approval boundary
- `mediusfy/pr_pipeline` — the service repository to be made public

## Addendum (2026-08-21): pr_pipeline stays private; modulex runs the engine natively

Decision 2's premise — make `pr_pipeline` public so a public caller can use
its reusable workflow — was revisited and rejected on exposure grounds: the
repository's full history references internal hostnames and infrastructure
layout, and publishing it bought modulex nothing that a native job doesn't.

Empirical finding (smoke-tested on modulex PR #124): GitHub refuses a
*public* caller resolving a *private* repository's reusable workflow — the
run fails at workflow resolution with "workflow was not found" — even with
the callee's Actions access policy set to organization-wide. The access
policy governs only private/internal callers.

Revised delivery for each consumer:

- **modulex (public)**: runs the deterministic engine natively in its own
  CI (`.github/workflows/pr-review.yml` builds `tools/agentcli` from the
  checkout and runs `modulex agent handoff`, uploading the Envelope as an
  artifact). No cross-repo resolution, no secrets in the job. Gating is
  split by what a failure means: every check except protected-paths fails
  the job (secret scan, declared make gates — real defects in the diff),
  while a protected-paths hit surfaces as a prominent reviewer warning
  instead of a red check. Protected paths measure "needs explicit human
  approval", and in PR CI the reviewer's approval of the PR is that
  approval; the check already exempts the routine cases (additive
  `## [Unreleased]` CHANGELOG edits via `changelogEditIsWithinUnreleased`,
  non-retract go.mod edits), so warnings appear only on genuinely
  sensitive paths like workflow files. AI commentary arrives via the
  badger App's
  `repository_dispatch` flow (`pr_pipeline`'s `pr-review-dispatch.yml`),
  which reviews any repository the App is installed on regardless of
  visibility.
- **Private org repositories (e.g. coding_pipeline)**: call the reusable
  workflow as designed. `pr_pipeline`'s Actions access policy was set to
  "organization" (2026-08-21) to permit exactly this.
- **External/customer repositories**: unchanged — the badger dispatch flow.

The engine job that PR #4 added to `pr-review-reusable.yml` remains correct
for private callers; modulex simply is not one of them.
