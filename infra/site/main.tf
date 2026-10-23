resource "google_project" "site" {
  provider        = google-beta.bootstrap
  project_id      = var.project_id
  name            = "Pharos"
  org_id          = var.organization_id
  billing_account = var.billing_account
  deletion_policy = "PREVENT"
  labels = {
    firebase   = "enabled"
    app        = "pharos"
    managed-by = "opentofu"
  }
}

resource "google_project_service" "site" {
  provider = google-beta.bootstrap
  project  = google_project.site.project_id
  for_each = toset([
    "cloudbilling.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "firebase.googleapis.com",
    "firebasehosting.googleapis.com",
    "serviceusage.googleapis.com",
    "storage.googleapis.com",
  ])
  service            = each.key
  disable_on_destroy = false
}

resource "google_firebase_project" "site" {
  provider        = google-beta
  project         = google_project.site.project_id
  deletion_policy = "PREVENT"
  depends_on      = [google_project_service.site]
}

# Firebase's default site uses the project ID. The provider handles the default
# site case; no Firebase web-app registration or client SDK is needed for Hugo.
resource "google_firebase_hosting_site" "site" {
  provider        = google-beta
  project         = google_project.site.project_id
  site_id         = google_project.site.project_id
  deletion_policy = "PREVENT"
  depends_on      = [google_firebase_project.site]
}

# Billing is optional for static Hosting, but required for our remote state
# bucket. Leave this absent until a billing account is deliberately selected.
resource "google_storage_bucket" "state" {
  provider                    = google-beta
  count                       = var.billing_account == null ? 0 : 1
  project                     = google_project.site.project_id
  name                        = "${var.project_id}-tofu-state"
  location                    = var.state_location
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  versioning {
    enabled = true
  }
  lifecycle {
    prevent_destroy = true
  }
  depends_on = [google_project_service.site]
}
