# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue. Use GitHub's private reporting channel:
[Report a vulnerability](https://github.com/shuaiZend/mage-mediagc/security/advisories/new).

Include, as far as you can:

- the version (`mage-mediagc version`) and how it was obtained (release binary,
  package, container image, built from source);
- what an attacker can do, and what access they need first;
- a minimal reproduction, ideally the smallest config and media tree that shows it;
- whether the issue also affects the database cleanup path.

You will get an acknowledgement, and credit in the advisory unless you prefer
otherwise.

## What counts as a vulnerability here

This tool is unusual: it holds credentials to a production database and moves or
deletes files on a production shop. So the interesting classes are not only the
usual memory-safety and injection ones.

**In scope:**

- **An orphan misclassification that can lose a live image.** If a
  configuration, schema or content pattern makes a referenced image look
  orphaned and therefore quarantine- or purge-able, that is a vulnerability.
  This is the failure mode the whole design is built to prevent.
- **Escaping the configured roots.** A relative path, symlink, crafted database
  value or glob that causes a read, move or delete outside the media tree or the
  quarantine directory.
- **Destructive operations without `--apply`.** Any path where a mutating command
  changes state on a dry run.
- **SQL injection** through configuration, table prefix, attribute names or
  values read from the database.
- **Credential disclosure** — printing a password in a report, log, error or the
  `--format json` payload, or including it in an error that reaches a bug report.
- **Privilege escalation via the packaging.** Anything in the `deb`/`rpm`/`apk`
  install scripts, the systemd units or the container image that runs with more
  privilege than it needs.
- Supply-chain issues in the release pipeline: unsigned or unverifiable
  artefacts, a writable action, a leaked token.

**Out of scope:**

- Running `purge --apply`, `db-clean --apply` or `--force` deliberately, and
  losing data as a result. That is the documented behavior, and the reason
  those commands require an explicit flag.
- Deleting a quarantine directory by hand and then being unable to `restore`.
  Use `purge`, which requires the manifest.
- Issues that require an attacker who can already modify `app/etc/env.php` or
  the config file, or who has arbitrary shell access as the invoking user. At
  that point they can do anything the tool can.
- Vulnerabilities in Magento itself, or in MySQL/MariaDB. Report those upstream.
- A shop legitimately having a very high orphan ratio. That is the condition the
  tool exists to detect; the ratio guard is a safety rail, not a guarantee.

## Design posture

The relevant guarantees, and where they are enforced:

| Guarantee | Mechanism |
| --- | --- |
| No accidental deletion | Dry run is the default; `--apply` is required, and there is no `--dry-run` flag to get wrong |
| No data loss on isolation | Files are *moved* by `os.Rename` into a quarantine directory, never deleted; a JSONL manifest records every move |
| Exact rollback | `restore` replays the manifest; a file already present at the destination is skipped, never overwritten |
| Bounded blast radius | `cleanup.maxDeleteFraction` (default 0.98) aborts a run whose orphan ratio implies reference collection failed |
| No cross-device surprises | Quarantine refuses to leave the filesystem, because a silent copy both doubles disk usage and can fail halfway |
| Conservative classification | A file is live if *any* candidate path matches, including case-insensitively. A false "live" costs space; a false "orphan" loses an image |
| No SQL built from unvalidated input | Identifiers are validated and quoted; values are parameterized |
| Database deletes are bounded | Per-table existence checks first, then bounded batches, each in its own transaction, on a dedicated connection |
| Least privilege | Only `db-clean` needs write access; every other command works with a read-only database user |
| Verifiable releases | SHA-256 checksums, CodeQL on push and weekly, Dependabot, and goreleaser pinned to an exact version |

## Hardening recommendations for operators

- Run `scan` with a **read-only** database user. Grant write access only when you
  intend to run `db-clean`.
- Prefer `MAGEGC_DB_PASSWORD` over `--db-password`, so the password does not
  appear in `ps` output or your shell history.
- Keep the quarantine directory on the same filesystem as the media tree, and
  outside the web root.
- Back up the database before `db-clean --apply`. The tool cannot roll that back
  for you.
- Run `quarantine --apply`, then **wait a full business cycle** before
  `purge --apply`. The quarantine is the safety net; purging is what removes it.
- Keep the shipped systemd units disabled or limited to the read-only scan, and
  never schedule `quarantine`, `purge` or `db-clean`.

## Supported versions

Fixes are released for the latest `0.x` release. As a pre-1.0 project, minor
versions may change behavior; the changelog records anything that does.
