-- 001_init_schema.sql — initial schema for the Postgres-backed prreview
-- store (../postgres.go), mirroring the Firestore collections it replaces:
-- per-(installation, repo, PR) job documents (lease + review memory +
-- usage), the flat delivery-dedup table, and the per-installation usage
-- ledger. See ../store.go for the field semantics and ../postgres.go for
-- how each table is read and written.
--
-- This repository has no migration tool wired up yet. Apply manually
-- against the target database:
--
--   psql "$POSTGRES_DSN" -f services/prreview/store/migrations/001_init_schema.sql
--
-- or with golang-migrate (https://github.com/golang-migrate/migrate), which
-- understands this NNN_description.sql naming directly once a matching
-- *.down.sql is added alongside it:
--
--   migrate -database "$POSTGRES_DSN" -path services/prreview/store/migrations up

CREATE TABLE IF NOT EXISTS prreview_jobs (
    installation_id   BIGINT      NOT NULL,
    owner             TEXT        NOT NULL,
    repo              TEXT        NOT NULL,
    pr_number         INTEGER     NOT NULL,
    status            TEXT        NOT NULL DEFAULT '',
    target_sha        TEXT        NOT NULL DEFAULT '',
    lease_expiry      TIMESTAMPTZ,
    lease_owner       TEXT        NOT NULL DEFAULT '',
    last_reviewed_sha TEXT        NOT NULL DEFAULT '',
    comment_id        BIGINT      NOT NULL DEFAULT 0,
    review_count      BIGINT      NOT NULL DEFAULT 0,
    tokens_used       BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (installation_id, owner, repo, pr_number)
);

CREATE TABLE IF NOT EXISTS prreview_dedup (
    delivery_id TEXT PRIMARY KEY,
    expire_at   TIMESTAMPTZ NOT NULL
);

-- No index on expire_at: every Seen/Forget call already looks up by the
-- primary key (delivery_id), and postgres.go's dedup TTL is a plain
-- "WHERE expire_at > now()" filter on that same single-row lookup, not a
-- scan ordered by expiry. An expire_at index would only help a periodic
-- cleanup job, and this implementation deliberately has none — see the
-- Postgres doc comment in ../postgres.go for why an unpruned expired row
-- is harmless rather than a correctness problem.

CREATE TABLE IF NOT EXISTS prreview_ledger (
    installation_id BIGINT PRIMARY KEY,
    reviews         BIGINT NOT NULL DEFAULT 0,
    tokens          BIGINT NOT NULL DEFAULT 0
);
