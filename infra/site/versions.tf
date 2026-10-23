terraform {
  required_version = ">= 1.10, < 2.0"
  required_providers {
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "7.24.0"
    }
  }
}

# Bootstrap uses local state. Migrate to the managed GCS bucket before sharing
# infrastructure administration; see README.md. Never commit state files.
provider "google-beta" {
  user_project_override = true
}

provider "google-beta" {
  alias                 = "bootstrap"
  user_project_override = false
}
