# Private resources

Notes on Pangolin's `private-resources` section, verified against
[`fosrl/pangolin`](https://github.com/fosrl/pangolin) at tag `1.22.0`.

## Two sections, two tables, two sets of routes

A blueprint's `public-resources` is merged into `proxy-resources`; its
`private-resources` is merged into `client-resources` the same way
(`server/lib/blueprints/types.ts`). They are not two views of one thing:

|  | Public | Private |
|---|---|---|
| Table | `resources` | `siteResources` |
| Listing | `GET /org/{orgId}/public-resources` | `GET /org/{orgId}/private-resources` |
| Listing rows | `data.resources[]`, id `resourceId` | `data.siteResources[]`, id `siteResourceId` |
| Delete | `DELETE /public-resource/{resourceId}` | `DELETE /private-resource/{siteResourceId}` |
| API key actions | `listResources`, `deleteResource` | `listSiteResources`, `deleteSiteResource` |

A private resource is therefore invisible to the public listing. Without the
second endpoint the prune would never see one, and a resource whose route was
deleted would stay published forever — blueprints do not prune.

The two id sequences are independent, so the same number is a different resource
in each section. `internal/pangolin/client.go` normalises `siteResourceId` into
the same row type the public listing yields, which keeps the quirk in one place
and lets the prune treat both sections alike.

## Only mode http maps to an HTTPRoute

`PrivateResourceSchema` takes `host`, `cidr`, `http`, `ssh` and `inference`.
Only `http` has a counterpart in the Gateway API:

- `host` and `cidr` describe a destination reachable *from the site* — an IP or
  a CIDR block, with TCP and UDP port ranges and an alias — and no Kubernetes
  workload behind it. There is no Gateway API object that says this honestly.
  `TCPRoute` does not: it names a listener port onto a Service, which is
  Pangolin's *public* tcp mode, and it lives in the experimental channel whose
  CRDs are not installed by default.
- `ssh` and `inference` are further from HTTP still.

So a route annotated `pangolin.p3l1.de/visibility: private` becomes a `mode:
http` private resource, and the rest is out of scope rather than approximated.

## What a private resource does not have

No `auth` block, no `targets`, no `rules`. Access is granted by `roles`, `users`
and `machines`; `sso`, healthcheck and access-rule annotations have nothing to
map onto, so the renderer refuses a private route that carries one rather than
dropping it silently.

Pangolin keeps the organisation's admin role attached to every private resource
and replaces the rest on each apply, so a resource published without `roles` or
`users` is reachable by org admins alone. That is a safe default, not an error.

**A role named in the annotation is created if it does not exist**
(`server/lib/blueprints/privateResources.ts`), so a typo does not fail the apply
— it quietly creates an empty role that grants nobody anything. This is the
opposite of the site check, where an unknown name fails the whole document.

## Fields Pangolin decides for itself

For `mode: http` the applier forces `tcp-ports` to `443,80`, `udp-ports` to
empty, `disable-icmp` to true, and defaults `ssl` to true. Sending them would
only restate what Pangolin does anyway, so the blueprint carries `name`, `mode`,
`sites`, `destination`, `destination-port`, `full-domain`, `scheme` and the
grants.

`sites` is the plural key. The singular `site` still works and is marked
deprecated in the schema.

## full-domain uniqueness is per section

`ConfigSchema.superRefine` checks `proxy-resources` against itself and
`client-resources` against itself, so the same `full-domain` in both sections
passes validation. The database check on the private side
(`getDomainForSiteResource`) compares a non-inference resource only against
*inference* resources, which narrows it further.

The renderer does not rely on either. It resolves hostname collisions across
both sections, oldest route wins — stricter than Pangolin, and stable whichever
side the routes sit on.

As for public resources, the base domain must be registered and verified in the
organisation or the apply throws. That check is not yet mirrored locally, and it
fails the whole document when it trips.
