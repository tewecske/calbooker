# Security Threat Model

This document records the trust boundaries, bearer credentials, abuse controls,
data ownership, and operational assumptions for calbooker. It describes the
system as implemented; update it when boundaries, credentials, or controls
change.

Calbooker is a single sign-on client of the goweb provider through Ory Hydra.
It stores no passwords and offers no password, recovery, or email confirmation
flows; those threats belong to the provider's threat model (goweb's
`docs/threat-model.md`).

## Scope and assets

In scope: the HTTP application (`internal/adapter/http`), application services
(`internal/service`), persistence adapters (`internal/store`), the OpenID
Connect relying party (`internal/adapter/oidc`), the PostgreSQL database, the
configured mail delivery, and the administrator surface.

Assets to protect:

- Session credentials and the authority they carry.
- Bearer credentials: guest transfer codes and group invite codes.
- Sign-in flow state, issuer subjects, and the ID token hints stored with sessions.
- Administrator audit history.
- Deployment secrets: database URL, session secret, single sign-on client
  secret, mail relay or Resend API key.
- Operational data: usage events, request IDs, trace IDs.

## Trust boundaries

| Boundary | Crosses from → to | Controls |
| --- | --- | --- |
| Browser → HTTP adapter | Untrusted input | Server-side validation, `html/template` escaping, CSRF, cross-origin request rejection, authorization checks in every handler, size-bounded lists, 256 KiB request bodies, server read/write/idle timeouts; Content-Security-Policy allows same-origin scripts and styles only and forbids framing |
| Reverse proxy → HTTP adapter | Network metadata and trace context | `X-Request-ID` generated server-side; incoming W3C `traceparent` continued only from `CALBOOKER_TRUSTED_PROXY`; client IP taken from the right-most untrusted `X-Forwarded-For` hop only when the peer is `CALBOOKER_TRUSTED_PROXY`, otherwise the peer address without its port |
| HTTP adapter → services | Principal and request data | Authentication and authorization checked in each handler and fragment; the principal carries only ID and admin flag |
| Services → PostgreSQL | Query parameters and secrets | Parameterized queries via `database/sql`; the adapter maps errors without echoing bearer values |
| Services → mail delivery | Message content | Plain-text messages; never logged |
| Browser → `/{language}/oauth/sso/callback` | Authorization code and state | Single-use hashed state bound to the starting browser by the `HttpOnly` state cookie; PKCE S256; the code is redeemed server to server with `client_secret_basic`; the ID token is verified (RS256 only, key by `kid` from the issuer's JWKS or its only key when `kid` is absent, exact `iss`, `aud`/`azp`, `exp`/`iat` with one minute of skew, `nonce` equal to the PKCE challenge, no `events`) before any account is touched |
| Hydra → `POST /oauth/sso/backchannel-logout` | Signed logout token | Exempt from the browser CSRF token (server to server) but authenticated by the token: RS256 signature, exact `iss`, `aud`, `iat` within five minutes, `jti`, the back-channel `events` member, `sid`, and no `nonce` (Back-Channel Logout 1.0 §2.6); it only revokes sessions bound to that `sid`; without a configured issuer the route answers 404 |
| Operators → administrator surface | Administrative authority | Shared admin authorization guard, per-action audit records mirrored to a separate security log stream with actor and target IDs; privilege grants and revocations have their own action codes; site administrators may manage any group (their group actions are marked as such in the audit detail); a record that cannot be stored is still reported to the security stream as failed |
| Application → logs/traces/usage | Diagnostic output | Stable low-cardinality fields; no sessions, tokens, issuer subjects, query strings, or credentials |

## Bearer credentials

| Credential | Form and storage | Exposure window | Controls |
| --- | --- | --- | --- |
| Session cookie | `calbooker_session`, opaque 32-byte random hex; stored as its SHA-256 digest in `sessions` | Until expiry or revocation | `HttpOnly`, `SameSite`, `Secure` outside development; server validates expiry and revocation on every request |
| CSRF token | `calbooker_csrf` cookie plus `_csrf` field / `X-CSRF-Token` header | Per browser session | Double-submit, constant-time compare, `HttpOnly`, `SameSite=Strict`, `Secure` whenever session cookies are, malformed cookies replaced; required on every state-changing request after `http.CrossOriginProtection` rejects cross-origin and sibling-subdomain browser requests |
| Sign-in flow state | 32-byte random state stored as its SHA-256 digest in `oauth_states`, mirrored in the `calbooker_oauth_state` browser cookie | Single use, ten minutes | Action allowlist (`sign-in`, guest upgrade `link`), atomic consumption with a single winner; the callback must present the matching `HttpOnly` cookie; a guest upgrade completes only for the guest that started it; a per-state PKCE S256 verifier is required to redeem the code |
| Guest transfer code | Single active code per guest, partial unique index | Until revoked or used | One active code per guest; revocable; redeemed atomically; rate limited; never logged |
| Group invite code | 12-character code in `group_invites.code` (unique), stored in clear so staff can share it again; carries a role, optional team, optional expiry, and optional use limit | Until revoked, expired, or used up | Shown only to accounts that may create that invite (admins: all; managers: all but admin invites; coaches: player and parent invites of teams they coach); audit entries and logs name an invite by its ID, never its code; redemption is rate limited (`group.invite.join`, keyed by account), locks the invite row and re-checks revocation, expiry, and the use limit in the same transaction; staff roles need a full account; profiles never join |
| Issuer subject | `oauth_identities` unique `(provider, subject)`, provider `sso` | Stored with the account | Never rendered or logged; removed with the account |
| ID token hint | ID token in `sessions.sso_id_token` | Until the session row is removed | Never accepted as authentication; read only to build the issuer logout URL; never rendered or logged |
| SSO binding (`sso_sid`) | Hydra session ID on the session row | Session lifetime | Identifier only; lets a verified logout token revoke exactly those sessions |
| SSO client secret | `CALBOOKER_SSO_CLIENT_SECRET` (`config.Secret`) | Process lifetime | Sent only to the issuer's token endpoint; redacted from configuration and diagnostics |
| Deployment secrets | `config.Secret` values from environment | Process lifetime | Redacted from formatting and JSON; production startup rejects unsafe configuration |

Additional sources of authority: server-generated request IDs, single-use flow
state, and revision-checked writes. None are trusted from the client.

## Abuse controls

- **Rate limits**: fixed-window budgets for `guest.create`, `guest.redeem`,
  and `group.invite.join`. Counters live in PostgreSQL (`rate_limit_buckets`),
  so every instance shares one budget and a restart does not reset it. Keys are
  stored only as SHA-256 hashes plus a redacted hint; origin budgets use the
  client IP without the port, and IPv6 clients share one budget per /64.
- **Sign-in abuse** (credential stuffing, lockouts) is handled by the provider.
- **Suspicious-account signals**: accounts exceeding action-count or
  distinct-origin thresholds are surfaced as investigation flags, not
  automatic enforcement.
- **Optimistic locking**: revision-checked writes reject stale edits as
  conflicts and vanished records as not-found.
- **Administrator invariants under concurrency**: demoting or deleting an
  administrator is serialized and refused when no other administrator would
  remain; administrators cannot demote or delete themselves.
- **Session revocation**: an administrator can end an account's calbooker
  sessions immediately. This does not end the Hydra session; the provider owns
  that, and its revocation reaches calbooker through back-channel logout.
- **Bounded work**: list page sizes, search lengths, and sort keys are validated
  and clamped server-side; query strings never reach logs, traces, or usage rows.
- **Backpressure**: the usage-event queue applies bounded backpressure and never
  fails the user request.

## Data ownership and lifecycle

- Owned rows cascade on account deletion: sessions, identities, guest claim
  codes, memberships, and flow states.
- Historical records survive account deletion with a nulled reference:
  `audit_log`, `usage_events`, and `groups.created_by`.
- The administrator audit log is preserved deliberately; retention cleanup does
  not remove it.
- Old, empty abandoned guests are deleted only when they own no group
  membership. Retention removes only `users.kind = 'guest'` rows, never
  `profile` (children without a login) or `account` rows.
- Child profiles never sign in: the session store refuses to create a session
  for a `profile` user, the session authenticator ignores one, and guest
  transfer codes, guest upgrades, and invite joins reject profiles. A profile
  is removed only by deleting it (its guardians or group staff), which
  cascades its membership, roster entries, and guardian links. Audit entries
  name profiles by ID only, never by the child's name or birth year.
- Removing a member cascades their roles and team roster entries; removing a
  group role removes the roster entries that depended on it.
- See [backup and retention](backup-retention.md) for periods and schedules.

## Operational assumptions

- TLS terminates before the application; production startup requires `https`
  public, issuer, callback, and account URLs and secure session cookies.
- Hydra and the provider are operated by the goweb deployment and trusted as
  the identity source; calbooker trusts the provider's email verification.
- Secrets are provided by the platform secret store and never committed.
- Database access is restricted to calbooker's own role; backups are
  encrypted and access-controlled.
- Only one instance performs startup migrations, serialized by a PostgreSQL
  advisory lock; the schema is forward-only.
- The host clock is trustworthy; session, token, and rate-limit windows depend
  on it.
- Mail goes through a credential-free `smtp://`/`smtps://` relay or the Resend
  API; the API key is sent only to `api.resend.com`, redirects are never
  followed, and errors carry only the status and Resend's short error code.

## Threat analysis

| Threat | Boundary | Mitigation | Residual risk |
| --- | --- | --- | --- |
| Session theft | Browser ↔ adapter | `HttpOnly`/`SameSite`/`Secure` cookie, server-side revocation and expiry | Malware on the client or a compromised proxy |
| CSRF | Browser → adapter | Double-submit token on every state-changing request, constant-time compare, cross-origin rejection | XSS would defeat any token scheme |
| Forged or replayed ID or logout tokens | Issuer → calbooker | Signature, issuer, audience, and time checks; tokens of one type are rejected as the other (`events` claim); unknown keys refresh the JWKS at most every 30 seconds | A replayed logout token only repeats an idempotent revocation |
| Session outliving revocation at the provider | Provider → Hydra → calbooker | Back-channel logout revokes every session bound to the `sid`; short `CALBOOKER_SESSION_LIFETIME` (8h) | Hydra gives up after 3 attempts; if calbooker was down, its session lives until expiry |
| Guest upgrade hijack | Browser → guest upgrade | Starting is a CSRF-protected POST by a signed-in guest; the state is bound to that guest and completes only when the callback's browser is still signed in as it; an identity that already has an account, or an email another account holds, is refused and the guest is left untouched; conversion, identity link, and transfer-code revocation commit together; every other guest session is revoked | Accounts are never merged |
| Account takeover through email collision | Issuer → account mapping | Accounts map by issuer subject; an existing account is linked by email only when both sides have a verified address; otherwise sign-in is refused | Linking trusts the provider's email verification |
| Forced logout (logout CSRF) | Browser → Hydra | Logout is idempotent and only ends sessions | A third-party page can sign a visitor out; accepted as low impact |
| Stale-write data loss | Adapter → database | Optimistic locking; conflicts distinguished from not-found | Concurrent editors must reload |
| Data leakage via logs/traces | App → observability | Secret-free structured fields, query-string stripping, redaction tests | Misconfigured downstream pipeline |
| Administrator action repudiation | Admin surface | Per-action audit records with actor, target, time, origin, mirrored to the security stream | Audit store compromise |
| Database compromise | Services → PostgreSQL | Own least-privilege role, encrypted backups, parameterized queries; sessions and flow state stored only as digests | Exposes guest transfer codes and ID token hints of live sessions |
| Denial of service | Browser → adapter | Bounded queues, clamped inputs, graceful shutdown | No in-app WAF; rely on the edge |
| Cross-group data exposure | Adapter → group services | Every group-scoped route builds a `GroupAccess` for the account first and answers 404 to non-members (tested per route), every query filters by `group_id`, team, resource, season, season part, and event rows are reached only through `(group_id, id)` (an event's teams, resource, and extra coaches by composite foreign keys), events a member may not see answer 404 (their participant lists and sign-up routes too; participant emails only for staff and the event's coaches); a call-up (`event_participants.called_up`, made only by staff and the event's coaches, audited) lets the called-up player and a linked parent see that one event and its participant list (names, answers; no emails) but never its teams' pages, rosters, or other events, is loaded per request into `GroupAccess` for members with the player role or children, and ends when the row is withdrawn or the member leaves the group, roster entries, season coach assignments, extra event coaches, coach qualifications, and coach availability reference the member's group role by foreign key; coaches see only their own availability and qualification | A site administrator sees every group by design |
| Privilege escalation inside a group | Member → group writes | Authorization only through `GroupAccess`, re-checked in the service; only admins grant or remove the admin role or remove an admin; admin, manager, and coach need a full account (guests and profiles are refused); the last admin cannot lose the role or leave; role writes are revision checked under a per-group advisory lock; a coach manages a team's roster and invites only through a current-season assignment that only admins and managers make, and only while holding the coach role; a coach creates, edits, cancels, or deletes an event only when they coach every one of its teams (before and after the change); players answer only for themselves and parents only for linked children, sign-up limits and deadlines are enforced server side under the event row lock, and only staff and the event's coaches change other members' answers or record attendance (audited); every admin and manager action is audited with the group as target | A compromised admin account controls its group |
| Invite abuse | Invite code → membership | Only admins create admin invites; coaches only player and parent invites to teams they coach; team invites carry only player or parent; guests can redeem only player and parent invites; codes can expire, carry a use limit, and be revoked at once; a repeat redemption that grants nothing uses nothing | Anyone holding an active code joins with its role; share staff codes privately and limit their uses |
| Guardian misuse | Children page → child data | Only staff link a parent to an existing account; a parent's own profiles are linked to them automatically; a link needs the parent role and is removed with it; parents see their children's teams but not those teams' emails | Staff are trusted to link the right parent |
| Member data exposure | Members page, team pages | Emails and the full member list only for admins and managers (coaches: their teams); players and parents see names on their own and children's teams only; linked parents appear on their children's team rosters by name | Display names are visible to teammates |
| Development pages in production | Browser → adapter | The week grid demo (`/{language}/dev/weekgrid`, unauthenticated, in-memory state) is registered only when `Dependencies.DevelopmentRoutes` is set, which `cmd/web` derives from `CALBOOKER_ENV=development`; tests prove production and test configuration serve 404 | A deployment started with `CALBOOKER_ENV=development` would serve it (and skip the production safety checks); the deploy env template, runbook, and NixOS module set `production` |
| Forged grid changes | Browser → week grid handlers | Drag-and-drop only proposes a change: the HTMX POST carries the CSRF token, `ParseWeekGridChange` validates weekday, resource, week, and times against the grid and snaps to its step, and the handler re-renders the grid from server state | Feature handlers must still authorize the item itself (coach availability: `GroupAccess.CanEditAvailability`, and a moved window must be one of the coach's windows of the part, else not found) |

## Out of scope

- Credentials, multi-factor authentication, and account recovery (provider).
- Edge controls (WAF, DDoS protection, HSTS) owned by the deployment platform.
- Compromised operator credentials or a compromised database host.
- Email deliverability and relay provider security.
