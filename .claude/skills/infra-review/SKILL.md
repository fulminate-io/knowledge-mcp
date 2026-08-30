---
name: infra-review
description: Deep, research-driven review of an infrastructure changeset BEFORE any infra command runs (deploy, apply, helm upgrade, terraform apply, provision, image roll). Grounds every claim in four authorities — provider docs (web-verified), current source, the LIVE cloud graph (actual deployed state), and runtime log graphs — to catch assumed provider semantics, missing config, broken scripts, unbootable VMs / unbuildable images, wrong permissions, blind or noisy monitors, and cross-system seams that silently disagree. Use before shipping any change to routing, load balancers, ingress/proxies, service mesh, manifests, cloud-init, startup/deploy scripts, Dockerfiles, certificates/identity, IAM/permissions, monitoring, or connection strings.
argument-hint: <the infra changeset — paths, a diff range, or a description of what is about to be deployed>
---

# Infra Review: $ARGUMENTS

<precedence>
User input > Skill constraints > Trained defaults

For universal orchestration discipline (background spawning, signal routing,
reviewer-gate semantics, user touch points, drift detection), reference
.claude/skills/orchestrate/SKILL.md. This skill is infra-review-specific.
</precedence>

<mental-model>
Infra review answers ONE question: after this change lands, will it actually WORK
when the command runs? Not "is each file valid" — every file can be individually
valid while the change is broken in the gap between what the config SAYS and what the
system actually DOES: a limit set to what the author assumed the provider means
(never web-checked); a required key that is simply absent; a script that is valid
shell and wrong logic; an image that builds on the author's box and not the target
(wrong arch); a permission too narrow or a "fix" that widens it; a monitor querying
the wrong field so it silently returns nothing; two systems on a path that disagree.
The review walks every one of those dimensions, verifying each value against the
STRONGEST available authority — provider live docs (semantics), current source (both
sides of a seam), the LIVE cloud graph (`graph:"cloud"` — what is actually deployed:
real config values, IAM bindings, routing, mounts, VMs), and runtime LOG graphs
(`graph:"logs"` — what is actually happening). The cloud graph answers "did the last
value actually land / does this resource even exist / has the manifest drifted" often
without a deploy; the logs confirm the symptom is real. Manifest-only is the weakest
authority — before the deploy command, not after a live outage.
</mental-model>

<when-to-run>
Run /infra-review when a changeset touches any of: L7 load-balancer / gateway config,
ingress or reverse-proxy routing, service-mesh / sidecar config, k8s or helm
manifests, cloud-init / user-data / startup or deploy SCRIPTS, systemd units,
Dockerfiles / image bakes, VM provisioning, TLS certs or host/user identity, IAM /
permissions / scopes / sudoers / pull secrets, connection strings (DB / cache / queue
/ object store), monitoring / logging / alerting config, or CI deploy pipelines. If
the next thing you are about to do is a deploy/apply/upgrade/provision/build command,
this skill runs first.
</when-to-run>

## Step 0: Check index freshness

```json
manage({ "operation": "status" })
```

If the index is behind HEAD, offer to `collect` before reviewing — the reviewer
traces application source through the code graph, so a stale index means a stale
trace. A stale index is a reason to collect, never a reason to route the reviewer to
grep the tree.

## Step 1: Gather the changeset + orient

Assemble what is actually changing:
- The infra diff — the manifests / config / IaC / scripts / Dockerfiles / pipeline
  files in the changeset (e.g. `git diff` over the infra paths, or the files named in
  $ARGUMENTS).
- The scoping ticket, if one exists (`assemble({ id: ticket_id })`).
- Any maintained infra **path-map / runbook** for this project — recall it to orient:

```json
thoughts({ "operation": "recall", "mode": "context", "query": "infra runtime path map runbook <the systems the change touches>" })
```

If the project maintains a hop-by-hop path-map or infra runbook (a document/memory),
use it to ORIENT the reviewer — do NOT hardcode any project-specific path into this
skill. If none exists, the reviewer builds context from the changeset and source.

Also identify, and pass to the reviewer, the **cloud account key** (`graph:"cloud"`)
and any **log source** (`graph:"logs"`) covering the systems the change touches — the
reviewer verifies against the live deployed state and real runtime logs, not the
manifest alone. The account key / log source is project-specific: take it from the
recalled path-map/runbook or the user, never hardcode it in this shipped skill. If the
project has no cloud/log collection configured, the reviewer falls back to docs +
source and says so.

## Step 2: Spawn the infra-reviewer (background)

<spawn id="infra-reviewer" background="true">

  <reference>See orchestrate constraint id="dispatch" — every spawn is background.</reference>

  <invocation>
    Agent(
      subagent_type: "infra-reviewer",
      prompt: "Review this infrastructure changeset BEFORE it is deployed. DISCOVERY-framed: do not assume it works. Verify each claim against the STRONGEST available authority, NOT the manifest alone — FOUR authorities: provider docs, current source, the LIVE cloud graph (graph:'cloud', account key '<CLOUD_ACCOUNT_KEY or none>': real meta_value config + USES_SA/BINDS_ROLE/ROUTES_TO/BACKS/MOUNTS_SECRET/BACKED_BY_VM edges — use it to confirm prior values actually LANDED, that the resource exists, and to catch drift), and runtime logs (graph:'logs', source '<LOG_SOURCE or none>' — check the CURRENT symptom the change claims to fix is real). Walk every dimension: (1) FLAGSHIP — web-verify every provider/API config value (timeouts, limits and their MAX, protocol/appProtocol, health-check fields, SDK error-retry semantics, IAM grants) against the provider's CURRENT docs via WebSearch/WebFetch, never from memory; build the verification log with doc URLs. (2) missing/incomplete config — cross-check every required key/env/secret/mount against the consumer that reads it AND the live cloud resource; default-deny. (3) scripts — will each startup/deploy/build script actually run to completion and do what it claims (shell correctness, error handling, idempotency, arch)? (4) images & VMs — will the image BUILD and the VM/container BOOT on the TARGET, not the author's box (base/arch/deps, no baked creds, no crash-loop)? (5) permissions — sufficient AND least-privilege, fail-closed preserved; never widen to fix; check the LIVE IAM bindings in the cloud graph. (6) observability — do monitors/logs/alerts observe what they claim; PROVE it against the real log graph (right field names, no under-count, no flood, coverage of the new surface). (7) cross-system seams — trace the runtime path and build the seam-agreement matrix across the seven seam classes, verified against BOTH sides' current source AND the cloud graph's live routing. (8) blast radius & reversibility. For each risky item name the primary evidence to check post-deploy. Never recommend weakening security to make something work. Produce the structured go/no-go report.\n\nChangeset: $ARGUMENTS\n\nCloud account key: <CLOUD_ACCOUNT_KEY or 'none'>\nLog source: <LOG_SOURCE or 'none'>\nScoping ticket: <ticket_id or 'none'>",
      description: "Infra review: provider-verify + config + scripts + images + perms + monitors + seams",
      run_in_background: true
    )
  </invocation>

</spawn>

## Step 3: Present the report

When the reviewer returns, surface its report — the provider-semantics verification
log, the traced path + seam matrix (if applicable), the per-dimension notes, the
tiered findings, the post-deploy primary-evidence checklist, and the verdict (go |
go-with-checks | no-go-until-resolved | no-go). Present it; do not end with a
permission-ask.

## Step 4: Route the findings

- **Tier 1 (will not work / security regression / irreversible)** → no-go. Fix before
  the command runs. A security-regression finding is never waved through to "smooth"
  a failure — grant the minimum needed while preserving fail-closed.
- **Tier 2 (unverified critical / probable break)** → resolve before go: web-verify
  the value, confirm the required config is set, or fix the disagreement. An
  UNVERIFIED load-bearing value is not "probably fine" — it is unproven.
- **Tier 3 (risky but recoverable)** → proceed with the primary-evidence checklist in
  hand; watch those items the moment the change lands.
- **Tier 4 (advisory)** → note and move on.

The post-deploy primary-evidence checklist is the review's second deliverable: after
the command runs, check each named piece of primary evidence (the access-log line,
the boot log, the metric, a direct probe) rather than assuming success. "The deploy
succeeded" is not "it works."

<constraint id="infra-review-discipline" severity="hard">
  <anti-patterns>
    <pattern>Reviewing the changeset inline instead of spawning the infra-reviewer — the review needs a dedicated context with web access</pattern>
    <pattern>Concluding a change is safe because each file is valid — validity-in-isolation is the false signal this skill exists to defeat</pattern>
    <pattern>Letting a provider config value ship without web-verifying its real semantics against current docs</pattern>
    <pattern>Reviewing a change to a running system from the manifest alone when the cloud graph (live deployed state) and log graphs (runtime evidence) are available — the deployed reality and real logs are stronger authorities than the diff</pattern>
    <pattern>Skipping the index-freshness check — a stale index yields a stale trace</pattern>
    <pattern>Hardcoding a project-specific path into this shipped skill — the path-map lives in the project's own memory, referenced generically</pattern>
    <pattern>Treating "deploy succeeded" as verification — check the primary evidence for each risky item post-deploy</pattern>
    <pattern>Accepting a security regression to make something work</pattern>
  </anti-patterns>
</constraint>
