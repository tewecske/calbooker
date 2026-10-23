# First Administrator

Calbooker has no local accounts: an account is created the first time someone
signs in through single sign-on. The first administrator is therefore promoted
in the database after their first sign-in. Later administrators are promoted
by an existing administrator on `/{language}/admin/users/{id}/edit`, which is
audited.

## Procedure

1. Open calbooker's sign-in page and sign in at the provider with the account
   that should administer calbooker. You land on calbooker's home page; the
   account now exists.
2. Promote it (the email must match the provider account's email):

   ```sh
   psql "$CALBOOKER_DATABASE_URL" -c "UPDATE users SET is_admin = TRUE, version = version + 1 WHERE email = lower(btrim('you@example.com')) AND is_admin = FALSE;"
   ```

   Expect `UPDATE 1`. The deployment runbooks (`deploy/SETUP.txt`,
   `deploy/SETUP-nix.txt`) run the same statement as the `postgres` superuser.
   Locally, use `docker exec goweb-postgres psql -U goweb -d calbooker -c ...`.
3. Reload calbooker. The account menu now shows the administration link.

Being an administrator at the provider does not make an account a calbooker
administrator, and the other way around: each app keeps its own flag.

## Notes

- The update bumps `version`, so open edit forms report a conflict instead of
  silently overwriting the change.
- The statement is a direct database write and is not in the audit log; record
  it in your change log.
- Calbooker always keeps at least one administrator: the last one cannot be
  demoted or deleted.
