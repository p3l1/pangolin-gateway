# PIN sessions and the periodic apply

Verified against [`fosrl/pangolin`](https://github.com/fosrl/pangolin) at tag `1.25.3`.

## What Pangolin does on every apply

`server/lib/blueprints/publicResources.ts` updates an existing resource by
deleting its PIN and password rows and inserting them again, whether or not the
value changed. For a resource without `policy` that happens in
`syncInlinePolicyAuth` on `resourcePolicyPincode` and `resourcePolicyPassword`;
a shared policy goes through the same delete-and-insert in
`server/lib/blueprints/resourcePolicies.ts`.

A visitor who signs in with a PIN gets a row in `resourceSessions` that points at
the PIN row it was checked against (`pincodeId`, `policyPincodeId`), and those
foreign keys are `onDelete: cascade`. Deleting the PIN therefore deletes every
session it issued. With the controller applying the whole state every five
minutes, a PIN session lived at most five minutes.

SSO sessions point at the user session instead, and the e-mail whitelist is
diffed rather than rewritten, so neither is affected. Basic auth is checked on
every request and has no session.

## What the controller does about it

`internal/controller/sessions.go` remembers the JSON of every PIN-carrying
`public-resources` and `public-policies` entry after a successful apply. On the
next pass, an entry whose JSON is unchanged is left out of the document. The
blueprint is additive, so an omitted entry stays as it is; the prune and the
status still work from the complete render.

Everything else is sent on every pass as before, so drift on those entries is
still repaired. The trade-off is limited to PIN-protected entries:

- An entry deleted in Pangolin disappears from the prune's listing, is forgotten,
  and is recreated on the following pass.
- An entry edited in Pangolin's dashboard is not repaired until its route or
  policy changes, or the controller restarts.

The memory is in-process. A restart, or a new leader, sends everything once and
ends the PIN sessions open at that moment.
