# mTLS client certificates

How the controller publishes a route's trusted client-certificate CAs, and why it
verifies the result. Checked against
[`fosrl/pangolin`](https://github.com/fosrl/pangolin) at tag `1.25.3`, where the
feature landed (`1.25.0`).

## What Pangolin stores

mTLS is an auth method on a public resource. In the blueprint it is
`auth.mtls = { enabled, "ca-certificates": [<PEM>…] }`: an array of PEM strings,
each optionally a bundle, deduped by SHA-256 fingerprint and capped at 20. It is
enforced at the TLS handshake by Traefik, so it stacks **AND** on top of the
app-layer auth (SSO, PIN, basic auth) rather than replacing it. The private key
never leaves the client; the controller only ever publishes the CA's public
certificate.

## The open-source gate, and why the controller verifies

The blueprint `mtls` schema is resolved at build time. On the **open-source**
build it compiles to `z.object({})`, so Zod silently strips the `enabled` and
`ca-certificates` keys and the apply still returns 201 — mTLS stays off while the
route looks published. The real schema ships only in the **Enterprise/SaaS**
builds (`server/private/lib/blueprints/MtlsSchema.ts`, under a commercial
licence). The open-source release notes did not flag this, so it is visible only
in source.

A security control that fails silently is worse than none. So after every
successful apply the controller reads the resource's trusted CA set back through
`GET /public-resource/{id}/mtls/certificates` (action
`listResourceMtlsCertificates`) and compares it, by SHA-256 fingerprint, against
what it sent. A set that is empty, mismatched, or unreadable flips that route
alone to `Accepted: False / MtlsNotApplied`. The read route lives in the
open-source tree and returns an empty list there, so the check correctly reports
mTLS inactive on a build that cannot enforce it. The failure is per route: one
route's `MtlsNotApplied` never disturbs the others, and it is skipped entirely in
a dry run, where nothing was written to confirm.

## Always sent, so removal clears

Unlike the healthcheck columns, Pangolin's cert sync only runs when the
`ca-certificates` key is present, and leaves the stored set untouched otherwise.
So the controller emits `auth.mtls` on **every** public resource on every pass:
`{enabled:true, ca-certificates:[…]}` when a route asks for it, and
`{enabled:false, ca-certificates:[]}` when it does not. Removing the annotation
therefore clears Pangolin's set on the next pass rather than leaving a stale
requirement behind — the same discipline basic auth follows.

## A bad CA fails the whole apply

A `ca-certificates` entry containing a private key, an unparseable certificate, or
more than 20 certificates makes Pangolin throw inside the apply transaction, which
rolls the entire document back with a 400 — every other route unpublished with it.
So the controller validates the CA before it is ever sent: it parses each PEM,
rejects a private key or an unparseable block, caps the set at 20, and rejects the
route locally (`UnsupportedValue`) when any of that fails.

## The three CA sources, and step-ca

The CA is named by one of three annotations, resolved in the route's own
namespace (never cross-namespace):

- `pangolin.p3l1.de/mtls-ca` — an inline PEM.
- `pangolin.p3l1.de/mtls-ca-secret` — a Secret, read from `ca.crt`, falling back
  to `tls.crt`. This covers a cert-manager CA Secret and any issued certificate's
  Secret alike; the Secret type is not checked, only the key.
- `pangolin.p3l1.de/mtls-ca-configmap` — a ConfigMap, read from `ca.crt`, falling
  back to `root_ca.crt`.

The controller does not run a CA. It publishes the trust anchor and nothing more;
issuing and rotating short-lived client certificates is the CA's job, for example
step-ca serving ACME to cluster-external clients. step-ca needs no special support
here: its root certificate lives in a ConfigMap as `root_ca.crt`, which the
ConfigMap source reads directly, so pointing a route at it is the whole
integration.
