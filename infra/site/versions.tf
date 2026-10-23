terraform {
  required_version = ">= 1.10, < 2.0"
  required_providers {
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "7.24.0"
    }
  }
}

# State is stored in the private, versioned GCS bucket configured in backend.tf.
# Authentication uses ADC; no credentials belong in this repository.
provider "google-beta" {
  user_project_override = true
}

provider "google-beta" {
  alias                 = "bootstrap"
  user_project_override = false
}
