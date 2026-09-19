# Repository guide

A Kubernetes controller publishing Gateway API `HTTPRoute`s as resources in a Pangolin
instance. Go, controller-runtime, Helm.

## Commands

Never call `go test`, `helm lint` or `golangci-lint` directly — use the justfile, which is what
CI runs:

- `just check` — format, vet, lint, unit tests. Run after every change.
- `just test` — envtest against a real API server, with the Pangolin fake. Run before every
  commit.
- `just e2e` — k3d, chart install, assertions against the fake. Run before opening a PR.
- `just verify` — regenerate RBAC and fail if the result is uncommitted, or if `Chart.yaml`'s
  `version` and `appVersion` diverge (release automation rewrites both).
- `just --list` — everything else.

## Safety

These are hard constraints, not preferences:

- **Never send a request to `cloud.p3l1.de`.** It is a production instance; an accidental
  prune there takes real services offline. Every test uses the fake in `test/pangolinfake/`.
- **Never deploy into the user's k3s cluster.** Local and k3d only.
- **k3d: only the cluster named `pangolin-gateway`.** Never list, create or delete another.

## Layout

- `cmd/main.go` — process entry point; wires flags into `manager.Config` and the Pangolin
  client, then starts the controller-runtime manager.
- `internal/gateway/` — `render.go` turns Gateway API objects into a blueprint and a verdict
  per route; `status.go` writes those verdicts into `status.parents`. Both are pure. The
  five `pangolin.p3l1.de/` annotations it reads are declared at the top of `render.go`.
- `internal/pangolin/` — the Integration API client behind a four-method interface, the
  blueprint types, and the dry-run decorator.
- `internal/controller/publisher.go` — the single reconciler, plus its metrics.
- `internal/manager/` — manager assembly (`Config`, `ControllerOptions`), the leader-election
  ID and the controller name.
- `internal/version/` — build metadata (`version.String()`), stamped by `-ldflags`.
- `charts/pangolin-gateway/` — the chart users install. RBAC lands in `templates/rbac.yaml`
  via `just generate`.
- `test/pangolinfake/` — the fake Integration API, used by both envtest and e2e. Also builds
  into its own image for the e2e cluster.
- `test/envtest/`, `test/e2e/` — the medium and full verification tiers.
- `test/envtestenv/` — the importable envtest harness (`Start`). Controller tests live beside
  their controller under `internal/controller/` and run in the medium tier (`just test`).

This project defines **no CRDs**. There is no `api/` and no `config/crd`; `controller-gen`
runs only for RBAC markers. The Gateway API types come from `sigs.k8s.io/gateway-api`, and
the CRDs envtest and k3d need are resolved from the module cache by `just gateway-crds` —
so the schemas under test are always the ones `go.mod` pins.

## Conventions

- English everywhere: code, comments, docs, commits, issues.
- Comments describe code, never project state or history. Three lines maximum, and only where
  the reason is not obvious from the code.
- `// SPDX-License-Identifier: AGPL-3.0-only` heads every Go file.
- Conventional Commits, enforced by a hook and by CI. Commits are GPG-signed (`git commit -S`).
- The chart must never contain `lookup`, `randAlphaNum`, Helm hooks, or annotations specific
  to one GitOps tool.
- The chart creates no Secret. The API key is supplied by the operator and referenced by
  `pangolin.apiKeySecret.name`.

## How the controller works

**One reconciler on a fixed key.** All three watches — HTTPRoute, Gateway, GatewayClass — map
to the same request, so the workqueue coalesces a burst into one pass and the whole desired
state goes out in a single apply. Watches carry a `GenerationChanged OR AnnotationChanged`
predicate: a status write is itself a watch event, and status writes do not bump generation,
so without this the controller loops against itself.

**A pass is:** read all three kinds (any read error aborts before anything is written) →
render → apply → prune → write status. The prune runs only after a successful apply and a
complete listing, and deletes only resources whose niceId starts with `gw-`.

**No finalizer.** A deleted route is simply absent from the next render and the prune removes
its resource. A finalizer would block namespace deletion whenever the controller is down.

**Status is per parentRef.** An HTTPRoute's conditions live in `status.parents[]`, each entry
keyed by parentRef and `controllerName`. Only entries carrying `p3l1.de/pangolin` may be
touched; others belong to other controllers. `SetRouteStatus` returns whether anything
changed, and the caller must skip the write when it did not.

## Pangolin

Target version is v1.22.0. Verified against its source, not just the docs:

- The blueprint key for a resource type is `mode`; `protocol` is deprecated.
- `public-resources` is correct and is merged into the internal `proxy-resources` map.
- `full-domain` must be unique across the blueprint, so a duplicate fails the entire apply.
  The renderer resolves collisions itself — oldest route wins — to keep one typo from
  unpublishing the cluster.
- The apply is transactional: failures come back as 400, success as 201. There is no
  200-with-partial-failures case.
- `DELETE /public-resource/{resourceId}` takes a **numeric** id, so the prune must list first
  to map niceId to resourceId.
- An unknown site in a target throws inside that same transaction, failing the whole document
  as well. The controller lists sites and rejects the offending route up front; when the
  listing is unavailable the check degrades to a warning rather than rejecting everything.

**API key actions.** Three are required — `applyBlueprint`, `listResources`, `deleteResource`
— and `listSites` is needed for the site check above. Every route is guarded by
`verifyApiKeyHasAction`, so a key missing one returns 401/403 for that route alone. Together
with pointing `--pangolin-endpoint` at the internal API rather than the Integration API, that
is the most likely cause of an unexplained 401 — which is why `pangolin.AuthError` names both
possibilities.
