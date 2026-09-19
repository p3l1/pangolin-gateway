# pangolin-gateway

A Kubernetes controller that publishes Gateway API `HTTPRoute`s as
[Pangolin](https://github.com/fosrl/pangolin) resources.

Status: early development. Not released.

## Why

Pangolin is an identity-aware tunnel and reverse proxy whose control plane runs outside the
cluster. Publishing a service there means creating a *resource*, and two properties make that
awkward to do by hand:

- **No wildcard resources.** Wildcards apply to certificates only, so every domain needs its
  own entry.
- **Blueprints are additive without prune.** Whatever was created once stays until something
  deletes it.

This controller closes the gap: it translates HTTPRoutes into Pangolin resources and removes
the ones no route claims any more.

## How it works

Each pass renders the whole desired state from the informer cache and sends it in a single
blueprint apply, then deletes the resources it owns that no longer have a route. Resources
are keyed `gw-<namespace>-<name>`, and the prune touches nothing without that `gw-` prefix —
so a hand-written blueprint can maintain other resources in the same organisation untouched.

There is no finalizer. A deleted route is simply absent from the next render, which is what
removes it; a finalizer would block namespace deletion whenever the controller is down.

## Requirements

- Pangolin v1.22.0 or newer, with the Integration API reachable from the cluster
- An Integration API key of the form `<key_id>.<key_secret>`
- The Gateway API CRDs installed in the cluster

**The API key needs three actions**: `applyBlueprint`, `listResources` and `deleteResource`.
A key missing one returns 401 or 403 for that operation alone, which looks exactly like a bad
key or a wrong endpoint.

Publishing private resources needs two more, `listSiteResources` and `deleteSiteResource`.
They guard a separate pair of endpoints over a separate table, so a key without them still
publishes private resources but can never prune one: a resource whose route is deleted would
stay published forever.

`listSites` is strongly recommended. Pangolin aborts an entire blueprint apply when
one target names a site that does not exist, so a single mistyped `pangolin.p3l1.de/site`
annotation would unpublish every route. With `listSites` the controller checks site names
first and rejects only the offending route. Without it the check is skipped and the controller
keeps working, but that failure mode returns.

## Installing

The chart never creates a Secret. Supply the key yourself:

```sh
kubectl create namespace pangolin-gateway-system
kubectl -n pangolin-gateway-system create secret generic pangolin-api-key \
    --from-literal=apiKey='<key_id>.<key_secret>'

helm install pangolin-gateway oci://ghcr.io/p3l1/charts/pangolin-gateway \
    --namespace pangolin-gateway-system \
    --set pangolin.endpoint=https://api.example.com/v1 \
    --set pangolin.org=my-org \
    --set pangolin.defaultSite=my-site \
    --set pangolin.apiKeySecret.name=pangolin-api-key
```

`pangolin.endpoint` is the Integration API base URL **including its version path**. Pangolin
Cloud serves `/v1`, self-hosted deployments usually `/api/v1`. Pointing it at the internal
API instead — the one on port 3000 — makes every call fail with an opaque 401.

Start with `--set pangolin.dryRun=true`. In that mode every write is logged and none is
performed, which is the safe way to see what a first real pass would do.

## Using it

The chart installs a GatewayClass named `pangolin` with
`controllerName: p3l1.de/pangolin`. Point a Gateway at it, then route to it as usual:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: pangolin
  namespace: demo
spec:
  gatewayClassName: pangolin
  listeners:
    - name: http
      port: 80
      protocol: HTTP
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: web
  namespace: demo
  annotations:
    pangolin.p3l1.de/display-name: Demo Web   # shown in Pangolin; defaults to demo/web
    pangolin.p3l1.de/sso: "true"              # default; omit to keep SSO on
    pangolin.p3l1.de/site: edge-site          # defaults to --default-site
    pangolin.p3l1.de/healthcheck-path: /healthz  # switches the check on
    pangolin.p3l1.de/healthcheck-interval: "30"  # seconds, optional
    pangolin.p3l1.de/healthcheck-timeout: "5"    # seconds, optional
spec:
  parentRefs:
    - name: pangolin
  hostnames:
    - demo.example.com
  rules:
    - backendRefs:
        - name: web
          port: 8080
```

That publishes a resource keyed `gw-demo-web` with `full-domain: demo.example.com`, targeting
`web.demo.svc.cluster.local:8080`.

The key is derived from the route and never changes; `display-name` only affects what
Pangolin's dashboard shows, and defaults to `<namespace>/<name>`. Migrating an existing
resource can keep the name people already recognise.

`pangolin.p3l1.de/sso` defaults to `"true"` on purpose: forgetting the annotation must not
publish a service unprotected. A value that is neither `"true"` nor `"false"` is rejected
rather than guessed.

## Access rules

A service can be behind SSO and still serve a few paths unauthenticated — a counting script
and a collection endpoint that other sites must reach, while the dashboard stays protected.
`pangolin.p3l1.de/access-rules` carries that as a YAML list:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/access-rules: |
      - action: allow
        match: path
        value: /script.js
      - action: allow
        match: path
        value: /api/collect
      - action: deny
        match: cidr
        value: 203.0.113.0/24
```

Rules are evaluated **before** authentication and the first match wins, so the three actions
differ more than their names suggest:

| Action | Effect |
|---|---|
| `allow` | **skips authentication entirely** for what the rule matches |
| `deny` | refuses the request |
| `pass` | falls through to the login, exactly as a non-matching request would |

`pass` opens nothing. Use it to carve an exception out of a broader rule — deny a country,
but send one CIDR through the normal login. Opening a path means `allow`, and an `allow` on
too wide a glob is an authentication bypass for everything under it.

**Order is the priority.** There is no `priority` field: Pangolin assigns one per position
and rejects a resource whose priorities collide, which a partly annotated list makes easy to
trigger. There is no `enabled` field either — a rule that should not apply is deleted.
Writing either is rejected rather than ignored.

`match` takes `path`, `cidr`, `ip`, `country`, `asn` or `region`. A `path` value is a
whole-path glob: `/api/*` matches `/api` and `/api/a/b`, while `/script.js` matches only
itself.

The controller validates `path`, `cidr` and `ip` before sending them, because an apply is
all or nothing — one bad value would unpublish every other route, not just the offending
one. **`country`, `asn` and `region` cannot be checked this way**: they depend on MaxMind
databases configured inside the Pangolin instance, which no API reports, so a value that
instance refuses will fail the whole apply. Every route then reports `PublishFailed` until
the rule is corrected.

Details of the matching and the evidence behind it: [docs/access-rules.md](docs/access-rules.md).

## Private resources

Pangolin separates resources reachable from the internet from those reachable only through a
connected Pangolin client. `pangolin.p3l1.de/visibility` chooses which one a route becomes:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/visibility: private
    pangolin.p3l1.de/roles: Member, Operators      # optional, comma-separated
    pangolin.p3l1.de/users: alice@example.com      # optional, comma-separated
```

The default is `public`, so a route that says nothing behaves exactly as it did before.

Without `roles` or `users` only the organisation's admin role reaches the resource — a safe
default, not a broken one. Note that Pangolin **creates a role it does not know** rather than
refusing it, so a misspelled role name does not fail the apply; it silently produces an empty
role that grants nobody anything.

A private resource has no auth block, no targets and no rules, so `sso`, the healthcheck
annotations and `access-rules` have nothing to map onto. A private route carrying one of them
is rejected rather than published without it. `roles` and `users` on a public route are
likewise rejected: quietly ignoring them would leave the author believing access is
restricted.

Only `mode: http` is covered. Pangolin's `host` and `cidr` modes describe a destination with
port ranges and no Kubernetes workload behind it — typically something outside the cluster,
reachable from the site. No Gateway API object says that honestly, so those modes are out of
scope rather than approximated.

Details and the evidence behind them: [docs/private-resources.md](docs/private-resources.md).

## Scope of v0.1

Deliberately narrow. A route is published only when it has exactly one hostname, one rule,
one `backendRef` to a Service in its own namespace, and no filters. Anything else is reported
on the route with `Accepted: False` and a reason, rather than published as something it is
not — silently dropping a redirect filter would proxy the whole domain to one backend.

A healthcheck is optional and off unless `pangolin.p3l1.de/healthcheck-path` is set.
Its hostname and port always mirror the target's — a check against a different address
would not describe that target — so only the path and, optionally, the interval and
timeout are annotated.

Not covered yet: TCPRoute, TLSRoute, the `host`, `cidr`, `ssh` and `inference` private
modes, policies, multiple hostnames per route, cross-namespace backends via ReferenceGrant,
and listener/`allowedRoutes` semantics.

## Status conditions

| Situation | Condition |
|---|---|
| Published | `Accepted: True`, `ResolvedRefs: True` |
| Publish to Pangolin failed | `Accepted: False`, `PublishFailed` |
| No, several, or wildcard hostname | `Accepted: False`, `UnsupportedValue` |
| Several rules, filters, or several backendRefs | `Accepted: False`, `UnsupportedValue` |
| Two routes claiming one key or hostname | `Accepted: False`, `DuplicateResourceKey` / `DuplicateHostname` (oldest route wins) |
| Backend is not a Service | `ResolvedRefs: False`, `InvalidKind` |
| Cross-namespace backendRef | `ResolvedRefs: False`, `RefNotPermitted` |
| Site does not exist in the organisation | `Accepted: False`, `UnknownSite` |
| Malformed healthcheck annotation | `Accepted: False`, `UnsupportedValue` |
| Malformed access rule | `Accepted: False`, `UnsupportedValue` |
| Annotation the chosen visibility cannot honour | `Accepted: False`, `UnsupportedValue` |
| parentRef names no existing Gateway | `Accepted: False`, `NoMatchingParent` |

Conditions are written per parentRef into `status.parents[]`, only on entries belonging to
`p3l1.de/pangolin`. Routes on another controller's GatewayClass are left entirely alone.

## Known issues in the environment

Two failure modes look like controller bugs but are not:

- **DELETE returns 403 behind CrowdSec.** The CrowdSec Traefik plugin blocked bodyless
  `DELETE` over HTTP/3 ([plugin #385](https://github.com/maxlerebourg/crowdsec-bouncer-traefik-plugin/issues/385)).
  Use plugin v1.8.0-alpha or later, or disable HTTP/3 in Traefik. The controller retries the
  deletion on the next pass either way.
- **401 on everything.** Either the endpoint points at the internal API rather than the
  Integration API, or the key lacks one of the required actions.

## Development

```sh
just setup   # verify tooling, install the commit hook
just check   # format, vet, lint, unit tests
just test    # envtest plus the Pangolin fake
just e2e     # k3d, chart install, end-to-end assertions
```

No test ever reaches a real Pangolin instance: both `just test` and `just e2e` run against
the fake in `test/pangolinfake/`, which reproduces the Integration API, records what it
received, and can inject failures on demand.

## Licence

AGPL-3.0-only.
