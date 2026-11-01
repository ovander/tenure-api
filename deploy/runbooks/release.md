# Release and rollback

A release is a tag `vX.Y.Z` on `main`, pushed by the owner. The `release` workflow runs the gate,
embeds the tenure-web release pinned in `web/VERSION`, builds the stamped binary and publishes:

- `tenure-<version>-linux-amd64`;
- `tenure-<version>.cdx.json`, the CycloneDX SBOM;
- `tenure-deploy-<version>.tar.gz`, this directory;
- `SHA256SUMS`.

## Deploy

1. Read the release notes and the migrations it adds (`migrations/`). A migration marked not
   reversible needs a backup taken just before the deploy:
   `systemctl start tenure-backup` and check its log.
2. On the host, as root, in a scratch directory, download the release assets, then run
   `/opt/tenure/deploy/deploy.sh <version> .`.

`deploy.sh` checks the checksum, migrates with the new binary while the old one still serves,
swaps `/opt/tenure/current`, restarts tenure and waits for `/readyz`. If tenure is not ready
within 60 s, it puts the previous binary back and fails.

3. Check `tenure version`, `/healthz` and the last `job_runs`. Log in through the browser.

## Roll back

- **Binary only** (the usual case): the migrations of a release are additive, so the previous
  binary runs on the new schema.
  1. Point `/opt/tenure/current` at `/opt/tenure/releases/<previous>`.
  2. Run `systemctl restart tenure`.
- **Schema as well** (a migration is wrong):
  1. Stop tenure.
  2. Run `tenure migrate down` with the new binary, once per migration to revert. Each `Down`
     section is the migration's own revert.
  3. Start the previous binary.
- A migration marked not reversible is reverted only by restoring the backup taken before the
  deploy (`restore.md`).

Each rollback is noted in the release's GitHub issue with its reason.
