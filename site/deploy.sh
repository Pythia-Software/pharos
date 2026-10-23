#!/bin/sh
# Build and deploy to the OpenTofu-managed Firebase default Hosting site.
set -eu
cd "$(dirname "$0")"
PROJECT_ID=${PHAROS_SITE_PROJECT:-pythia-pharos}
BASE_URL=${PHAROS_SITE_URL:-"https://$PROJECT_ID.web.app/"}
hugo --gc --minify --panicOnWarning --baseURL "$BASE_URL"
firebase deploy --only hosting --project "$PROJECT_ID" --config firebase.json
