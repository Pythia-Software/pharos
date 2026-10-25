# Pharos Hosting infrastructure

OpenTofu owns the `pythia-pharos` Google Cloud project (display name `Pharos`),
its enabled APIs, Firebase enablement, default Hosting site, and a private,
versioned GCS bucket for remote state. Firebase CLI owns uploaded site versions
and releases; Hugo owns generated HTML. No Firebase client app registration or
client SDK is needed for this static site.

The production configuration is tracked in `production.auto.tfvars`: the
`pythia.software` organization (`205553137863`) and Pythia Billing are selected.
These are resource identifiers, not credentials. The Google provider is pinned
to `hashicorp/google-beta` 7.24.0 and the provider lockfile is committed.

## One-time workstation setup

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
to manage resources in the project, use its enabled APIs, and read the billing
account. Never commit credential files.

## Review infrastructure changes

```sh
cd infra/site
tofu init
tofu fmt -check
tofu validate
tofu plan -out=site.tfplan
tofu apply site.tfplan
tofu output
```

Plans, local variable overrides, state, and credentials are ignored. Review the
plan before applying. Project/Firebase/Hosting deletion is prevented, and
removing an API resource does not disable the live service. `prevent_destroy`
protects Firebase and Hosting while their resource blocks remain configured;
keep those protections in code.

Firebase can briefly return a service-usage permission error immediately after
project creation even for its owner. Verify the active ADC identity and project
IAM, then re-plan and retry after permissions propagate; do not recreate the
project or add broad roles as a workaround.

## Remote state and bootstrap history

State is stored in `gs://pythia-pharos-tofu-state`, prefix `pharos/site`.
The bucket enforces private access, object versioning, and deletion prevention.
The GCS backend provides state locking. All checkouts use the committed backend
configuration; do not initialize an independent local state for this project.

The first provisioning used local state to create the project and its bucket,
then `tofu init -migrate-state -force-copy` migrated the resources into GCS before
provisioning Firebase. This bootstrap step is complete and must not be repeated
for normal use. The active configuration is tracked as `backend.tf`.

To provision a different environment, use a separate root/backend and choose
new project, Hosting and bucket IDs; never repoint this production state by
editing the project ID in place. Resource identifiers are globally unique.

## Site deployment

```sh
# From the repository root, after signing in to Firebase CLI:
site/deploy.sh
```

See [site development](../../site/README.md) for preview commands, theme/content
locations and custom-domain overrides. The initial workflow is explicit local
deployment. Custom-domain DNS and GitHub Actions/Workload Identity Federation
are future additions; no service account keys, databases, compute instances,
Cloud Run services or analytics are provisioned by this starter.

References: [Firebase infrastructure setup](https://firebase.google.com/docs/projects/terraform/get-started),
[Firebase Hosting](https://firebase.google.com/docs/hosting/quickstart),
[OpenTofu GCS backend](https://opentofu.org/docs/language/settings/backends/gcs/).
