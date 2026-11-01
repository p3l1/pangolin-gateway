# pangolin-gateway

A Kubernetes controller that publishes Gateway API `HTTPRoute`s as
[Pangolin](https://github.com/fosrl/pangolin) resources, and Pangolin private resources
of every mode it supports through its own `PrivateResource` kind.

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
are keyed `gw-<namespace>-<name>` for a route and `gw-pr-<namespace>-<name>` for a
`PrivateResource`, and the prune touches nothing without the `gw-` prefix — so a hand-written
blueprint can maintain other resources in the same organisation untouched.

There is no finalizer. A deleted route is simply absent from the next render, which is what
removes it; a finalizer would block namespace deletion whenever the controller is down.

## Requirements

- Pangolin v1.25.3 or newer, with the Integration API reachable from the cluster. The
  [mTLS](#mtls) auth method additionally needs an Enterprise/SaaS build; everything else
  works on any build
- An Integration API key of the form `<key_id>.<key_secret>`
- The Gateway API CRDs installed in the cluster. The chart installs the controller's own
  `PrivateResource` and `ResourcePolicy` CRDs.

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

`listClients` and `listUsers` are recommended for the same reason as soon as a
`PrivateResource` grants access to machines or users: a grant list none of whose names exist
also fails the whole apply. They are only called while something names a machine or a user.

`listResourceMtlsCertificates` is needed only once a route uses [mTLS](#mtls). After each
apply the controller reads the resource's trusted CA set back to confirm it took effect; a
key without the action, or a non-Enterprise build, makes that check fail and the route reports
`MtlsNotApplied` rather than a client-certificate requirement that is silently off.

`listResourcePolicies` and `deleteResourcePolicy` are needed only with
`pangolin.licensed` (see [Shared policies](#shared-policies-resourcepolicy)). They guard a
licensed pair of endpoints; the prune lists the policies and deletes the `gw-` ones no
`ResourcePolicy` claims any more.

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

The chart installs the `PrivateResource` and `ResourcePolicy` CRDs as ordinary templates, so
`helm upgrade` updates them too. They are annotated `helm.sh/resource-policy: keep`:
uninstalling the release leaves them, and with them every object of those kinds, in place. `crds.keep=false` drops the
annotation; `crds.install=false` leaves installing the CRDs to you — the controller does not
start without them.

Start with `--set pangolin.dryRun=true`. In that mode every write is logged and none is
performed, which is the safe way to see what a first real pass would do.

Add `--set pangolin.licensed=true` only when the organisation holds the licence for shared
policies; see [Shared policies](#shared-policies-resourcepolicy).

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
    pangolin.p3l1.de/roles: Member            # optional, who gets through the sign-in
    pangolin.p3l1.de/users: alice@example.com # optional, comma-separated
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

`pangolin.p3l1.de/roles` and `pangolin.p3l1.de/users` restrict who gets through that
sign-in. On a public route they become `sso-roles` and `sso-users` inside the resource's
`auth` block; on a private one they sit on the resource itself. Both are refused when
`sso` is `"false"`, because then there is nobody to authorise.

A route carrying neither keeps both keys out of the blueprint, which leaves whatever
Pangolin's own dashboard granted untouched — publishing must not revoke access nobody
asked it to touch. Annotating either one hands that resource's access list to Git: both
lists are then sent in full, so deleting an entry revokes it.

## Healthchecks

Pangolin stops routing to a target that has stopped answering. The check is off until
`pangolin.p3l1.de/healthcheck-path` is set:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/healthcheck-path: /api/webhook          # switches an HTTP check on
    pangolin.p3l1.de/healthcheck-status: "400"               # expected code; default is any 2xx
    pangolin.p3l1.de/healthcheck-method: HEAD                # default GET
    pangolin.p3l1.de/healthcheck-follow-redirects: "false"   # default true
    pangolin.p3l1.de/healthcheck-interval: "30"              # seconds, default 30
    pangolin.p3l1.de/healthcheck-timeout: "5"                # seconds, default 5
```

Hostname and port are not annotations: a check against an address other than the
target's would not describe that target.

### Two strategies

`pangolin.p3l1.de/healthcheck-mode` chooses between them, `http` (the default) and
`tcp`:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/healthcheck-mode: tcp     # switches the check on by itself
    pangolin.p3l1.de/healthcheck-timeout: "2"  # seconds, default 5
```

A TCP check opens a connection and claims nothing more: the port is accepting. It is
the honest description of a target that has no path answering 2xx at all — ArgoCD's
ApplicationSet webhook again, whose only route answers 400. `healthcheck-status`
describes that endpoint more precisely; `healthcheck-mode: tcp` describes it more
modestly, and one of the two is always better than a check that is permanently red.

In `tcp` mode the annotations that describe a response — `healthcheck-path`,
`-status`, `-method` and `-follow-redirects` — are **rejected rather than ignored**,
because a check that quietly drops half of what it was asked for is the failure this
controller exists to avoid. In `http` mode the path is still required: the mode names
the strategy, not what to request.

`snmp` and `icmp` appear in Pangolin's dashboard but are rejected here. They are a
paid feature its agent does not implement, and an unknown mode makes the agent fall
back to HTTP — so a route asking for one would silently get something else.

**`healthcheck-status` exists because "healthy" and "answers 2xx" are not the same
claim.** ArgoCD's ApplicationSet webhook answers 400 on its only route and 404
everywhere else, so a check insisting on 2xx would be permanently red — worse than no
check at all, because Pangolin stops routing to a target it believes is down.

**`healthcheck-follow-redirects` earns its place only next to `healthcheck-status`.**
Pangolin follows redirects by default, so an expected 301 or 302 would be compared
against whatever the redirect lands on and could never match.

**`healthcheck-method` is checked against a fixed list of verbs rather than passed
through.** Pangolin takes any string here but discards a check whose method is empty
*before it ever runs*, which leaves a check in its database that never fires. The
controller always sends a method for the same reason.

Pangolin's schema carries more fields that this controller does not map: `enabled`
(a check is switched off by removing its annotations, the same way a rule that should
not apply is deleted rather than disabled), `scheme` (the target's own method fixes
it), `headers`, `unhealthy-interval` and the two thresholds.

A malformed value is reported on the route as `Accepted: False` rather than dropped —
a check that silently does not run is the failure these annotations were added to fix.

Removing an annotation restores its default on the next pass. Pangolin skips a column
its document leaves out, so the controller always sends these keys rather than omitting
them: an omitted one would keep the value a route has just stopped asking for.

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

## Basic auth

A public resource can require an `Authorization: Basic` header before anything
reaches the backend:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/basic-auth: "true"
    pangolin.p3l1.de/sso: "false"    # see below; a browser is sent to SSO otherwise
```

**The annotation carries no credentials.** The repositories holding these routes are
public, and a password written into an annotation is a published password. The
controller generates one instead and writes it to a Secret named after the route:

```sh
kubectl -n argocd get secret gw-argocd-appset-webhook-basic-auth \
    -o jsonpath='{.data.password}' | base64 -d
```

The Secret is of type `kubernetes.io/basic-auth` with the keys `username` and
`password`; the username is the route's name. It is written once and never
rewritten, so a URL already pasted into a webhook keeps working. **Rotating means
deleting it** — the next pass generates a new one. It carries an `ownerReference` to
its route, so Kubernetes removes it when the route goes.

### What this costs in permissions

The ClusterRole gains `get` and `create` on Secrets. That is a deliberate widening,
and the shape of it matters:

- No `list` and no `watch`, so the controller cannot enumerate Secrets. Reads go
  straight to the API server rather than through an informer cache, which is what
  keeps those two verbs off the role.
- No `update` and no `delete`, so it can neither overwrite an existing Secret nor
  remove one. Deletion is left to Kubernetes through the `ownerReference`.
- The name is derived from the route — `gw-<route>-basic-auth`, in the route's own
  namespace — and no annotation can point it elsewhere.
- A Secret of that name the controller does not own is **refused, not adopted**, and
  the route is rejected with `Accepted: False`. Otherwise whatever that Secret held
  would quietly become the password of a resource on the internet.

Under `--dry-run` no Secret is written. A route asking for one it does not have yet
is reported as rejected, naming the Secret that a real pass would create.

### Why it exists

ArgoCD's ApplicationSet controller does not verify the signature GitHub computes
from a webhook secret ([argo-cd#21444](https://github.com/argoproj/argo-cd/issues/21444)),
so configuring one there protects nothing. Basic auth in front of the resource does,
because GitHub's sender accepts credentials in the payload URL and turns them into an
`Authorization: Basic` header.

That last part is measured, not documented: GitHub's documentation only warns against
putting credentials in the payload URL, and its own delivery log omits the
`Authorization` header even though it lists `X-Hub-Signature-256`. Only the receiving
end shows it.

`extendedCompatibility` is always sent as true. Without it Pangolin answers an
unauthenticated request with its login page instead of a challenge, which no webhook
sender can act on. Pangolin skips the challenge while `sso` is on, so a route meant
for a machine should also set `pangolin.p3l1.de/sso: "false"` — a client that sends
the header unprompted is let through either way.

A private route carrying the annotation is rejected: a private resource has no auth
block to put the credentials in.

## PIN

A public resource can ask for a six-digit PIN on Pangolin's sign-in page:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/pincode: "482913"
```

The PIN sits **beside** SSO, not in place of it. With `sso` on, the sign-in page
offers both, and either one lets a visitor through. Access rules still run first,
so a path opened with `allow` needs neither.

Unlike basic auth, **the annotation carries the PIN itself**. A PIN is a code for
people, handed out in a chat or a ticket. It is exactly as secret as the place it
was posted, so it does not belong in a public repository. Write it from wherever it
was handed out, such as a template over a pull request's labels.

An empty value means no PIN. A template can therefore always write the key and
fill it only when it has a code. Removing the annotation or emptying it removes the
PIN on the next pass: Pangolin clears it on every apply before reading the
document back.

Pangolin accepts 100000 to 999999 and nothing else. A value outside that range, a
leading zero included, is rejected on the route rather than sent, because an apply
Pangolin refuses unpublishes every other route along with it.

A private route carrying the annotation is rejected: a private resource has no
sign-in page to ask on.

## E-mail whitelist

A public resource can let specific people in by e-mail address, with a one-time code
sent to them, without a Pangolin account:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/email-whitelist: "anna@example.com, bob@example.org"
```

Entries are separated by commas or whitespace, lowercased, and deduplicated. The whitelist
sits **beside** SSO like the [PIN](#pin): the `sso` annotation decides whether the login is
on, and either way lets a listed address through. Access rules still run first.

An empty value means no whitelist, and removing the annotation removes it on the next pass:
Pangolin replaces the list on every apply.

An entry has to pass the same check as Pangolin's own schema, or the controller rejects
the route instead of sending it, because one bad address makes Pangolin refuse the whole
apply. A wildcard such as `*@example.com` is rejected here with a pointer to
[Shared policies](#shared-policies-resourcepolicy): only a policy can hold one. A private
route carrying the annotation is rejected too.

## Shared policies (`ResourcePolicy`)

Shared resource policies are a **licensed** Pangolin feature (Enterprise or SaaS tier).
The controller leaves them alone unless it is told the organisation holds the licence:

```yaml
# values.yaml
pangolin:
  licensed: true   # renders --pangolin-licensed
```

With the flag off, which is the default, the controller never calls the policy endpoints and
never sends `public-policies` or `policy:`. Every `ResourcePolicy` reports
`Accepted: False`, `NotLicensed`, and a route referencing one is rejected. Sending either to an
unlicensed organisation would make Pangolin refuse the whole apply.

A `ResourcePolicy` is a namespaced object that becomes one entry under `public-policies`.
Its niceId is `gw-<namespace>-<name>`:

```yaml
apiVersion: pangolin.p3l1.de/v1alpha1
kind: ResourcePolicy
metadata:
  name: choir
  namespace: demo
spec:
  displayName: Choir members
  roles: [Member]
  emailWhitelist:
    - anna@example.com
    - "*@plan-b-chor.de"
  pincode: "012345"
```

| Field | Meaning |
|---|---|
| `displayName` | Name in the dashboard; defaults to `<namespace>/<name>` |
| `sso` | Require a Pangolin login; default `true` |
| `roles`, `users` | Who may sign in; `Admin` is refused, and neither is meaningful with `sso: false` |
| `emailWhitelist` | Exact addresses and `*@domain` wildcards, lowercased |
| `pincode` | Six digits, leading zeros allowed |
| `rules` | `action`, `match`, `value` as for [access rules](#access-rules), in priority order |

A route opts in by naming the policy, in its own namespace:

```yaml
metadata:
  annotations:
    pangolin.p3l1.de/policy: choir
```

The route is then in Pangolin's shared policy mode: sign-in, whitelist, PIN and rules come
from the policy alone. Pangolin falls back to a resource's own PIN, password and header
auth when the policy has none, so the controller sends a resource with sign-in off and
nothing else. For the same reason the route may not also carry `sso`, `pincode`,
`basic-auth`, `access-rules`, `roles`, `users`, `email-whitelist` or any mTLS annotation; it is
rejected, naming the first one, rather than published with a second opinion. A route is
also rejected when the controller is not licensed, when the policy does not exist or is not
accepted, and when the route is private. Removing the annotation returns the route to its
own inline settings.

Pangolin never deletes a policy on apply. After a successful apply the controller lists
the policies and deletes the `gw-` ones no `ResourcePolicy` claims, after the resource
prune so a policy a route just left is free to go. A refused delete is logged and retried
on the next pass without failing it. Without `--pangolin-licensed` the listing is skipped.

## mTLS

A public resource can require a client certificate during the TLS handshake. The
client must present a certificate signed by one of the CAs the route names, or
Pangolin refuses the connection before any sign-in page. mTLS sits **on top of**
the other methods: a route may use it alone or alongside SSO, a PIN, or basic auth.

Name the CA with exactly one of three annotations, all pointing at the **public**
CA certificate only — never a private key:

```yaml
metadata:
  annotations:
    # one of:
    pangolin.p3l1.de/mtls-ca-secret: corporate-ca      # a Secret in the route's namespace
    pangolin.p3l1.de/mtls-ca-configmap: step-ca-root   # a ConfigMap in the route's namespace
    pangolin.p3l1.de/mtls-ca: |                         # or an inline PEM
      -----BEGIN CERTIFICATE-----
      ...
      -----END CERTIFICATE-----
```

From a **Secret** the CA is read from `ca.crt`, falling back to `tls.crt`, so both a
cert-manager CA Secret and any issued certificate's Secret work; the Secret type is not
checked, only the key. From a **ConfigMap** it is read from `ca.crt`, falling back to
`root_ca.crt`, which is step-ca's default. Each value may be a single certificate or a PEM
bundle of several, up to the 20 Pangolin allows. The controller runs its own CA in neither
case — it only publishes the trust anchor; issuing and rotating client certificates is the
CA's job. A Secret or ConfigMap that is missing, or carries no certificate, a private key, or
too many, rejects the route rather than publishing it without the protection it asked for.

**It must be an Enterprise/SaaS Pangolin.** On an open-source build the `auth.mtls` field is
silently dropped at apply, so mTLS would be off while the route looked published. To catch
that, the controller reads the trusted CA set back after each apply and compares it against
what it sent; a set that is empty, mismatched, or unreadable flips that route alone to
`MtlsNotApplied` (see [Status conditions](#status-conditions)). This needs the
`listResourceMtlsCertificates` API-key action.

A private route carrying any mTLS annotation is rejected, as with the other auth methods.

Rationale and the source behind the open-source gate: [docs/mtls.md](docs/mtls.md).

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

A route only ever becomes a `mode: http` private resource. Pangolin's `host` and `cidr`
modes describe a destination with port ranges and often no Kubernetes workload behind it — no
Gateway API object says that honestly — so they come through the `PrivateResource` kind below.

Details and the evidence behind them: [docs/private-resources.md](docs/private-resources.md).

## The PrivateResource kind

`pangolin.p3l1.de/v1alpha1` `PrivateResource` is a namespaced object whose fields map
one-to-one onto a blueprint's `private-resources` entry. It covers modes `host`, `cidr`
and `http`:

```yaml
apiVersion: pangolin.p3l1.de/v1alpha1
kind: PrivateResource
metadata:
  name: tekton-webhook
  namespace: tekton-pipelines
spec:
  displayName: Tekton Webhook
  mode: host
  sites: [my-site]
  destination: el-webhook.tekton-pipelines.svc.cluster.local
  alias: tekton-webhook.homelab.internal
  tcpPorts: "8080"
  machines: [fine-mastigoproctus-giganteus]
```

| Field | Modes | Meaning |
|---|---|---|
| `displayName` | all | Name in the dashboard; defaults to `<namespace>/<name>` |
| `mode` | — | `host`, `cidr` or `http`; required |
| `sites` | all | Site niceIds; defaults to `pangolin.defaultSite` |
| `destination` | all | IP or hostname; a CIDR block for `cidr`; required |
| `tcpPorts`, `udpPorts` | host, cidr | `"80,443,8000-9000"`, `"*"` for all; **unset means none** |
| `disableIcmp` | host, cidr | Block ping |
| `alias` | host | DNS name clients use; required when `destination` is a hostname |
| `fullDomain` | http | Hostname clients use; required |
| `ssl` | http | Serve `fullDomain` with a certificate; default `true` |
| `scheme`, `destinationPort` | http | How the site reaches the destination; scheme defaults to `http`, port required |
| `roles`, `users`, `machines` | all | Who may connect, besides org admins |

A field that has no meaning in the chosen mode is refused rather than ignored. Examples for
each mode: [host](examples/privateresource-host.yaml), [cidr](examples/privateresource-cidr.yaml),
[http](examples/privateresource-http.yaml).

**Ports are closed unless named.** A hand-written blueprint opens every port for an absent
`tcp-ports` or `udp-ports`; the controller always sends both, empty when the spec says
nothing. Set `udpPorts: "*"` explicitly to keep a migrated blueprint's implicit UDP.

**Grants replace, every time.** Pangolin deletes a private resource's role, user and machine
grants on every apply and writes back what the document lists, keeping only the admin role.
Omitting `roles`, `users` and `machines` therefore leaves the resource to org admins and
revokes anything granted in the dashboard. `users` match a username or an e-mail address,
`machines` a machine client's niceId. An unknown role is **created**, not refused; the
`Admin` role is refused, since it has access anyway. When `listUsers`/`listClients` are
available, a `users` or `machines` list **none** of whose names exist is rejected — Pangolin
would fail the whole apply for it — while a name that does not exist among others is dropped by
Pangolin and named in the `Accepted` message.

Resources from both kinds share the collision check: a full-domain, an alias or a private
route's hostname claimed twice goes to the oldest object, and the other is rejected with
`DuplicateHostname`. A public route may share its hostname with a private resource or route:
a connected Pangolin client reaches the private one, everyone else the public one.

The controller publishes every `PrivateResource` in the cluster; there is no class to opt
in through. Unlike a route, whose backend is a Service in its own namespace, a
`PrivateResource` names any destination the site can reach — another namespace, the node
network, a whole CIDR block. **Creating one is therefore network access from the site**, and
`create` on `privateresources.pangolin.p3l1.de` belongs only to those who should have that.

## Scope of v0.1

Deliberately narrow. A route is published only when it has exactly one hostname, one rule,
one `backendRef` to a Service in its own namespace, and no filters. Anything else is reported
on the route with `Accepted: False` and a reason, rather than published as something it is
not — silently dropping a redirect filter would proxy the whole domain to one backend.

A healthcheck is optional and off unless `pangolin.p3l1.de/healthcheck-path` is set;
see [Healthchecks](#healthchecks).

Not covered yet: TCPRoute, TLSRoute, the `ssh`, `inference` and `gateway` private modes,
private-resource `labels`, `enabled` and `auth-daemon`, policies, multiple hostnames per
route, cross-namespace backends via ReferenceGrant, and listener/`allowedRoutes` semantics.

## Status conditions

| Situation | Condition |
|---|---|
| Published | `Accepted: True`, `ResolvedRefs: True` |
| Publish to Pangolin failed | `Accepted: False`, `PublishFailed` |
| No, several, or wildcard hostname | `Accepted: False`, `UnsupportedValue` |
| Several rules, filters, or several backendRefs | `Accepted: False`, `UnsupportedValue` |
| Two routes claiming one key, or one hostname within the same section | `Accepted: False`, `DuplicateResourceKey` / `DuplicateHostname` (oldest route wins) |
| Backend is not a Service | `ResolvedRefs: False`, `InvalidKind` |
| Cross-namespace backendRef | `ResolvedRefs: False`, `RefNotPermitted` |
| Site does not exist in the organisation | `Accepted: False`, `UnknownSite` |
| Malformed healthcheck annotation | `Accepted: False`, `UnsupportedValue` |
| Malformed access rule | `Accepted: False`, `UnsupportedValue` |
| Annotation the chosen visibility cannot honour | `Accepted: False`, `UnsupportedValue` |
| Basic auth asked for, credentials unavailable | `Accepted: False`, `UnsupportedValue` |
| PIN that is not six digits from 100000 | `Accepted: False`, `UnsupportedValue` |
| Malformed `email-whitelist`, or a wildcard in it | `Accepted: False`, `UnsupportedValue` |
| `policy` while unlicensed, unknown or unaccepted, on a private route, or beside an access annotation | `Accepted: False`, `UnsupportedValue` |
| mTLS CA unreadable, absent, a private key, or over 20 | `Accepted: False`, `UnsupportedValue` |
| mTLS CA did not take effect (non-Enterprise build, or missing action) | `Accepted: False`, `MtlsNotApplied` |
| parentRef names no existing Gateway | `Accepted: False`, `NoMatchingParent` |

Conditions are written per parentRef into `status.parents[]`, only on entries belonging to
`p3l1.de/pangolin`. Routes on another controller's GatewayClass are left entirely alone.

A `PrivateResource` carries `Accepted` and `Programmed` in `status.conditions`, and its
niceId in `status.resourceKey` while it is published:

| Situation | Condition |
|---|---|
| Published | `Accepted: True`, `Programmed: True` |
| Publish to Pangolin failed | `Accepted: True`, `Programmed: False`, `PublishFailed` |
| Dry run | `Accepted: True`, `Programmed: False`, `DryRun` |
| Invalid field, port list, alias or destination | `Accepted: False`, `InvalidSpec` |
| Site does not exist; no listed machine or user exists | `Accepted: False`, `UnknownSite` / `UnknownMachine` / `UnknownUser` |
| Some listed machines or users do not exist | `Accepted: True`, the `Accepted` message names them |
| Key, hostname or alias claimed by an older object | `Accepted: False`, `DuplicateResourceKey` / `DuplicateHostname` |

`kubectl get privateresources` (short name `ppr`) shows both conditions.

A `ResourcePolicy` carries the same two conditions and its niceId in `status.policyKey`:

| Situation | Condition |
|---|---|
| Published | `Accepted: True`, `Programmed: True` |
| Controller not run with `--pangolin-licensed` | `Accepted: False`, `NotLicensed` |
| Invalid name, role, PIN, e-mail entry or rule | `Accepted: False`, `InvalidSpec` |
| Key claimed by an older policy | `Accepted: False`, `DuplicateResourceKey` |
| Publish to Pangolin failed | `Accepted: True`, `Programmed: False`, `PublishFailed` |

`kubectl get resourcepolicies` (short name `prp`) shows both conditions.

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
