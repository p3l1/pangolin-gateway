# Access rules

Notes on how Pangolin evaluates a resource's access rules, verified against
[`fosrl/pangolin`](https://github.com/fosrl/pangolin) at tag `1.22.0`. The
documentation and the source disagree in places; the source is what runs.

## Rules are evaluated before authentication

`server/routers/badger/verifySession.ts` checks a resource's rules, then falls
through to the authentication methods. The three actions are not three shades of
the same thing:

| Action | Internally | Effect |
|---|---|---|
| `allow` | `ACCEPT` | returns immediately — **authentication is skipped entirely** |
| `deny` | `DROP` | the request is refused |
| `pass` | `PASS` | falls through to the authentication checks |

`pass` opens nothing. It produces exactly what happens when no rule matches at
all, and it exists to carve an exception out of a broader rule: deny a whole
country, but let one CIDR through to the normal login.

**Serving a few paths of an SSO-protected resource unauthenticated therefore
needs `allow`, not `pass`.** That is a real authentication bypass for everything
the rule matches, which is why this controller validates the pattern rather than
forwarding whatever it is given.

## The first matching rule wins

Rules are sorted by ascending `priority` and evaluated in that order; the first
one that matches decides, and a disabled rule is skipped. A rule without an
explicit priority is assigned `index + 1`, and Pangolin rejects a resource whose
priorities collide — including collisions between explicit and auto-assigned
ones.

This controller therefore does not expose `priority` at all. The order of the
annotated list is the priority, which makes a collision impossible to express.
`enabled` is likewise not exposed: a rule that should not apply is removed.

## Path values are whole-path globs

Matching is segment-based and the pattern must consume the entire path
(`server/lib/pathMatch.ts`):

- `*` as a whole segment matches zero or more segments — `/api/*` matches
  `/api` and `/api/a/b`
- `*` inside a segment is a wildcard within that segment — `/assets/*.js`
- leading and trailing slashes are insignificant
- the request path is percent-decoded and `.`/`..` are resolved first, so
  `/public/../admin` is matched as `/admin` rather than swallowed by `/public/*`

Values are additionally checked by `isValidUrlGlobPattern`
(`server/lib/validators.ts`), which is **stricter than the matcher**: it permits
letters, digits, `-._~!$&'()*+,;#=@:` and percent escapes, and forbids empty
segments except a trailing one. Notably `?` is rejected in a value even though
the matcher understands it. `internal/gateway/rules.go` mirrors that validator.

## An invalid value fails the entire apply

`validateRule` throws inside the same transaction as everything else, so one bad
`ip`, `cidr` or `path` value rejects the whole blueprint — every other route
included. The renderer checks those three itself and refuses the single route,
which is the same pattern it already applies to duplicate `full-domain` values
and unknown sites.

`country`, `asn` and `region` cannot be checked this way. They depend on MaxMind
databases configured inside the Pangolin instance (`server.maxmind_db_path`,
`server.maxmind_asn_path`), and no API reports whether those are present — a
well-formed `country` rule still fails validation on an instance without the
database. Those three match types are passed through unchecked, and a value
Pangolin refuses takes the whole apply down with it. The README says so at the
point of use.

## Rule evaluation is switched on by the rules array

Pangolin derives `applyRules` from `rules && rules.length > 0`, so no separate
switch is sent. The array must always be present in the blueprint: its schema
accepts an array or no key, but never `null`, and an absent key leaves the
stored setting untouched — which would keep evaluating the rules of a route
whose last rule was just removed. `pangolin.Rules` marshals a nil slice as `[]`
so this cannot be got wrong by constructing a resource without rules.
