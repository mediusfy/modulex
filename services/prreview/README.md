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

## Per-installation AI keys

Enable AI commentary for an installation by creating its secret
(`infra/prreview` grants the worker a prefix-conditioned accessor on every
`prreview-ai-*` secret, so no per-secret IAM step is needed — creating the
secret is enough):

```sh
printf '%s' '{"provider": "anthropic", "api_key": "sk-ant-api03-...", "model": "claude-opus-5"}' | \
  gcloud secrets create prreview-ai-<installation-id> --data-file=-
```

`provider` selects the backend (`ai.Dispatch` in `services/prreview/ai`):

| Provider | Required fields | Notes |
|---|---|---|
| `anthropic` (default) | `api_key` | `model` optional, defaults to `claude-opus-5`. Must be a Console API key (`sk-ant-api03-...`), **not** a `claude auth login` OAuth session token (`sk-ant-oat01-...`) — those are short-lived and meant for personal interactive use, not a caller-keyed automated backend. |
| `openai` | `api_key`, `model` | Calls `<base_url or api.openai.com/v1>/chat/completions`. |
| `deepseek` | `api_key`, `model` | Calls `<base_url or api.deepseek.com>/chat/completions`. |
| `ollama` | `base_url`, `model` | Calls `<base_url>/v1/chat/completions`. `api_key` is optional (only needed if the installation's Ollama server sits behind an authenticating proxy). **The installation supplies and owns this server** — modulex never runs or pays for it, and the server must be reachable over HTTP(S) from Cloud Run, not just from the installation's own network. |

Omitting `provider` (or using a bare, unquoted key with no JSON wrapper —
the format from before multi-provider support) defaults to `anthropic`.

Without an enabled config, the installation's reviews post engine-only —
never an error, and never a dropped review.

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
