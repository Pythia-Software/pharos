// Settings → Sources: collect transcripts that other machines push to a
// storage bucket (docs/capture-push.md). guide() writes the setup for
// Niteshift sandboxes and Google Cloud Storage from four names; render()
// shows it with the buckets this library already collects from.
(() => {
  const release = 'https://github.com/Pythia-Software/pharos/releases/latest/download';
  const manual = 'https://github.com/Pythia-Software/pharos/blob/master/docs/capture-push.md';

  // Every value below is pasted into shell commands, so each is held to
  // characters that need no quoting.
  const fields = [
    {key: 'project', label: 'Google Cloud project ID', placeholder: 'my-project', pattern: /^[a-z][a-z0-9-]{4,28}[a-z0-9]$/,
      hint: 'The project that will own the bucket.'},
    {key: 'bucket', label: 'Bucket name', placeholder: 'my-team-pharos', pattern: /^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$/,
      hint: 'A new bucket, used only for this. Bucket names are shared by all of Google Cloud.'},
    {key: 'organization', label: 'Niteshift organization ID', placeholder: '00000000-0000-0000-0000-000000000000', pattern: /^[A-Za-z0-9_-]{4,80}$/,
      hint: 'In any Niteshift task, run the command under “Find your Niteshift organization ID”.'},
    {key: 'number', label: 'Google Cloud project number', placeholder: '123456789012', pattern: /^[0-9]{6,20}$/,
      hint: 'Step 1 prints it. It is needed from step 2 on.'},
  ];
  const organizationCommand = `ns oidc-token --audience pharos | python3 -c "import sys,json,base64;print(json.loads(base64.urlsafe_b64decode(sys.stdin.read().split('.')[1]+'=='))['org_id'])"`;

  const clean = value => String(value ?? '').trim();

  // guide returns the steps for these names. A step whose names are missing
  // or malformed is not ready, and its text shows <placeholders>.
  function guide(input = {}) {
    const value = {}, problems = [], unset = new Set();
    for (const field of fields) {
      const text = clean(input[field.key]);
      if (text && !field.pattern.test(text)) problems.push(`${field.label} does not look right: ${text}`);
      if (!text || !field.pattern.test(text)) unset.add(field.key);
      value[field.key] = unset.has(field.key) ? `<${field.label.toLowerCase().replaceAll(' ', '-')}>` : text;
    }
    const machine = clean(input.machine) || 'Niteshift';
    const ready = (...keys) => keys.every(key => !unset.has(key));
    const provider = `projects/${value.number}/locations/global/workloadIdentityPools/pharos-push/providers/niteshift`;
    const audience = `//iam.googleapis.com/${provider}`;

    const create = `(
set -eu
P=${value.project}
B=${value.bucket}
ORG=${value.organization}
N=$(gcloud projects describe "$P" --format='value(projectNumber)')

gcloud services enable iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com storage.googleapis.com --project "$P"
gcloud storage buckets create "gs://$B" --project "$P" --location US --uniform-bucket-level-access --public-access-prevention

# Machines re-upload what is still in use; anything untouched for 90 days goes.
printf '{"rule":[{"action":{"type":"Delete"},"condition":{"age":90}}]}' > "\${TMPDIR:-/tmp}/pharos-lifecycle.json"
gcloud storage buckets update "gs://$B" --lifecycle-file "\${TMPDIR:-/tmp}/pharos-lifecycle.json"

# Sandboxes may add and replace objects. They cannot read or list the bucket.
gcloud iam roles create pharosPusher --project "$P" --title "Pharos pusher" --permissions storage.objects.create,storage.objects.delete --stage GA

# Only tasks in your Niteshift organization are accepted, with no stored key.
gcloud iam workload-identity-pools create pharos-push --project "$P" --location global --display-name "Pharos pushers"
gcloud iam workload-identity-pools providers create-oidc niteshift --project "$P" --location global --workload-identity-pool pharos-push --issuer-uri https://identity.niteshift.dev --attribute-mapping "google.subject=assertion.sub,attribute.org=assertion.org_id" --attribute-condition "assertion.org_id == '$ORG' && assertion.principal_type == 'task'"
gcloud storage buckets add-iam-policy-binding "gs://$B" --member "principalSet://iam.googleapis.com/projects/$N/locations/global/workloadIdentityPools/pharos-push/attribute.org/$ORG" --role "projects/$P/roles/pharosPusher"

# This Mac reads the bucket as you.
gcloud storage buckets add-iam-policy-binding "gs://$B" --member "user:$(gcloud config get-value account)" --role roles/storage.objectViewer

echo "Google Cloud project number: $N"
)`;

    const services = `# Uploads this task's agent transcripts to your Pharos bucket as they change.
# Nothing secret is stored: the task's identity token is traded for
# short-lived access.
services:
  - name: pharos-push
    command: sh .niteshift/pharos-push.sh
    restart: always
`;

    const script = `#!/bin/sh
# Installs the pharos command-line program if it is missing, then pushes
# transcripts as they change. Run by the pharos-push service in services.yaml.
set -eu
# A failure (no network, access not granted) waits before the service is
# restarted, so a broken setup retries every few minutes, not in a loop.
trap 'status=$?; [ "$status" -eq 0 ] || sleep 300; exit "$status"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
bin="$HOME/.pharos/bin/pharos"
# To stay on one version, replace latest/download with download/v1.2.3.
release=${release}

if [ ! -x "$bin" ]; then
  mkdir -p "$(dirname "$bin")"
  name=pharos-linux-$(uname -m)
  curl -fsSL -o "$bin.tmp" "$release/$name"
  expected=$(curl -fsSL "$release/$name.sha256" | cut -d' ' -f1)
  [ "$(sha256sum "$bin.tmp" | cut -d' ' -f1)" = "$expected" ]
  chmod +x "$bin.tmp"
  mv "$bin.tmp" "$bin"
fi

# With no arguments it watches; \`sh .niteshift/pharos-push.sh --check\` or
# \`--status\` runs that instead.
if [ $# -eq 0 ]; then
  set -- --watch
fi
"$bin" capture-push --config "$here/pharos-push.json" "$@"
`;

    const push = {
      contract: 'pharos-push-config/1', access: 'write', prefix: 'pharos/{owner}', retention_days: 90, pool: machine,
      destination: {type: 'gcs', bucket: value.bucket, workload_identity_provider: provider,
        identity_token: {command: ['ns', 'oidc-token', '--audience', audience]}},
    };

    const steps = [
      {id: 'create', title: '1. Create the bucket and its access', ready: ready('project', 'bucket', 'organization'),
        about: 'Run this once on this Mac, in Terminal, with gcloud signed in as someone who can administer the project. It creates the bucket, lets tasks in your Niteshift organization add to it without a stored key, and lets your Google account read it.',
        blocks: [{label: 'Copy commands', text: create}]},
      {id: 'repository', title: '2. Add three files to the repository', ready: ready('bucket', 'number'),
        about: 'Commit these to the default branch of each repository whose Niteshift tasks should be collected. Niteshift reads .niteshift from the default branch only, and starts the service in every new task.',
        blocks: [
          {file: '.niteshift/services.yaml', label: 'Copy services.yaml', text: services},
          {file: '.niteshift/pharos-push.sh', label: 'Copy pharos-push.sh', text: script},
          {file: '.niteshift/pharos-push.json', label: 'Copy pharos-push.json', text: JSON.stringify(push, null, 2) + '\n'},
        ]},
      {id: 'check', title: '3. Check from a Niteshift task', ready: true,
        about: 'Start a task in that repository and run this in its terminal. The check uploads one small file and prints "ok": true with a "prefix". That prefix is your folder in the bucket; step 4 needs it. If the download or the upload is refused, allow github.com, sts.googleapis.com and storage.googleapis.com in the environment’s network settings.',
        blocks: [{label: 'Copy check commands', text: 'ns services status\nsh .niteshift/pharos-push.sh --check\n'}]},
    ];
    return {problems, steps, bucket: unset.has('bucket') ? '' : value.bucket};
  }

  // readConfig is what this Mac reads the bucket with: your own Google
  // sign-in, through gcloud, and one person's folder.
  function readConfig(bucket, prefix) {
    return JSON.stringify({
      contract: 'pharos-push-config/1', access: 'read', prefix: clean(prefix),
      destination: {type: 'gcs', bucket: clean(bucket), access_token: {command: ['gcloud', 'auth', 'print-access-token']}},
    }, null, 2);
  }

  const kinds = {gcs: 'Google Cloud Storage', s3: 'Amazon S3', azure: 'Azure Blob Storage', http: 'Web server'};

  // bucketLine describes one connected bucket and what has come from it.
  function bucketLine(bucket) {
    const name = [kinds[bucket.type] || bucket.type, [bucket.bucket, bucket.prefix].filter(Boolean).join('/')].filter(Boolean).join(' · ');
    const machines = Number(bucket.machines) || 0;
    let status = 'Connected. Nothing collected yet; Pharos checks hourly and when you connect.';
    if (machines) status = `Collected from ${machines.toLocaleString()} ${machines === 1 ? 'machine' : 'machines'}.`;
    else if (bucket.collected_at) status = 'Connected. No machine has pushed to this folder yet.';
    return {name, status, collectedAt: bucket.collected_at || ''};
  }

  // connectedLine is the status after Connect.
  function connectedLine(connected) {
    const waiting = Number(connected?.checked_in) || 0;
    if (!waiting) return 'Connected. No machine has checked in to this folder yet; check the prefix against step 3 if you expected one.';
    return `Connected. ${waiting.toLocaleString()} ${waiting === 1 ? 'machine has' : 'machines have'} checked in. Pharos is collecting now.`;
  }

  const node = (tag, cls, text) => {
    const element = document.createElement(tag);
    if (cls) element.className = cls;
    if (text !== undefined) element.textContent = text;
    return element;
  };
  const call = async (path, options = {}) => {
    const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json'}});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || response.statusText);
    return body;
  };
  const css = `
.bucket-setup>summary{cursor:pointer;font-weight:600;padding:8px 0}
.bucket-setup-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,260px),1fr));gap:12px 16px;margin:12px 0}
.bucket-setup-fields label,.bucket-step label{display:grid;gap:4px;min-width:0;font-weight:600}
.bucket-setup-fields small,.bucket-step small{font-weight:400;color:var(--muted)}
.bucket-setup input,.bucket-setup textarea{box-sizing:border-box;width:100%;min-width:0;padding:6px 8px;border:1px solid var(--line);border-radius:7px;background:var(--bg);color:var(--ink);font:12px/1.4 ui-monospace,SFMono-Regular,monospace}
.bucket-step{border-top:1px solid var(--line);padding:14px 0;min-width:0}.bucket-step h4{margin:0 0 6px}.bucket-step p{margin:6px 0}
.bucket-block{margin:10px 0;min-width:0}.bucket-block-head{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px}
.bucket-block code{overflow-wrap:anywhere}
.bucket-block pre,.bucket-setup-find pre{margin:6px 0 0;padding:10px 12px;max-height:260px;overflow:auto;border:1px solid var(--line);border-radius:7px;background:color-mix(in srgb,var(--bg,#eee) 72%,transparent);font:12px/1.45 ui-monospace,SFMono-Regular,monospace;white-space:pre}
.bucket-connected{display:grid;grid-template-columns:minmax(0,1fr);gap:8px;margin:0 0 12px}.bucket-connected:empty{display:none}
.bucket-connected h4{margin:0;font-size:var(--fs-base);overflow-wrap:anywhere}.bucket-connected p{margin:4px 0 0}`;

  const state = {root: null, timer: 0, polls: 0};

  function render(root) {
    if (!root) return;
    if (state.root !== root) {
      state.root = root;
      if (!document.getElementById('bucketSetupStyle')) {
        const style = node('style', '', css);
        style.id = 'bucketSetupStyle';
        document.head.append(style);
      }
      const row = node('div', 'optional-behavior'), copy = node('div');
      copy.append(node('h3', '', 'Collect from other machines'),
        node('p', 'muted', 'Agents that run on other machines, such as cloud sandboxes, can push their transcripts to a storage bucket you own. Pharos collects them from the bucket hourly and adds their conversations to this library. Nothing passes through a Pharos server.'));
      row.append(copy);
      const problem = node('p', 'sync-note badtext');
      problem.setAttribute('role', 'status');
      problem.hidden = true;
      root.replaceChildren(row, node('div', 'bucket-connected'), problem, setup());
      root.hidden = false;
    }
    refresh();
  }

  // setup builds the guide once; typing rewrites the text of its blocks in
  // place, so focus and the disclosure survive.
  function setup() {
    const details = node('details', 'bucket-setup');
    details.dataset.viewKey = 'bucketSetup';
    details.dataset.viewLabel = 'Set up Niteshift with Google Cloud Storage';
    details.dataset.viewDefault = 'false';
    details.append(node('summary', '', 'Set up Niteshift with Google Cloud Storage'));
    const other = node('p', 'muted', 'You need the gcloud command-line tool on this Mac and a repository that Niteshift runs tasks in. Amazon S3, Azure Blob Storage and machines other than Niteshift sandboxes use the same pieces, configured by hand: see ');
    const link = node('a', '', 'the capture-push guide');
    link.href = manual;
    link.target = '_blank';
    link.rel = 'noreferrer';
    other.append(link, '.');
    details.append(other);

    const inputs = {}, grid = node('div', 'bucket-setup-fields');
    const field = (key, label, hint, placeholder) => {
      const wrap = node('label', '', label), input = node('input');
      input.type = 'text';
      input.id = 'bucketSetup-' + key;
      input.placeholder = placeholder || '';
      input.autocomplete = 'off';
      input.spellcheck = false;
      input.setAttribute('autocapitalize', 'off');
      input.oninput = update;
      wrap.append(input, node('small', '', hint));
      inputs[key] = input;
      return wrap;
    };
    for (const one of fields) grid.append(field(one.key, one.label, one.hint, one.placeholder));
    grid.append(field('machine', 'Machine name in Pharos', 'How these sandboxes appear under Machine. Leave empty for Niteshift.', 'Niteshift'));
    details.append(grid);

    const status = node('p', 'muted');
    status.setAttribute('role', 'status');
    const copyText = async (text, button) => {
      if (button.getAttribute('aria-busy') === 'true') return;
      button.setAttribute('aria-busy', 'true');
      try {
        await window.pharosHost.clipboard.writeText(text);
        status.textContent = 'Copied.';
      } catch {
        status.textContent = 'Could not copy. Select the text and copy it.';
      } finally {
        button.removeAttribute('aria-busy');
      }
    };
    const block = (label, file) => {
      const wrap = node('div', 'bucket-block'), head = node('div', 'bucket-block-head'), pre = node('pre'), button = node('button', 'claude-watch-button', label);
      button.type = 'button';
      button.onclick = () => copyText(pre.textContent, button);
      pre.tabIndex = 0;
      pre.setAttribute('aria-label', file || label.replace(/^Copy /, ''));
      head.append(file ? node('code', '', file) : node('span'), button);
      wrap.append(head, pre);
      return {wrap, pre, button};
    };

    const find = node('div', 'bucket-setup-find bucket-block'), findBlock = block('Copy command');
    findBlock.pre.textContent = organizationCommand;
    find.append(node('p', 'muted', 'Find your Niteshift organization ID: run this in the terminal of any Niteshift task.'), findBlock.wrap);
    details.append(find);

    const problems = node('p', 'sync-note badtext');
    problems.setAttribute('role', 'status');
    details.append(problems);

    const sections = guide().steps.map(step => {
      const section = node('section', 'bucket-step'), waiting = node('p', 'muted', 'Fill in the names above to finish these.');
      section.append(node('h4', '', step.title), node('p', 'muted', step.about), waiting);
      const blocks = step.blocks.map(one => block(one.label, one.file));
      section.append(...blocks.map(one => one.wrap));
      details.append(section);
      return {waiting, blocks};
    });

    // Step 4 connects this library: the folder from the check, and the read
    // configuration, which stays editable for other clouds.
    const connect = node('section', 'bucket-step');
    connect.append(node('h4', '', '4. Connect this library'),
      node('p', 'muted', 'Pharos reads the bucket with your own Google sign-in, through gcloud, so no key is stored. Each person collects their own folder: tasks you start land under your prefix. Collected conversations show Niteshift as their Channel.'));
    const prefix = field('prefix', 'Your folder in the bucket', 'The "prefix" the check printed in step 3, such as pharos/1a2b3c4d-…', 'pharos/');
    const configLabel = node('label', '', 'Read configuration'), config = node('textarea');
    config.id = 'bucketSetup-config';
    config.rows = 12;
    config.spellcheck = false;
    let edited = false;
    config.oninput = () => { edited = true; };
    configLabel.append(config, node('small', '', 'Written from the names above. Edit it, or paste another read configuration, to connect a different bucket.'));
    const actions = node('div', 'claude-watch-actions'), button = node('button', 'claude-watch-button', 'Connect'), result = node('p', 'muted');
    button.type = 'button';
    button.id = 'bucketSetupConnect';
    result.setAttribute('role', 'status');
    button.onclick = async () => {
      let parsed;
      try { parsed = JSON.parse(config.value); } catch { result.textContent = 'The read configuration is not valid JSON.'; return; }
      button.disabled = true;
      result.textContent = 'Reading the bucket…';
      try {
        const body = await call('/api/bucket-imports', {method: 'POST', body: JSON.stringify({config: parsed})});
        result.textContent = connectedLine(body.connected);
        state.polls = 0;
        show(body);
      } catch (failure) {
        result.textContent = failure.message;
      } finally {
        button.disabled = false;
      }
    };
    actions.append(button);
    connect.append(prefix, configLabel, actions, result, status);
    details.append(connect);

    function update() {
      const values = Object.fromEntries(Object.entries(inputs).map(([key, input]) => [key, input.value]));
      const made = guide(values);
      problems.textContent = made.problems.join(' ');
      problems.hidden = !made.problems.length;
      made.steps.forEach((step, index) => {
        sections[index].waiting.hidden = step.ready;
        step.blocks.forEach((one, at) => {
          const target = sections[index].blocks[at];
          if (target.pre.textContent !== one.text) target.pre.textContent = one.text;
          target.button.disabled = !step.ready;
        });
      });
      if (!edited) config.value = readConfig(made.bucket || '<bucket-name>', clean(values.prefix) || 'pharos/<your-niteshift-user-id>');
    }
    update();
    return details;
  }

  async function refresh() {
    clearTimeout(state.timer);
    try { show(await call('/api/bucket-imports')); } catch (failure) {
      const problem = state.root?.querySelector(':scope > .badtext');
      if (problem?.isConnected) { problem.textContent = failure.message; problem.hidden = false; }
    }
  }

  function show(body) {
    const root = state.root;
    if (!root?.isConnected || !body) return;
    clearTimeout(state.timer);
    const problem = root.querySelector(':scope > .badtext');
    problem.hidden = true;
    const list = root.querySelector('.bucket-connected'), existing = new Map([...list.children].map(row => [row.dataset.id, row]));
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.focusKey : '';
    const buckets = body.buckets || [];
    const rows = buckets.map(bucket => {
      const signature = JSON.stringify(bucket), kept = existing.get(bucket.id);
      if (kept?.dataset.signature === signature) return kept;
      const line = bucketLine(bucket), row = node('div', 'source-card');
      row.dataset.id = bucket.id;
      row.dataset.signature = signature;
      row.append(node('h4', '', line.name), node('p', '', line.status));
      if (line.collectedAt) row.append(node('p', 'muted', `Last collected ${new Date(line.collectedAt).toLocaleString()}`));
      const actions = node('div', 'claude-watch-actions'), remove = node('button', 'claude-watch-button', 'Disconnect');
      remove.type = 'button';
      remove.dataset.focusKey = bucket.id + ':remove';
      remove.title = 'Stop collecting from this bucket. What was collected stays in your library.';
      remove.onclick = async () => {
        remove.disabled = true;
        try { show(await call('/api/bucket-imports', {method: 'POST', body: JSON.stringify({remove: bucket.id})})); } catch (failure) {
          problem.textContent = failure.message;
          problem.hidden = false;
          remove.disabled = false;
        }
      };
      actions.append(remove);
      row.append(actions);
      return row;
    });
    if (rows.length !== list.children.length || rows.some((row, index) => list.children[index] !== row)) list.replaceChildren(...rows);
    if (focused && !list.contains(document.activeElement)) list.querySelector(`[data-focus-key="${window.CSS.escape(focused)}"]`)?.focus({preventScroll: true});
    // A new connection is collected in the background; watch for its result
    // for a few minutes, then leave it to the next visit.
    if (buckets.some(bucket => !bucket.machines) && state.polls < 60) {
      state.timer = setTimeout(() => {
        state.polls++;
        if (root.isConnected && !root.closest('[hidden]')) refresh();
      }, 5000);
    }
  }

  window.pharosBucketSetup = {guide, readConfig, bucketLine, connectedLine, organizationCommand, render};
  // This script is deferred: Settings → Sources may already be showing.
  const section = document.getElementById('bucketImportsSection');
  if (section && !section.parentElement.closest('[hidden]')) render(section);
})();
