variable "project_id" {
  description = "Globally unique Google Cloud project ID and default Hosting site ID."
  type        = string
  default     = "pythia-pharos"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{4,28}[a-z0-9]$", var.project_id))
    error_message = "Use a valid 6–30 character Google Cloud project ID."
  }
}

variable "organization_id" {
  description = "Google Cloud organization that owns the project."
  type        = string
  default     = "205553137863"
}

variable "billing_account" {
  description = "Optional billing account ID. Null keeps billing unlinked."
  type        = string
  default     = null
  nullable    = true
}

variable "state_location" {
  description = "GCS location for infrastructure state; does not control Hosting CDN location."
  type        = string
  default     = "US"
}
