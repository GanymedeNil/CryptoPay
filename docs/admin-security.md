# Admin account security

## Two-step verification (TOTP)

- Every admin can enable two-step verification under **Account security** (the user menu in the top right, or the sidebar): enter the current password → scan the QR code with an authenticator app (Google Authenticator, 1Password, Microsoft Authenticator, Authy, …) or type the grouped secret by hand → enter the 6-digit code to confirm. The algorithm is RFC 6238 (HMAC-SHA1, 6 digits, 30 seconds); one time step either side is accepted, and a code can be used only once.
- Once enabled, signing in takes two steps: email and password first, then the 6-digit code from the authenticator (within 5 minutes, at most 5 wrong attempts, after which the admin has to sign in again; repeated failures on one account are blocked for a while). Enabling two-step verification signs the account out of its sessions on other devices.
- **Recovery codes**: enabling shows 10 one-time recovery codes (shaped like `abcde-fghjk`) **only once**. Copy them or download the `.txt` and keep them offline. If the phone is lost, use one of them on the second sign-in step instead; each code works once, and an email tells the admin how many remain after use. An admin can generate a new set under "Account security" with the password and a current code (all the old ones stop working).
- Authenticator secrets are stored encrypted with `APP_SECRET`; recovery codes and sign-in challenge tokens are stored only as SHA-256 hashes. None of these ever appears in the audit log, the application log or any email.
- **Require it for every admin**: turn on "Require two-step verification for every admin" under "Settings → Security" (only an admin who already has two-step verification can turn it on). While it is on, admins who have not set it up can only see the setup flow after signing in, and every other endpoint returns `403 two_factor_setup_required` until they finish; nobody can turn off their own two-step verification meanwhile.
- **Reset**: an owner can reset another user's two-step verification on the Admins page (it deletes the secret and recovery codes, signs the user out of every session and emails them). Owners cannot reset their own; turn it off under "Account security" instead.
- **Break glass**: if the only owner has lost the authenticator and has no recovery code, run this on the server:

  ```bash
  docker compose exec backend /usr/local/bin/gateway reset-2fa --email owner@example.com
  ```

  The command removes that user's two-step verification, signs them out of every session, writes an audit log entry (actor `cli:reset-2fa`) and, if email is configured, notifies the user. They can then sign in with the password alone; if "Require two-step verification" is on, they have to set it up again after signing in.

## Session management

The "Account security" page lists all of your active sessions (IP and browser at sign-in, with the current device marked). You can sign out a single session or every other session at once. Changing the password or enabling two-step verification also signs out the other sessions.

## Email notifications

Once SMTP is configured (see [Configuration](configuration.md) and [`.env.example`](../.env.example)), the gateway sends email through an outbox (failures are retried after `min(2^n × 30s, 2h)`, at most 8 times):

- **Security notices** (sent to the admin concerned; controlled by "Settings → Email notifications → Security emails", on by default): sign-in from a never-seen IP (not sent for an account's first sign-in), repeated failed sign-ins that temporarily blocked the account (at most one email per account per hour), password changed (by the admin or an owner), two-step verification enabled / disabled / reset by an owner, recovery codes regenerated, sign-in with a recovery code (including how many remain), account created, role changed, deactivated / reactivated.
- **Operational alerts** (off by default; recipients are the "Alert recipients", or every active owner when the list is empty): a webhook event finally failed to deliver (at most one email per merchant per hour), a transfer that cannot be matched arrived (at most one per chain per hour), an invoice became underpaid (one per invoice), a chain's scanner has been failing for more than 10 minutes (at most one per chain every 6 hours). Alerts are raised asynchronously after the database transaction commits, so a failing or slow email never affects payment processing.
- The email language follows the "Email language" setting (Chinese / English); all times are shown in UTC. Emails never contain passwords, secrets, verification codes or tokens.
- The "Email log" page shows the outbox, filters it by status / type, shows message bodies and retries failed emails; "Settings → Email notifications" can send a test email and shows the error SMTP returned.
