terraform {
  backend "gcs" {
    bucket = "pythia-pharos-tofu-state"
    prefix = "pharos/site"
  }
}
