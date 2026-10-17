# Repository guide

A Kubernetes controller publishing Gateway API `HTTPRoute`s, and its own `PrivateResource`
objects, as resources in a Pangolin instance. Go, controller-runtime, Helm.

## Commands

Never call `go test`, `helm lint` or `golangci-lint` directly — use the justfile, which is what
CI runs:

- `just check` — format, vet, lint, unit tests. Run after every change.
- `just test` — envtest against a real API server, with the Pangolin fake. Run before every
  commit.
- `just e2e` — k3d, chart install, assertions against the fake. Run before opening a PR.
- `just verify` — regenerate deepcopy, CRDs and RBAC and fail if the result is uncommitted, or
  if `Chart.yaml`'s `version` and `appVersion` diverge (release automation rewrites both).
- `just --list` — everything else.

## Safety

These are hard constraints, not preferences:

- **Never send a request to `cloud.p3l1.de`.** It is a production instance; an accidental
  prune there takes real services offline. Every test uses the fake in `test/pangolinfake/`.
- **Never deploy into the user's k3s cluster.** Local and k3d only.
- **k3d: only the cluster named `pangolin-gateway`.** Never list, create or delete another.

## Layout

- `api/v1alpha1/` — the `PrivateResource` kind (`pangolin.p3l1.de`). `zz_generated.deepcopy.go`
  is generated; never edit it by hand.
- `cmd/main.go` — process entry point; wires flags into `manager.Config` and the Pangolin
  client, then starts the controller-runtime manager.
- `internal/gateway/` — `render.go` turns Gateway API objects into a blueprint and a verdict
  per route; `status.go` writes those verdicts into `status.parents`. All pure. The
  `pangolin.p3l1.de/` annotations are declared where they are parsed: sso, site and
  display-name at the top of `render.go`, the healthcheck in `healthcheck.go`, access rules
  in `rules.go`, visibility and grants in `visibility.go`, basic auth in `basicauth.go`.
  Credentials are resolved before the render, by `internal/controller/basicauth.go`, and
  handed in through `Inputs.BasicAuth` so the renderer stays pure. `privateresource.go`
  translates a `PrivateResource`; `RenderAll` returns verdicts for both kinds, and both
  compete in one collision check.
- `internal/pangolin/` — the Integration API client behind an eight-method interface, the
  blueprint types, and the dry-run decorator.
- `internal/controller/publisher.go` — the single reconciler, plus its metrics.
- `internal/manager/` — manager assembly (`Config`, `ControllerOptions`), the leader-election
  ID and the controller name.
- `internal/version/` — build metadata (`version.String()`), stamped by `-ldflags`.
- `charts/pangolin-gateway/` — the chart users install. RBAC lands in `templates/rbac.yaml`
  and the CRD in `templates/crds.yaml`, both via `just generate`.
- `config/crd/bases/`, `config/rbac/` — controller-gen output, the source of the chart copies.
- `test/pangolinfake/` — the fake Integration API, used by both envtest and e2e. Also builds
  into its own image for the e2e cluster.
- `test/envtest/`, `test/e2e/` — the medium and full verification tiers.
- `test/envtestenv/` — the importable envtest harness (`Start`). Controller tests live beside
  their controller under `internal/controller/` and run in the medium tier (`just test`).

This project defines **one CRD**, `privateresources.pangolin.p3l1.de`, generated from
`api/v1alpha1` by `just generate` (deepcopy, CRD, RBAC). The chart ships it as a template —
not in `crds/`, which Helm never upgrades — annotated `helm.sh/resource-policy: keep` unless
`crds.keep=false`. envtest loads it from `config/crd/bases`. The Gateway API types come from
`sigs.k8s.io/gateway-api`, and the CRDs envtest and k3d need are resolved from the module
cache by `just gateway-crds` — so the schemas under test are always the ones `go.mod` pins.

## Conventions

- English everywhere: code, comments, docs, commits, issues.
- Comments describe code, never project state or history. Three lines maximum, and only where
  the reason is not obvious from the code.
- `// SPDX-License-Identifier: AGPL-3.0-only` heads every Go file.
- Conventional Commits, enforced by a hook and by CI. Commits are GPG-signed (`git commit -S`).
- The chart must never contain `lookup`, `randAlphaNum`, Helm hooks, or annotations specific
  to one GitOps tool.
- The chart creates no Secret. The API key is supplied by the operator and referenced by
  `pangolin.apiKeySecret.name`. The *controller* creates one Secret per route asking for
  basic auth; its RBAC is `get` and `create` only, and Secrets bypass the client cache so
  no `list` or `watch` is needed.

## How the controller works

**One reconciler on a fixed key.** All four watches — HTTPRoute, Gateway, GatewayClass,
PrivateResource — map to the same request, so the workqueue coalesces a burst into one pass
and the whole desired state goes out in a single apply. Watches carry a `GenerationChanged OR AnnotationChanged`
predicate: a status write is itself a watch event, and status writes do not bump generation,
so without this the controller loops against itself.

**A pass is:** read all four kinds (any read error aborts before anything is written) →
render → apply → prune → write status. The prune runs once per blueprint section, each
against its own listing. The prune runs only after a successful apply and a
complete listing, and deletes only resources whose niceId starts with `gw-`. Routes are keyed
`gw-<ns>-<name>`, PrivateResources `gw-pr-<ns>-<name>`.

**No finalizer.** A deleted route is simply absent from the next render and the prune removes
its resource. A finalizer would block namespace deletion whenever the controller is down.

**Status is per parentRef.** An HTTPRoute's conditions live in `status.parents[]`, each entry
keyed by parentRef and `controllerName`. Only entries carrying `p3l1.de/pangolin` may be
touched; others belong to other controllers. `SetRouteStatus` returns whether anything
changed, and the caller must skip the write when it did not. A PrivateResource has plain
`status.conditions` — `Accepted` from the renderer, `Programmed` from the apply's outcome —
and the same skip-when-unchanged rule.

## Pangolin

Target version is v1.22.0. Verified against its source, not just the docs:

- The blueprint key for a resource type is `mode`; `protocol` is deprecated.
- `public-resources` is correct and is merged into the internal `proxy-resources` map.
- `full-domain` is unique **per section**: Pangolin checks `proxy-resources` and
  `client-resources` against themselves, so a duplicate within one fails the entire apply.
  The renderer resolves collisions itself — oldest route wins, across both sections — to keep
  one typo from unpublishing the cluster.
- The apply is transactional: failures come back as 400, success as 201. There is no
  200-with-partial-failures case.
- `DELETE /public-resource/{resourceId}` takes a **numeric** id, so the prune must list first
  to map niceId to resourceId.
- `private-resources` is a separate table behind separate routes: it is absent from
  `listResources`, its listing rows name the id `siteResourceId`, and it is deleted through
  `DELETE /private-resource/{siteResourceId}`. The client normalises that spelling so the
  prune sees one row type. The two id sequences are independent — the same number is a
  different resource in each section. Rationale: `docs/private-resources.md`.
- Access rules run **before** authentication: `allow` skips it entirely, `pass` only falls
  through to it. Rationale: `docs/access-rules.md`.
- A healthcheck whose `method` is empty is dropped before it reaches the agent, so the check
  sits in Pangolin's database and never runs; `method` is therefore always sent. Without
  `status` the agent accepts any 2xx, and it follows redirects unless `follow-redirects` says
  otherwise. A `mode` of `tcp` skips all of that and only dials the port; the key is an
  unvalidated string and anything else falls back to HTTP, so the renderer checks it itself.
- Pangolin skips a column whose key its document leaves out, so every healthcheck key an
  annotation can clear is sent on every pass rather than omitted.
- `auth.basic-auth` becomes a hash of `base64(user:password)` in a separate table. Every
  apply deletes that row before writing it again, so an omitted block removes the
  protection — unlike the healthcheck columns, which stay. `extendedCompatibility` turns an
  unauthenticated request into a 401 rather than the login page, but Pangolin skips that
  challenge while `sso-enabled` is true.
- An unknown site in a target throws inside that same transaction, failing the whole document
  as well. The controller lists sites and rejects the offending route up front; when the
  listing is unavailable the check degrades to a warning rather than rejecting everything.
- A private resource's `roles`, `users` and `machines` are replaced on every apply; omitted
  equals empty and leaves only the admin role. Unknown roles are created, `Admin` fails the
  document. `users` match username or e-mail, `machines` client niceIds; a list none of whose
  names resolve fails the document, so both are checked against `listUsers`/`listClients`
  the way sites are. An absent `tcp-ports`/`udp-ports` means every port, so host and cidr
  always send both. Aliases are unique per organisation. Rationale: `docs/private-resources.md`.

**API key actions.** Three are required — `applyBlueprint`, `listResources`, `deleteResource`
— plus `listSiteResources` and `deleteSiteResource` to prune private resources, and
`listSites`, `listClients` and `listUsers` for the name checks above. Every route is guarded by
`verifyApiKeyHasAction`, so a key missing one returns 401/403 for that route alone. Together
with pointing `--pangolin-endpoint` at the internal API rather than the Integration API, that
is the most likely cause of an unexplained 401 — which is why `pangolin.AuthError` names both
possibilities.
