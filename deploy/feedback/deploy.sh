#!/usr/bin/env bash
# One Cloud Run service, a private GCS bucket, and durable upload notifications.
# Required: GCP_PROJECT, TWILIO_SID, TWILIO_SECRET_NAME, EMAIL_FROM.
# Twilio's secret is referenced in Secret Manager, never copied to local disk.
set -euo pipefail
# Newly created Google-managed service identities can take a few seconds to
# become visible to IAM. Binding operations are idempotent and safe to retry.
retry_iam() {
  local delay
  for delay in 0 2 4 8 16; do
    if [ "$delay" -gt 0 ]; then sleep "$delay"; fi
    if "$@"; then return 0; fi
  done
  return 1
}
ROOT=$(cd -- "$(dirname -- "$0")/../.." && pwd)
: "${GCP_PROJECT:?Set the target GCP project}"
: "${TWILIO_SID:?Set the send-only Twilio API Key SID used by Explo}"
: "${TWILIO_SECRET_NAME:?Set its Secret Manager secret name}"
: "${EMAIL_FROM:?Set the verified Twilio sender address}"
REGION=${REGION:-us-central1}
SERVICE=${SERVICE:-pythia-feedback}
BUCKET=${BUCKET:-$GCP_PROJECT-ui-feedback}
BUILD_BUCKET=${BUILD_BUCKET:-$GCP_PROJECT-feedback-build}
AUTH_PROJECT=${AUTH_PROJECT:-ui-feedback}
COOKIE_SECRET=${COOKIE_SECRET:-ui-feedback-cookie-key}
RUNTIME_ACCOUNT="feedback-runtime@$GCP_PROJECT.iam.gserviceaccount.com"
PUSH_ACCOUNT="feedback-push@$GCP_PROJECT.iam.gserviceaccount.com"
BUILD_ACCOUNT="feedback-build@$GCP_PROJECT.iam.gserviceaccount.com"
TOPIC=ui-feedback-created
DEAD_TOPIC=ui-feedback-failed
SUBSCRIPTION=ui-feedback-email
WORK="$ROOT/.context/feedback-image"
mkdir -p "$WORK"
cd "$ROOT"
gcloud services enable run.googleapis.com storage.googleapis.com pubsub.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com --project="$GCP_PROJECT" --quiet
PROJECT_NUMBER=$(gcloud projects describe "$GCP_PROJECT" --format='value(projectNumber)')
for account in feedback-runtime feedback-push feedback-build; do
  if ! gcloud iam service-accounts describe "$account@$GCP_PROJECT.iam.gserviceaccount.com" --project="$GCP_PROJECT" >/dev/null 2>&1; then
    gcloud iam service-accounts create "$account" --display-name="$account" --project="$GCP_PROJECT" --quiet
  fi
done
if ! gcloud storage buckets describe "gs://$BUCKET" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://$BUCKET" --location="$REGION" --uniform-bucket-level-access --public-access-prevention --project="$GCP_PROJECT" --quiet
fi
gcloud storage buckets update "gs://$BUCKET" --lifecycle-file="$ROOT/deploy/feedback/lifecycle.json" --clear-soft-delete --no-versioning --public-access-prevention --project="$GCP_PROJECT" --quiet
gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" --member="serviceAccount:$RUNTIME_ACCOUNT" --role=roles/storage.objectUser --project="$GCP_PROJECT" --quiet
if ! gcloud secrets describe "$COOKIE_SECRET" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  openssl rand -base64 32 | tr -d '\n' | gcloud secrets create "$COOKIE_SECRET" --data-file=- --replication-policy=automatic --project="$GCP_PROJECT" --quiet
fi
for secret in "$COOKIE_SECRET" "$TWILIO_SECRET_NAME"; do
  gcloud secrets add-iam-policy-binding "$secret" --member="serviceAccount:$RUNTIME_ACCOUNT" --role=roles/secretmanager.secretAccessor --project="$GCP_PROJECT" --quiet
done
if ! gcloud artifacts repositories describe feedback --location="$REGION" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  gcloud artifacts repositories create feedback --repository-format=docker --location="$REGION" --project="$GCP_PROJECT" --quiet
fi
if ! gcloud storage buckets describe "gs://$BUILD_BUCKET" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  gcloud storage buckets create "gs://$BUILD_BUCKET" --location="$REGION" --uniform-bucket-level-access --public-access-prevention --project="$GCP_PROJECT" --quiet
fi
gcloud storage buckets update "gs://$BUILD_BUCKET" --lifecycle-file="$ROOT/deploy/feedback/build-lifecycle.json" --clear-soft-delete --no-versioning --public-access-prevention --project="$GCP_PROJECT" --quiet
gcloud storage buckets add-iam-policy-binding "gs://$BUILD_BUCKET" --member="serviceAccount:$BUILD_ACCOUNT" --role=roles/storage.objectViewer --project="$GCP_PROJECT" --quiet
gcloud artifacts repositories add-iam-policy-binding feedback --location="$REGION" --member="serviceAccount:$BUILD_ACCOUNT" --role=roles/artifactregistry.writer --project="$GCP_PROJECT" --quiet
gcloud projects add-iam-policy-binding "$GCP_PROJECT" --member="serviceAccount:$BUILD_ACCOUNT" --role=roles/logging.logWriter --condition=None --quiet
# Both UI bundles are embedded. Use the repository's locked build, with Node 24.
"$ROOT/tools/setup.sh"
GOPRIVATE='github.com/pythia-software/*,github.com/Pythia-Software/*' GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$WORK/feedback" ./cmd/feedback
cp "$ROOT/deploy/feedback/Dockerfile" "$WORK/Dockerfile"
IMAGE="$REGION-docker.pkg.dev/$GCP_PROJECT/feedback/service:$(date -u +%Y%m%d%H%M%S)"
gcloud builds submit "$WORK" --config="$ROOT/deploy/feedback/cloudbuild.yaml" --substitutions="_IMAGE=$IMAGE" --service-account="projects/$GCP_PROJECT/serviceAccounts/$BUILD_ACCOUNT" --gcs-source-staging-dir="gs://$BUILD_BUCKET/source" --project="$GCP_PROJECT" --quiet
ORIGIN=$(gcloud run services describe "$SERVICE" --region="$REGION" --project="$GCP_PROJECT" --format='value(status.url)' 2>/dev/null || true)
INITIAL_ORIGIN=${ORIGIN:-https://feedback.pythia.software}
ACCESS_FLAGS=(--quiet)
if [ -z "$ORIGIN" ]; then ACCESS_FLAGS+=(--no-allow-unauthenticated); fi
# An initial deployment is private until its canonical origin is configured.
# On updates preserve the existing service origin for valid cookies/callbacks.
ENV_FILE="$ROOT/.context/feedback-environment.json"
python3 - "$ENV_FILE" "$INITIAL_ORIGIN" "$BUCKET" "$AUTH_PROJECT" "$PUSH_ACCOUNT" "$TWILIO_SID" "$EMAIL_FROM" <<'PY'
import json, sys
path, origin, bucket, project, push, sid, sender = sys.argv[1:]
with open(path, 'w') as f:
    json.dump(dict(FEEDBACK_ORIGIN=origin, FEEDBACK_BUCKET=bucket, FEEDBACK_AUTH_PROJECT=project, FEEDBACK_PUSH_ACCOUNT=push, TWILIO_API_KEY_SID=sid, FEEDBACK_EMAIL_FROM=sender), f)
PY
gcloud run deploy "$SERVICE" --image="$IMAGE" --region="$REGION" --project="$GCP_PROJECT" --service-account="$RUNTIME_ACCOUNT" --min-instances=0 --max-instances=1 --concurrency=8 --memory=512Mi --cpu=1 --timeout=60 "${ACCESS_FLAGS[@]}" --env-vars-file="$ENV_FILE" --set-secrets="FEEDBACK_COOKIE_KEY=$COOKIE_SECRET:latest,TWILIO_API_KEY_SECRET=$TWILIO_SECRET_NAME:latest"
ORIGIN=$(gcloud run services describe "$SERVICE" --region="$REGION" --project="$GCP_PROJECT" --format='value(status.url)')
gcloud run services update "$SERVICE" --region="$REGION" --project="$GCP_PROJECT" --update-env-vars="FEEDBACK_ORIGIN=$ORIGIN" --quiet
for topic in "$TOPIC" "$DEAD_TOPIC"; do
  if ! gcloud pubsub topics describe "$topic" --project="$GCP_PROJECT" >/dev/null 2>&1; then gcloud pubsub topics create "$topic" --project="$GCP_PROJECT" --quiet; fi
done
STORAGE_AGENT=$(gcloud storage service-agent --project="$GCP_PROJECT" | tr -d '[:space:]')
retry_iam gcloud pubsub topics add-iam-policy-binding "$TOPIC" --member="serviceAccount:$STORAGE_AGENT" --role=roles/pubsub.publisher --project="$GCP_PROJECT" --quiet
PUBSUB_AGENT="service-$PROJECT_NUMBER@gcp-sa-pubsub.iam.gserviceaccount.com"
gcloud beta services identity create --service=pubsub.googleapis.com --project="$GCP_PROJECT" --quiet
retry_iam gcloud iam service-accounts add-iam-policy-binding "$PUSH_ACCOUNT" --member="serviceAccount:$PUBSUB_AGENT" --role=roles/iam.serviceAccountTokenCreator --project="$GCP_PROJECT" --quiet
if gcloud pubsub subscriptions describe "$SUBSCRIPTION" --project="$GCP_PROJECT" >/dev/null 2>&1; then
  gcloud pubsub subscriptions modify-push-config "$SUBSCRIPTION" --push-endpoint="$ORIGIN/events/storage" --push-auth-service-account="$PUSH_ACCOUNT" --push-auth-token-audience="$ORIGIN" --project="$GCP_PROJECT" --quiet
else
  gcloud pubsub subscriptions create "$SUBSCRIPTION" --topic="$TOPIC" --push-endpoint="$ORIGIN/events/storage" --push-auth-service-account="$PUSH_ACCOUNT" --push-auth-token-audience="$ORIGIN" --ack-deadline=60 --min-retry-delay=10s --max-retry-delay=600s --dead-letter-topic="$DEAD_TOPIC" --max-delivery-attempts=10 --message-retention-duration=7d --project="$GCP_PROJECT" --quiet
fi
gcloud pubsub subscriptions update "$SUBSCRIPTION" --expiration-period=never --project="$GCP_PROJECT" --quiet
gcloud pubsub topics add-iam-policy-binding "$DEAD_TOPIC" --member="serviceAccount:$PUBSUB_AGENT" --role=roles/pubsub.publisher --project="$GCP_PROJECT" --quiet
gcloud pubsub subscriptions add-iam-policy-binding "$SUBSCRIPTION" --member="serviceAccount:$PUBSUB_AGENT" --role=roles/pubsub.subscriber --project="$GCP_PROJECT" --quiet
if ! gcloud pubsub subscriptions describe ui-feedback-failed-review --project="$GCP_PROJECT" >/dev/null 2>&1; then gcloud pubsub subscriptions create ui-feedback-failed-review --topic="$DEAD_TOPIC" --message-retention-duration=7d --project="$GCP_PROJECT" --quiet; fi
gcloud pubsub subscriptions update ui-feedback-failed-review --expiration-period=never --project="$GCP_PROJECT" --quiet
NOTIFICATIONS=$(gcloud storage buckets notifications list "gs://$BUCKET" --format=json --project="$GCP_PROJECT")
if ! python3 -c 'import json,sys; topic=sys.argv[1]; configs=[n.get("Notification Configuration",n) for n in json.load(sys.stdin)]; sys.exit(0 if any(n.get("topic", "").endswith("/topics/"+topic) and n.get("object_name_prefix")=="uifeedback/v1/submissions/" and n.get("event_types")==["OBJECT_FINALIZE"] for n in configs) else 1)' "$TOPIC" <<<"$NOTIFICATIONS"; then
  gcloud storage buckets notifications create "gs://$BUCKET" --topic="$TOPIC" --event-types=OBJECT_FINALIZE --object-prefix=uifeedback/v1/submissions/ --payload-format=json --project="$GCP_PROJECT" --quiet
fi
gcloud run services add-iam-policy-binding "$SERVICE" --region="$REGION" --project="$GCP_PROJECT" --member=allUsers --role=roles/run.invoker --quiet
python3 "$ROOT/deploy/feedback/auth-plan.py" "$ORIGIN" "$AUTH_PROJECT" > "$ROOT/.context/feedback-auth-plan.json"
python3 - "$ORIGIN" "$GCP_PROJECT" "$BUCKET" "$IMAGE" "$ROOT/.context/feedback-deployment.json" <<'PY'
import json, sys
origin, project, bucket, image, path = sys.argv[1:]
with open(path, 'w') as f: json.dump(dict(origin=origin, project=project, bucket=bucket, image=image), f, indent=2)
print('Feedback service: '+origin)
print('Configure Pharos with PHAROS_FEEDBACK_URL='+origin)
PY
curl --fail --silent --show-error "$ORIGIN/health"
