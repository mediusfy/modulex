output "receiver_url" {
  description = "Public webhook endpoint to configure on the GitHub App (append /webhook)."
  value       = google_cloud_run_v2_service.receiver.uri
}

output "worker_url" {
  description = "Worker task endpoint (invoked only by the Cloud Tasks OIDC identity)."
  value       = local.worker_url
}

output "queue_path" {
  description = "Cloud Tasks queue path the services enqueue to."
  value       = google_cloud_tasks_queue.reviews.id
}

output "artifact_registry" {
  description = "Docker repository for the receiver/worker images."
  value       = google_artifact_registry_repository.prreview.id
}
