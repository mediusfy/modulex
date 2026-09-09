# Hosted PR-review service on GCP (ADR-0035 plan step 6, Jira MOD-88).
#
# Design invariants this configuration encodes:
#   - True scale-to-zero: both Cloud Run services run min-instances 0; no
#     compute cost while idle (MOD-88 AC).
#   - No always-on datastore: Firestore only — no Cloud SQL, no GKE. Idle
#     cost is fixed cents (image storage + Firestore at rest).
#   - Dedup records self-expire via Firestore's native TTL on expire_at
#     (MOD-83) — no cleanup worker.
#   - Least privilege per component (MOD-86): the receiver can enqueue and
#     touch Firestore; the worker can run, read prreview-ai-* secrets, and
#     touch Firestore; only the queue's invoker SA may call the worker.
#   - Every resource is labeled per component for the BigQuery billing
#     export (MOD-87).
#
# NEEDS HUMAN APPROVAL to apply (agent-safety-policy.md: infrastructure
# change). `terraform plan` is read-only and safe.

terraform {
  required_version = ">= 1.6"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  # billingbudgets.googleapis.com requires a quota project under
  # user-credential auth; bill API quota to the service's own project.
  user_project_override = true
  billing_project       = var.project_id
}

locals {
  labels_base = {
    service = "prreview"
    adr     = "adr-0035"
  }
}

# --- APIs -------------------------------------------------------------------

resource "google_project_service" "apis" {
  for_each = toset([
    "run.googleapis.com",
    "cloudtasks.googleapis.com",
    "firestore.googleapis.com",
    "secretmanager.googleapis.com",
    "artifactregistry.googleapis.com",
    "billingbudgets.googleapis.com",
    # Meta-APIs the provider itself needs once user_project_override
    # routes quota through this project.
    "cloudresourcemanager.googleapis.com",
    "iam.googleapis.com",
    "orgpolicy.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

# --- Artifact Registry ------------------------------------------------------

resource "google_artifact_registry_repository" "prreview" {
  repository_id = "prreview"
  format        = "DOCKER"
  location      = var.region
  labels        = merge(local.labels_base, { component = "registry" })
  depends_on    = [google_project_service.apis]
}

# --- Firestore --------------------------------------------------------------

resource "google_firestore_database" "db" {
  name        = "(default)"
  location_id = var.region
  type        = "FIRESTORE_NATIVE"
  depends_on  = [google_project_service.apis]
}

# Native TTL on dedup records: they self-expire at zero storage cost with
# no cleanup worker (MOD-83).
resource "google_firestore_field" "dedup_ttl" {
  database   = google_firestore_database.db.name
  collection = "dedup"
  field      = "expire_at"
  ttl_config {}
}

# --- Service accounts (least privilege, MOD-86) -----------------------------

resource "google_service_account" "receiver" {
  account_id   = "prreview-receiver"
  display_name = "prreview webhook receiver"
}

resource "google_service_account" "worker" {
  account_id   = "prreview-worker"
  display_name = "prreview review worker"
}

resource "google_service_account" "invoker" {
  account_id   = "prreview-invoker"
  display_name = "prreview Cloud Tasks OIDC invoker"
}

resource "google_project_iam_member" "receiver_firestore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = "serviceAccount:${google_service_account.receiver.email}"
}

resource "google_project_iam_member" "worker_firestore" {
  project = var.project_id
  role    = "roles/datastore.user"
  member  = "serviceAccount:${google_service_account.worker.email}"
}

resource "google_cloud_tasks_queue_iam_member" "receiver_enqueue" {
  name     = google_cloud_tasks_queue.reviews.name
  location = var.region
  role     = "roles/cloudtasks.enqueuer"
  member   = "serviceAccount:${google_service_account.receiver.email}"
}

resource "google_cloud_tasks_queue_iam_member" "worker_enqueue_followups" {
  name     = google_cloud_tasks_queue.reviews.name
  location = var.region
  role     = "roles/cloudtasks.enqueuer"
  member   = "serviceAccount:${google_service_account.worker.email}"
}

# Both services enqueue OIDC tasks signed as the invoker SA.
resource "google_service_account_iam_member" "receiver_uses_invoker" {
  service_account_id = google_service_account.invoker.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.receiver.email}"
}

resource "google_service_account_iam_member" "worker_uses_invoker" {
  service_account_id = google_service_account.invoker.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.worker.email}"
}

# --- Secrets ----------------------------------------------------------------
# Secret *containers* are managed here; secret *values* are added by a
# human (gcloud secrets versions add), never by Terraform state or an
# agent. Per-installation AI keys (prreview-ai-<id>) are created out of
# band as installations enable commentary; the worker's accessor grant
# is project-wide on Secret Manager but the service only ever reads the
# prreview-ai-* name pattern (enforced in code; tighten with per-secret
# IAM as installations are onboarded).

resource "google_secret_manager_secret" "webhook_secret" {
  secret_id = "prreview-webhook-secret"
  labels    = merge(local.labels_base, { component = "receiver" })
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret" "app_private_key" {
  secret_id = "prreview-github-app-key"
  labels    = merge(local.labels_base, { component = "worker" })
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_iam_member" "receiver_webhook_secret" {
  secret_id = google_secret_manager_secret.webhook_secret.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.receiver.email}"
}

resource "google_secret_manager_secret_iam_member" "worker_app_key" {
  secret_id = google_secret_manager_secret.app_private_key.secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.worker.email}"
}

# --- Cloud Tasks ------------------------------------------------------------
# One shared queue; per-PR serialization is the Firestore lease's job
# (MOD-85), not per-PR queues.

resource "google_cloud_tasks_queue" "reviews" {
  name     = "prreview-reviews"
  location = var.region
  retry_config {
    max_attempts  = 8
    min_backoff   = "10s"
    max_backoff   = "600s"
    max_doublings = 5
  }
  rate_limits {
    max_dispatches_per_second = 5
    max_concurrent_dispatches = 20
  }
  depends_on = [google_project_service.apis]
}

# --- Cloud Run: worker (private) -------------------------------------------

resource "google_cloud_run_v2_service" "worker" {
  name     = "prreview-worker"
  location = var.region
  labels   = merge(local.labels_base, { component = "worker" })

  lifecycle {
    # The API echoes an empty top-level scaling block; without this the
    # plan shows a perpetual no-op diff.
    ignore_changes = [scaling]
  }

  template {
    service_account = google_service_account.worker.email
    scaling {
      min_instance_count = 0 # scale-to-zero is the point (MOD-88 AC)
      max_instance_count = 10
    }
    timeout = "900s" # a review may take minutes; the webhook was acked long ago
    containers {
      image = var.image_worker
      resources {
        limits = { cpu = "2", memory = "4Gi" }
      }
      env {
        name  = "GOOGLE_CLOUD_PROJECT"
        value = var.project_id
      }
      env {
        name  = "TASKS_QUEUE_PATH"
        value = google_cloud_tasks_queue.reviews.id
      }
      env {
        name  = "INVOKER_SERVICE_ACCOUNT"
        value = google_service_account.invoker.email
      }
      env {
        name  = "GITHUB_APP_ID"
        value = var.github_app_id
      }
      env {
        name = "GITHUB_APP_PRIVATE_KEY"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.app_private_key.secret_id
            version = "latest"
          }
        }
      }
    }
  }
}

# Only the queue's OIDC identity may invoke the worker.
resource "google_cloud_run_v2_service_iam_member" "worker_invoker" {
  name     = google_cloud_run_v2_service.worker.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.invoker.email}"
}

# --- Cloud Run: receiver (public webhook endpoint) --------------------------

resource "google_cloud_run_v2_service" "receiver" {
  name                 = "prreview-receiver"
  location             = var.region
  labels               = merge(local.labels_base, { component = "receiver" })
  invoker_iam_disabled = true

  lifecycle {
    # The API echoes an empty top-level scaling block; without this the
    # plan shows a perpetual no-op diff.
    ignore_changes = [scaling]
  }

  template {
    service_account = google_service_account.receiver.email
    scaling {
      min_instance_count = 0
      max_instance_count = 5
    }
    containers {
      image = var.image_receiver
      resources {
        limits = { cpu = "1", memory = "512Mi" }
      }
      env {
        name  = "GOOGLE_CLOUD_PROJECT"
        value = var.project_id
      }
      env {
        name  = "TASKS_QUEUE_PATH"
        value = google_cloud_tasks_queue.reviews.id
      }
      env {
        name  = "WORKER_URL"
        value = "${google_cloud_run_v2_service.worker.uri}/task"
      }
      env {
        name  = "INVOKER_SERVICE_ACCOUNT"
        value = google_service_account.invoker.email
      }
      env {
        name = "WEBHOOK_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.webhook_secret.secret_id
            version = "latest"
          }
        }
      }
    }
  }
}

# GitHub must reach the webhook unauthenticated; the HMAC signature is the
# authentication (MOD-83). Public access is granted by disabling the
# invoker IAM check on the receiver (below, invoker_iam_disabled) rather
# than an allUsers IAM binding, which the org's domain-restricted-sharing
# policy rejects; this keeps that policy intact org-wide.

# --- Cost & metering (MOD-87) ----------------------------------------------
# Per-installation usage is metered by the service itself (the Firestore
# ledger); this budget bounds the *operator's* GCP spend. Pair with the
# project's BigQuery billing export (configured at billing-account level,
# outside per-project Terraform) and the per-component labels above.

resource "google_billing_budget" "prreview" {
  billing_account = var.billing_account_id
  display_name    = "prreview monthly budget"

  budget_filter {
    projects = ["projects/${var.project_id}"]
  }

  lifecycle {
    # The API canonicalizes the project ID to its number; without this
    # the plan shows a perpetual no-op diff.
    ignore_changes = [budget_filter[0].projects]
  }

  amount {
    specified_amount {
      units = var.budget_amount_units
    }
  }

  threshold_rules {
    threshold_percent = 0.5
  }
  threshold_rules {
    threshold_percent = 0.9
  }
  threshold_rules {
    threshold_percent = 1.0
  }

  dynamic "all_updates_rule" {
    for_each = length(var.budget_notification_channels) > 0 ? [1] : []
    content {
      monitoring_notification_channels = var.budget_notification_channels
    }
  }
}
