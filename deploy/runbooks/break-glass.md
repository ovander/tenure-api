# Break-glass

Break-glass is access outside the normal path, used only when that path is down: Socrate
unavailable, the host unreachable, or a key lost. Every use is logged in a private GitHub issue
labelled `break-glass`, with who, when, why and what was done. Every secret used is rotated
afterwards.

## What is kept offline

The safe is an encrypted password manager vault held by the owner, plus a paper copy in a
locked place. It holds:

- the age identity of the backups (`AGE-SECRET-KEY-…`): without it no backup can be read;
- the PostgreSQL superuser password;
- the root SSH key of the host and the provider console credentials (Scaleway, with MFA);
- the S3 keys of the archive and backup buckets;
- the Socrate admin recovery codes.

Nothing in the safe is ever committed, pasted into an issue or sent by e-mail.

## Situations

- **Socrate down.** tenure fails closed: no login, the API answers 401 and the jobs keep running.
  There is no local login to fall back on, by design. Wait for Socrate, or restore it
  (ovander/go-oauth2 runbooks). Do not add a bypass.
- **Host unreachable.** Use the provider console with MFA. If the host is lost, rebuild it from
  `deploy/README.md`, restore the latest backup (`restore.md`) and deploy the current release.
- **Database access needed for a repair.** Connect as `tenure_migrate`, or as the superuser in a
  single transaction that is reviewed before commit. Never connect as `tenure_api`, never
  `UPDATE` an audited table by hand (audit events are INSERT-only), and never disable RLS. Any
  correction of business data goes through the API, so that it is audited.
- **Backup identity lost.** Generate a new age key pair, put the public key in
  `/etc/tenure/backup.env`, run a backup at once and store the new identity in the safe.
  Backups made with the old key become unreadable after 35 days at most.
