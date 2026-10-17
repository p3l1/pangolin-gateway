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

## Two sources: HTTPRoutes and the PrivateResource CRD

`PrivateResourceSchema` takes `host`, `cidr`, `http`, `ssh` and `inference`
(1.24.0 adds `gateway`, alias `exit-node`). Only `http` has a counterpart in the
Gateway API:

- `host` and `cidr` describe a destination reachable *from the site* — an IP, a
  hostname or a CIDR block, with TCP and UDP port ranges and an alias — and often
  no Kubernetes workload behind it at all. There is no Gateway API object that
  says this honestly. `TCPRoute` does not: it names a listener port onto a
  Service, which is Pangolin's *public* tcp mode, and it lives in the
  experimental channel whose CRDs are not installed by default.
- `ssh` and `inference` are further from HTTP still.

So a route annotated `pangolin.p3l1.de/visibility: private` becomes a `mode:
http` private resource, and `host`, `cidr` and `http` are also available through
the `PrivateResource` kind in `pangolin.p3l1.de/v1alpha1`, whose fields map
one-to-one onto the blueprint keys. `ssh`, `inference` and `gateway` remain out
of scope, as do `labels`, `enabled`, `auth-daemon` and the AI keys.

Both sources share one blueprint apply, one prune and one collision check. A
PrivateResource is keyed `gw-pr-<namespace>-<name>`, so a route and a resource
of the same name do not compete for a key; the `gw-` prefix keeps both inside
what the prune may delete.

## The keys a PrivateResource sends, verified at 1.22.0

| CRD field | Blueprint key | Modes | Notes |
|---|---|---|---|
| `displayName` | `name` | all | Defaults to `<namespace>/<name>` |
| `mode` | `mode` | all | `protocol` is not involved here |
| `sites` | `sites` | all | Defaults to `--default-site` |
| `destination` | `destination` | all | IP or hostname; a CIDR block for `cidr` |
| `tcpPorts`, `udpPorts` | `tcp-ports`, `udp-ports` | host, cidr | Always sent, see below |
| `disableIcmp` | `disable-icmp` | host, cidr | Always sent |
| `alias` | `alias` | host | Required when `destination` is a hostname |
| `fullDomain` | `full-domain` | http | Required |
| `ssl` | `ssl` | http | Defaults to true, always sent |
| `scheme` | `scheme` | http | `http` or `https`, defaults to `http` |
| `destinationPort` | `destination-port` | http | Required |
| `roles`, `users`, `machines` | same | all | See *Grants* below |

**Ports default to all, not none.** The schema defaults an absent `tcp-ports`
and `udp-ports` to `"*"`, and `parsePortRangeString` (`server/lib/ip.ts`) turns
`"*"` into "every port" and `""` into a single dummy range on port 0 — nothing.
The CRD reads the other way round, closed unless named, so the renderer sends
both keys on every host and cidr resource, empty when the spec says nothing. A
blueprint migrated verbatim that only names `tcp-ports` therefore loses the
`udp-ports: "*"` it had by default; name `udpPorts: "*"` to keep it.

**`mode: http` ignores the port keys.** The applier forces `tcp-ports` to
`443,80`, `udp-ports` to empty and `disable-icmp` to true, and defaults `ssl` to
true. The renderer refuses those fields on an http resource rather than sending
values that would be overwritten.

**An alias must be unique in the organisation.** Both the create and the update
path query `siteResources.alias` and throw `Alias … already in use` inside the
transaction, failing the whole document. The renderer resolves alias collisions
the same way as hostnames — oldest wins — and counts aliases, full-domains and
route hostnames as one namespace, compared case-insensitively. An alias held by a
resource this controller does not own — one made in the dashboard or by a
hand-written blueprint — is not visible to it, and still fails the apply.

**The schema's refinements are mirrored**, because each fails the whole
document: a host destination must be an IP or a hostname, and a hostname needs
an alias without wildcards (the refine tests it against the plain domain
pattern); a cidr destination must parse as a CIDR block; an alias must be a
fully qualified name (wildcards `*` and `?` allowed); port lists must be `"*"`,
empty, or ports and ranges between 1 and 65535.

## What a private resource does not have

No `auth` block, no `targets`, no `rules`. Access is granted by `roles`, `users`
and `machines`; `sso`, healthcheck and access-rule annotations have nothing to
map onto, so the renderer refuses a private route that carries one rather than
dropping it silently.

## Grants on a private resource are always authoritative

Verified in `server/lib/blueprints/privateResources.ts` at 1.22.0 and 1.24.0.
The schema defaults `roles`, `users` and `machines` to `[]`, so after parsing an
omitted list and an empty one are the same thing. The update path then deletes
every row in `clientSiteResources`, `userSiteResources` and — except the admin
role's — `roleSiteResources`, and inserts the lists back. **Every apply replaces
all grants**, and a resource published without any is reachable by org admins
alone. That is a safe default, not an error, but it means a grant made in the
dashboard lasts only until the next pass. This differs from a public resource,
where omitting `sso-roles`/`sso-users` leaves the dashboard alone (see below).

How each list is resolved:

- **`roles`** by name. **A role that does not exist is created**, with the
  default role actions, so a typo does not fail the apply — it quietly creates
  an empty role that grants nobody anything. This is the opposite of the site
  check. `Admin` is refused by the schema and fails the whole document; the
  admin role keeps access regardless.
- **`users`** by username *or* e-mail address
  (`or(inArray(users.username, …), inArray(users.email, …))`), restricted to the
  organisation's members. One identifier can therefore match two accounts that
  share it — say an internal user and an IdP user with the same address.
- **`machines`** — this spelling, plural — by client `niceId`
  (`inArray(clients.niceId, …)`), in any state and including user devices.

A name that resolves to nothing is dropped silently. A list *none* of whose
names resolve is worse: the code inserts an empty array, which the database
layer refuses with `values() must be called with at least one value`, and the
whole document fails. That holds for `machines` at 1.24.0 still, and for `users`
at 1.22.0 (1.24.0 guards it; 1.23 was not checked).

The controller therefore checks both before the apply, like sites: it lists
`GET /org/{orgId}/clients?status=active,blocked,archived` (action `listClients`)
and `GET /org/{orgId}/users` (action `listUsers`), only while some object names
a machine or a user. An object whose list resolves to nothing is rejected; one
whose list resolves only in part is published, since rejecting it would revoke
access for every name that does resolve, and its `Accepted` message names the
ones Pangolin will drop. The clients listing returns machine clients only, so a
user device's niceId counts as unknown; grant the user instead. When either
listing fails, its check is skipped with a logged error and Pangolin is the
authority again. Both listings honour `page` and report it back, and a page
without a pagination total is treated as a failed listing.

## Fields Pangolin decides for itself

For `mode: http` the applier forces `tcp-ports` to `443,80`, `udp-ports` to
empty, `disable-icmp` to true, and defaults `ssl` to true. A private route
therefore carries `name`, `mode`, `sites`, `destination`, `destination-port`,
`full-domain`, `scheme` and the grants; a PrivateResource adds an explicit `ssl`.

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

## Grants sit in different places

`roles` and `users` reach a private resource on the resource itself, as `roles` and
`users`. A public resource takes them inside its `auth` block instead, as `sso-roles`
and `sso-users` — Pangolin's schema puts authentication there, and `auth` exists only
for HTTP public resources
([docs](https://docs.pangolin.net/manage/blueprints)). The controller therefore reads
one pair of annotations and writes it to whichever place the chosen visibility uses.

On a **public** resource both keys are omitted when the route annotates neither,
which leaves whatever the dashboard granted untouched. (On a private one omission
revokes, as described above.) Verified in `server/lib/blueprints/publicResources.ts`
at `1.24.0`: every path guards on the key being present — `if (resourceData.auth?.["sso-roles"])`
on create, `!== undefined` on update — and skips it otherwise.

A key that *is* present is authoritative. The setters remove what the list omits
(`if (role && !ssoRoles.includes(role.name))`), so deleting an entry from the annotation
revokes that access. An empty list would therefore revoke everything; Go's `omitempty`
never sends one, so annotating a route is the only way to hand its access list to Git.

