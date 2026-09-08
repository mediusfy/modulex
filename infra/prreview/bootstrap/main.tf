# Bootstrap: the dedicated GCP project for the hosted PR-review service.
# modulex gets its own project so the service's IAM, quotas, budget, and
# billing attribution never mix with other infrastructure. Apply this once
# (with org- or folder-level permissions), then feed its project_id output
# to ../ (the service stack).
#
# NEEDS HUMAN APPROVAL to apply (agent-safety-policy.md: infrastructure
# change).

terraform {
  required_version = ">= 1.6"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

variable "project_id" {
  description = "Globally-unique ID for the new project."
  type        = string
  default     = "mediusfy-modulex-prreview"
}

variable "org_id" {
  description = "Organization ID the project lives under; leave null when using folder_id."
  type        = string
  default     = null
}

variable "folder_id" {
  description = "Folder ID the project lives under; leave null when using org_id."
  type        = string
  default     = null
}

variable "billing_account_id" {
  description = "Billing account to link (required for Cloud Run/Firestore)."
  type        = string
}

resource "google_project" "prreview" {
  name            = "modulex prreview"
  project_id      = var.project_id
  org_id          = var.org_id
  folder_id       = var.folder_id
  billing_account = var.billing_account_id
  labels = {
    service = "prreview"
    adr     = "adr-0035"
  }
}

output "project_id" {
  description = "Feed this to ../ as var.project_id."
  value       = google_project.prreview.project_id
}
