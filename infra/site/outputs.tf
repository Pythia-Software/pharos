output "project_id" {
  value = google_project.site.project_id
}

output "hosting_url" {
  value = google_firebase_hosting_site.site.default_url
}

output "state_bucket" {
  value = one(google_storage_bucket.state[*].name)
}
