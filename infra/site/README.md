# Pharos Hosting infrastructure

OpenTofu owns the `pythia-pharos` Google Cloud project (display name `Pharos`),
its enabled APIs, Firebase enablement, default Hosting site, and an optional
private, versioned GCS bucket for remote state. Firebase CLI owns uploaded site
versions/releases; Hugo owns generated HTML. No Firebase client app registration
or client SDK is needed for this static site.

## One-time account setup

Install OpenTofu and Firebase CLI, with Node 24 active:

```sh
brew install opentofu
npm install -g firebase-tools
gcloud auth login grady@pythia.software
gcloud auth application-default login
firebase login
```

OpenTofu uses Application Default Credentials, separately from gcloud’s CLI
login. Open the [Firebase console](https://console.firebase.google.com/) as the
same account and accept its Terms of Service if prompted. You need permission
to create projects in the organization, enable APIs/manage Firebase on the new
project, and (if linking billing) use the billing account. The default
organization is `pythia.software` (`205553137863`). Never commit credential files.

## Review and create

```sh
cd infra/site
cp terraform.tfvars.example terraform.tfvars
# Set billing_account if linking Pythia Billing; otherwise keep it null.
tofu init
tofu fmt -check
tofu validate
tofu plan -out=site.tfplan
tofu apply site.tfplan
tofu output
```

Project IDs and Hosting site IDs are globally unique. `pythia-pharos` is the
requested ID; only a successful creation confirms availability. If it is taken,
choose a replacement before applying and update `site/.firebaserc`, Hugo’s
baseURL, `site/deploy.sh` and `backend.tf.example` to match. Do not create this
project separately with gcloud: that would require importing it into state.

The provider is pinned to `hashicorp/google-beta` 7.24.0. Commit the generated
`.terraform.lock.hcl` after initialization. Plans, variable files, state and
credentials are ignored. Project/Firebase/Hosting deletion is prevented, and
removing an API resource does not disable the live service.

## State bootstrap and migration

The first apply uses **local state**, because the remote bucket does not yet
exist. Keep that state backed up and run applies from only one checkout.
Static Hosting can start without billing; a remote GCS state bucket needs
billing, so its resource is created only when `billing_account` is set.

After a successful billed apply creates `pythia-pharos-tofu-state`:

```sh
cp backend.tf.example backend.tf
tofu init -migrate-state
tofu plan
```

Confirm migration when prompted. `backend.tf` is ignored because it is enabled
only after bootstrap. On later checkouts, copy the same backend file and run
`tofu init` (not a fresh apply with local state). The bucket enforces private
access, object versioning, and deletion prevention; GCS backend provides state
locking. Do not start CI or multi-user infrastructure work until migration is
complete. Preserve local backups until remote state is verified.

A custom domain/DNS configuration and automated GitHub deployment are future
additions. No service account keys, databases, compute instances, Cloud Run
services or analytics are provisioned by this starter.

References: [Firebase infrastructure setup](https://firebase.google.com/docs/projects/terraform/get-started),
[Firebase Hosting](https://firebase.google.com/docs/hosting/quickstart),
[OpenTofu GCS backend](https://opentofu.org/docs/language/settings/backends/gcs/).
