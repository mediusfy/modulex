# Inputs for the hosted PR-review service (ADR-0035 plan step 6, Jira
# MOD-88). Applying this configuration is an infrastructure change and
# requires explicit human approval per docs/planning/agent-safety-policy.md.

variable "project_id" {
  description = "GCP project hosting the service."
  type        = string
}

variable "region" {
  description = "Region for Cloud Run, Cloud Tasks, and Artifact Registry."
  type        = string
  default     = "europe-west3"
}

variable "image_receiver" {
  description = "Fully-qualified receiver container image (Artifact Registry)."
  type        = string
}

variable "image_worker" {
  description = "Fully-qualified worker container image (Artifact Registry)."
  type        = string
}

variable "github_app_id" {
  description = "GitHub App ID (not a secret)."
  type        = string
}

variable "billing_account_id" {
  description = "Billing account for the budget + alerts (MOD-87)."
  type        = string
}

variable "budget_amount_units" {
  description = "Monthly budget in whole currency units; near-zero by design — the service scales to zero and free tiers absorb light usage."
  type        = string
  default     = "10"
}

variable "budget_notification_channels" {
  description = "Monitoring notification channel IDs for budget alerts."
  type        = list(string)
  default     = []
}
