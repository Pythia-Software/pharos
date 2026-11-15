# Importing from agent platforms

Design, 2026-11-15. **Proposal; nothing here is built.** It extends
[importer plugins](plugin-imports.md), whose bucket path
(`pharos capture-push` on the machine, `bucket-import/1` in the library) is
the base. This document asks how that becomes a five-minute job on the
hosted platforms that run a coding agent in a sandbox, starting with
Niteshift, without writing a separate integration for each.

Platform facts below come from each platform's public documentation in
November 2026. Anything marked **unverified** has to be checked in a real
sandbox before its recipe ships.

## What every platform has in common

Each platform runs somebody's agent (Claude Code, Codex, OpenCode and so on)
in a short-lived Linux sandbox that the customer configures a little. From
our side they differ in only five ways, so an adapter is five answers and no
code:

| Slot | Question | The usual answers |
|---|---|---|
| **Install** | Where does one command run so that the CLI is in every sandbox? | a setup script, a base image, a dev container Feature, an agent plugin |
| **Credential** | How does the push credential reach a process in the sandbox? | a platform secret as an environment variable; or no secret at all, by trading the platform's identity token for cloud access |
| **Trigger** | What makes the push run? | a supervised service that watches; the agent's own hooks; a step at the end |
| **Identity** | Which platform, environment, task and person is this sandbox? | environment variables or an identity token the platform provides |
| **Limits** | What can stop it? | an egress allowlist, secrets withheld from the agent, suspension, snapshots |

The work is to make the generic system offer a good answer for every slot,
and then to write each platform down as a small **recipe**.

## Four additions to the generic system

### 1. A watching mode, so no agent hook is needed

`pharos capture-push --watch` stays running, watches the agents' transcript
folders and pushes a few seconds after they change, with the same locking
and debouncing as a hook-started push. It makes one pass when it starts.

This is the preferred trigger wherever a platform supervises long-running
processes, because it:

- works for every agent app, including those with no hooks or whose hooks
  need trusting (a repository's `.codex/hooks.json` is not trusted
  automatically on Niteshift, for example);
- needs nothing committed to the agent's configuration;
- recovers by itself: a sandbox resumed after suspension restarts the
  service, whose first pass uploads whatever the suspension interrupted.

Hooks remain for platforms with no place to run a service, and `--wait`
remains for a final step where one exists.

### 2. A Pharos agent plugin, so setup can be "enable this"

Claude Code and Codex both load plugins from marketplaces, and platforms are
adopting them as the way to customize agents: Niteshift loads the enabled
plugins' hooks in every task and trusts them. A small first-party plugin
carries:

- a `Stop` and `SessionEnd` hook that run `pharos capture-push`;
- a `SessionStart` hook that installs the CLI into the home folder when it
  is missing, checked against a pinned hash.

Where plugins are available, a person enables the plugin and supplies the
destination, and nothing is added to setup scripts or to the repository.
The same plugin serves a laptop, a dev container and a hosted sandbox.

### 3. Keyless credentials, where the platform can prove who it is

Several platforms issue each sandbox a signed identity token (OIDC):
Niteshift (`ns oidc-token --audience …`), GitHub Actions, GitLab CI and
others. AWS, Google Cloud and Azure all accept such a token in exchange for
short-lived access to a bucket. `capture-push` gains that exchange, over
plain HTTPS like its uploads:

```json
{"type": "s3", "bucket": "example-pharos", "role_arn": "arn:aws:iam::…:role/pharos-push",
 "identity_token": {"command": ["ns", "oidc-token", "--audience", "sts.amazonaws.com"]}}
```

- No secret is stored on the platform, pasted into a settings page or
  visible to an agent. There is nothing to rotate and nothing left behind
  when an integration is abandoned.
- **Create the bucket** gains "trust a platform": it creates the role and
  limits it to the token's organization and environment, write-only, under
  the prefix.
- Stored write-only keys remain the answer everywhere else.

### 4. The sandbox says where it is

`capture-push` recognizes the platform it runs on from the recipe's markers
and adds a `platform` block to its hello and exports: platform name,
environment, task, a link to the task and the person who started it, as far
as the platform exposes them. The library uses it for three things:

- **Pool.** The default pool is "*Platform* · *environment*", so Machine
  reads "Niteshift · api" with no configuration.
- **A way back.** A conversation shows **Open in Niteshift** when its
  export carried a task link.
- **Whose work it is** (next section).

Whether a platform is also a value of **Channel** is a product decision
left open here. The task was started through the platform (from Slack,
Linear or its own UI), which is what Channel means, and Conductor and TL1
already have channels of their own. Until decided, the platform is recorded
as evidence so the choice needs no re-import.

## Teams: whose transcripts go to whose library

A platform environment is configured once for a team, so every teammate's
sandboxes push to the same destination, while each person's library should
hold their own work. The push configuration's prefix may therefore contain
`{owner}`:

```json
{"prefix": "pharos/{owner}", "destination": {"type": "s3", "bucket": "example-pharos", "...": "..."}}
```

- The owner is the platform's identifier for the person who started the
  task, from the identity slot; where the platform gives none, the push
  refuses to guess and says what to set.
- Each person's Pharos reads only `pharos/<their owner>/`, and **Create the
  bucket** can issue each a read credential limited to that prefix, so
  teammates cannot read each other's transcripts.
- A library that is meant to hold a team's work reads `pharos/` and sees
  each owner as a pool of their own.
- Setup shows the owners found in the bucket and asks which one is you,
  instead of asking anyone to find their platform user ID.

A single person's bucket has no `{owner}` and nothing changes.

## Recipes

A recipe is a checked-in data file per platform (`pharos-platform/1`, in
`contracts/plugins/platforms/`), read by both ends:

- `capture-push` uses its **markers** (how to recognize the platform) and
  **identity** (where environment, task, link and owner come from);
- setup uses its **install**, **credential** and **trigger** answers to
  print exactly what to commit and which secrets to add where, as text and
  as an agent prompt, plus the hosts to allow through the platform's
  firewall;
- its **limits** become warnings in setup.

**Set up a machine** then starts with "Where do your agents run?" and a
list of recipes, with "somewhere else" giving today's generic instructions.
A new platform is a new file and a test sandbox, not a release of new code.
Recipes also carry the date and documentation they were checked against,
because these platforms change monthly.

### Niteshift (first)

Niteshift runs Claude Code, Codex, OpenCode, Pi and others in sandboxes it
configures from files committed under `.niteshift/`, snapshots after setup,
suspends on inactivity and resumes on demand.

| Slot | Answer |
|---|---|
| Install | One line in `.niteshift/setup`, which runs as root on a fresh environment and is captured in the snapshot |
| Credential | Keyless: `ns oidc-token` exchanged for bucket access. Otherwise a Setup-script-scope secret that the service references by name, fetched each time it starts |
| Trigger | A `pharos-push` entry in `.niteshift/services.yaml` running `capture-push --watch`, restarted by Niteshift and after every resume |
| Identity | The token's `org_id`, `environment_id` and `user_id` claims; the task and its link are **unverified** |
| Limits | A suspended task runs nothing, which the watcher's first pass on resume covers. No stop or teardown hook is documented. Egress rules are **unverified** |

What a person does: run **Create the bucket** with "trust Niteshift", then
commit a two-file change (a prompt for an agent is provided) and open a
task. No secret is entered anywhere.

To check in a real sandbox before shipping: where each agent keeps its
transcripts and under which user; whether a service's environment can run
`ns oidc-token`; whether outbound HTTPS to the bucket is allowed by
default; and what identifies the task. Niteshift also offers an MCP server
that reads task transcripts. That is a different, lossier source (its
rendering, not the agent's files) and is not used; it is noted as a
fallback for an agent that leaves no files.

### Dev containers (one recipe, several platforms)

GitHub Codespaces, Ona (formerly Gitpod), DevPod and Coder all build from
`devcontainer.json`. A published **Dev Container Feature** installs the CLI
and starts the watcher from the container's start command, with the
credential from the platform's secrets. One line in `devcontainer.json`
covers all of them and local dev containers. Identity markers differ per
platform and live in each one's recipe.

### Vendor agent clouds

| Platform | Install | Credential | Trigger | Limits to state plainly |
|---|---|---|---|---|
| Claude Code on the web | the environment's setup script (cached), or the plugin's `SessionStart` | an environment variable; these are readable by everyone who uses the environment, so write-only keys only | the repository's `.claude/settings.json` hooks run in cloud sessions, or the plugin | network access is limited by default: the bucket's host must be added to the environment's allowlist |
| Codex cloud | setup script, which has internet | secrets are removed before the agent starts, so the key must be a plain environment variable: write-only keys only | Codex hooks or plugin | the agent phase has no internet by default; pushing needs the bucket's host allowed. Without that, nothing can be pushed and setup must say so |
| Cursor cloud agents | `install` in `.cursor/environment.json` | the Secrets tab | a `terminals` entry running the watcher | Cursor kinds are not importable yet (see [kinds](plugin-imports.md#kinds-come-from-the-registry)), so this recipe waits on that |

Platforms that run a closed agent of their own and leave no transcript
files in a sandbox the customer controls (Devin, Jules and similar) give
nothing to capture. They need an export from the vendor and are out of
scope.

### Sandbox SDKs and CI

- **E2B, Modal, Daytona, Fly Sprites and similar** are libraries the
  customer builds a harness on, not products with a settings page. The
  recipe is a snippet for the sandbox template that installs the CLI, and
  one call, `pharos capture-push --wait`, before the harness destroys the
  sandbox. Agents run through an SDK keep the same transcript files, so
  nothing else changes.
- **CI** (agents run from GitHub Actions or GitLab CI): a published action
  runs the push as a final step with the runner's identity token, keyless.

### Order

1. The four generic additions, and the recipe format.
2. Niteshift, verified in a real environment.
3. The Dev Container Feature (Codespaces, Ona, DevPod, Coder).
4. Claude Code on the web and Codex cloud.
5. The GitHub Action; SDK snippets.
6. Cursor cloud agents, when Cursor kinds become importable.

## Hazards these platforms add

- **Snapshots can clone a capture root.** A platform that snapshots a
  prepared environment and starts many sandboxes from it would give them
  all one pusher ID if the snapshot contained a capture root, and clones
  share every machine identifier we could compare. So the installer never
  creates the capture root; the first push in a sandbox does. If clones
  share one anyway, nothing is lost, because bucket keys are never reused:
  the library sees one pusher's chain of exports fork and treats each
  branch as its own host.
- **A suspended sandbox pushes nothing.** The watcher pushes seconds after
  each turn, long before an inactivity timeout, and again on resume.
- **Firewalls fail silently.** `capture-push --check` tests the bucket and,
  when keyless, the token exchange, and names the host that was blocked.
  Setup lists the hosts to allow before anyone hits the problem.
- **Secrets that agents can read.** Where a platform shows its environment
  variables to the agent or to every user of the environment, only a
  write-only credential belongs there. Recipes for such platforms offer
  nothing else.
- **Recipes go stale.** Each records when it was last verified; setup
  shows that date, and `--check` failing on a known platform says the
  recipe may be out of date rather than blaming the person.
- **Dead integrations multiply here.** A recipe committed to a repository
  keeps pushing from every new sandbox after its owner stops using Pharos.
  The far side already quiets itself (see
  [integrations that never work, or stop](plugin-imports.md#integrations-that-never-work-or-stop));
  with keyless access, removing the role's trust is the one switch that
  ends it everywhere.

## Decisions for the owner

1. **Keyless credentials** as the preferred answer where a platform issues
   identity tokens. It adds a token exchange per cloud to `capture-push`.
2. **A Pharos agent plugin** for Claude Code and Codex, published to their
   marketplaces, including a hook that installs the CLI.
3. **`{owner}` prefixes** for team environments. This needs to know how the
   Niteshift client works: one person's sandboxes, or a team sharing an
   environment where each person wants their own library, or one library
   for the team.
4. **Platform as a Channel value,** or evidence only.
5. **Access to a Niteshift environment** to verify the recipe's unverified
   answers.
