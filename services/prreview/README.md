# prreview — the hosted PR-review GitHub App (GCP)

The optional hosted delivery of the modulex PR-review engine (ADR-0035
plan step 6; Jira MOD-83 … MOD-88): zero setup for adopters, zero idle
cost for the operator. A third thin adapter over the same engine as CI and
the editor plugins — it adds delivery and tenancy, never new check logic.

```
GitHub webhook ──> receiver (Cloud Run, min-instances 0)
                     HMAC verify → dedup (Firestore TTL) → quota → enqueue → 2xx
                          │
                     Cloud Tasks (durable retries, OIDC-authenticated)
                          ▼
                   worker (Cloud Run, min-instances 0)
                     Firestore lease → installation token → shallow fetch
                     → engine review → caller-keyed AI commentary
                     → create/update the single PR comment → ledger
                     → follow-up task if the target SHA moved
```

## Invariants

- **Scale to zero (MOD-88):** no PR activity means no running instance and
  no compute cost. Firestore only — no always-on datastore.
- **Fast ack (MOD-83):** the receiver acks well inside GitHub's ~10s
  budget; reviews never run in the webhook request.
- **Idempotent (MOD-83):** deliveries dedupe on the GitHub delivery ID;
  dedup docs carry a native Firestore TTL and self-expire.
- **Serialized per PR (MOD-85):** a Firestore lease
  (status/target_sha/lease expiry) collapses rapid pushes to one final
  review, never posts a superseded SHA, updates one comment in place, and
  lets a crashed worker's PR retry via lease expiry. The state machine is
  pure (`store.AcquireDecision`/`store.CompleteDecision`) and
  concurrency-tested.
- **Tenant isolation (MOD-86):** a short-lived installation-scoped token
  per job; Firestore partitioned by installation ID; AI keys in Secret
  Manager under `prreview-ai-<installation>`; per-installation
  rate limits cap one tenant's blast radius.
- **Caller-keyed AI (decision 4):** commentary runs on the installation's
  own Anthropic key and model choice — the operator holds no model key and
  pays no model cost. AI failure (including a policy refusal) degrades to
  an engine-only review, never a dropped one.
- **Metered (MOD-87):** the Firestore job record and per-installation
  ledger count reviews and reported token usage; the operator's GCP spend
  is bounded by a billing budget with alerts (infra/prreview).

## Layout

Every external dependency sits behind a small interface with an in-memory
implementation, so the full pipeline is table-tested without GCP:

| Package | Role | Production impl |
|---|---|---|
| `webhook` | HMAC verify + event parsing | pure |
| `receiver` | webhook HTTP handler | pure over interfaces |
| `store` | lease state machine, dedup, ledger | Firestore |
| `queue` | task hand-off | Cloud Tasks |
| `worker` | job orchestration | pure over interfaces |
| `engine` | review engine + checkout | modulex packages + git |
| `ai` | caller-keyed commentary | Anthropic Go SDK |
| `githubauth` | App JWT → installation token; PR comments | GitHub REST |
| `tenants` | per-installation AI config | Secret Manager |
| `ratelimit` | per-installation quota | in-process |

## Deploy

Infrastructure lives in `infra/prreview` (Terraform). Applying it is an
infrastructure change requiring explicit human approval
(docs/planning/agent-safety-policy.md); `terraform plan` is safe. Secret
*values* (webhook secret, App private key, per-installation AI keys) are
added by a human with `gcloud secrets versions add`, never via Terraform.

Build images from the repository root:

```sh
docker build -f services/prreview/Dockerfile --target receiver -t <registry>/prreview-receiver .
docker build -f services/prreview/Dockerfile --target worker   -t <registry>/prreview-worker .
```
