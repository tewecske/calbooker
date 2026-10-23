# Backup and Retention

This document records what calbooker keeps, how long, what cleanup does, and
the expectations for datastore backups. See the
[deployment runbook](deployment.md) for deploy ordering and the
[first administrator procedure](first-admin.md) for first-admin setup.

## Data inventory and retention

| Data | Table | Retention setting | Cleanup behavior |
| --- | --- | --- | --- |
| Empty abandoned guests | `users` (`kind = 'guest'`; profiles and accounts are never removed) | `CALBOOKER_GUEST_RETENTION` (`720h`) | `guest_cleanup` removes guests older than the cutoff that own no group membership. |
| Sign-in flow states | `oauth_states` | not age-based | `token_retention` removes consumed or expired states. |
| Usage events | `usage_events` | `CALBOOKER_USAGE_RETENTION` (`2160h`) | `usage_retention` removes events older than the cutoff. |
| Rate-limit windows | `rate_limit_buckets` | live for one window | `rate_limit_retention` removes expired windows. Keys are stored only as hashes. Not worth backing up. |
| Administrator audit log | `audit_log` | not automatically removed | Preserved as the historical record of administrator actions. |
| Sessions and identities | `sessions`, `oauth_identities` | not age-based | Expired and revoked sessions are rejected at lookup; rows are removed when their account is deleted. |

Notes:

- Retention cleanup only removes expired, consumed, or over-age records. A
  negative or missing cutoff is rejected rather than treated as "delete all".
- Guest cleanup locks candidate accounts before checking membership so a
  concurrent join cannot be cascaded away.
- Account deletion cascades owned rows (sessions, claim codes, identities,
  memberships) while usage and audit rows survive with their account reference
  set to `NULL`. See `internal/store/deletion_rules_integration_test.go`.
- The audit log is intentionally not time-limited. If a regulatory retention
  limit applies, export and prune it under a separate, reviewed procedure.
- Sessions keep `sso_sid` and the ID token used as the sign-out hint until the
  session row is removed, so backups contain ID tokens for unexpired sessions;
  treat them like other session data.
- Hydra's and the provider's data live in the goweb deployment's databases and
  are backed up there.

## Cleanup schedule

The maintenance worker runs every `service.DefaultMaintenanceInterval` (six
hours) and registers four jobs:

- `guest_cleanup`
- `token_retention`
- `usage_retention`
- `rate_limit_retention`

Each run is independent: one job failing does not cancel the others, and the
worker keeps its last-run state in process memory. Current status and a manual
"run now" action are available to administrators on
`/{language}/admin/system`. Retention periods are read once at startup, so
changing them requires a restart.

## Backup expectations

- Scope: back up the whole `calbooker` database (schema and data).
- Cadence: take a backup before every deploy, before any destructive migration,
  and on a regular schedule that matches your recovery point objective.
- Method:

  ```sh
  pg_dump --format=custom "$CALBOOKER_DATABASE_URL" > calbooker-$(date +%Y%m%dT%H%M%S).dump
  pg_restore --clean --if-exists --dbname "$CALBOOKER_DATABASE_URL" calbooker-<timestamp>.dump
  ```

- Verify restores: restore into a disposable database on a schedule and run the
  integration suite against it.
- Protect backups: encrypt at rest, restrict access to operators, and never copy
  them into the repository, CI artifacts, logs, or tickets.
- Align backup retention with privacy commitments: older backups may still
  contain rows that cleanup removed from the live database.

## Verifying retention

- Inspect current job status on `/{language}/admin/system`, then run a job
  manually if needed.
- Integration coverage lives in `internal/store/postgres/retention_integration_test.go`,
  `internal/store/postgres/guest_integration_test.go`, and
  `internal/store/deletion_rules_integration_test.go`.
