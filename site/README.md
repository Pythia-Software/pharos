# Pharos website

A Hugo static site with the local `pharos-green` theme. It uses the shipping
Pharos icon, the existing library screenshot, and the app’s harbor green,
cream, and brass palette. Assets are mounted from their existing repository
locations, so build from this checkout rather than copying `site/` elsewhere.
No frontend JavaScript, third-party fonts, Firebase SDK, or runtime server is
required.

## Preview and build

Use Hugo 0.168.0 extended (the version verified for this starter):

```sh
hugo server --source site --disableFastRender
hugo --source site --gc --minify --panicOnWarning
```

Preview at `http://localhost:1313`. Output is ignored under `site/public/`.
Content lives in `site/content/`; theme templates and CSS live under
`site/themes/pharos-green/`. Add Markdown pages there as the site grows.
The download link points to GitHub’s latest published release and does not
advertise the pending notarization build as available.

## Provision and deploy

Provision the project and Hosting site using [OpenTofu](../infra/site/README.md).
Then, with Node 24 active and Firebase CLI installed/authenticated:

```sh
site/deploy.sh
```

The script builds with the Firebase production URL and deploys Hosting only.
`PHAROS_SITE_PROJECT` and `PHAROS_SITE_URL` override project and canonical URL.
Run deployment commands from this worktree so its Firebase configuration is used.
Hugo emits real pages and a custom 404; there is no SPA catch-all rewrite.

The production URL is `https://pythia-pharos.web.app/`. Firebase provides
managed HTTPS. A custom domain can be added once its name and DNS
ownership are known; set `PHAROS_SITE_URL` to that domain before deploying.

The initial workflow is an explicit local deployment. GitHub Actions deployment
and Workload Identity Federation can be added later without storing service
account keys in GitHub or this repository.
