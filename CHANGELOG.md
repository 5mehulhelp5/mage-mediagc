# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-09-23

First release.

### Added

- `scan` — read-only analysis of `pub/media/catalog/product`: indexes the media
  tree in parallel, collects every reference that can keep an image alive, and
  reports live files, orphaned files, missing files, derived cache usage,
  per-directory orphan hotspots and database orphan statistics.
- `list` — emits bare paths (`--kind orphan|live|missing`) for piping into
  `rsync --files-from` and similar tools.
- `cache stats` / `cache clean --apply` — measure or empty Magento's derived
  thumbnail cache, which Magento regenerates on demand.
- `quarantine --apply` — moves orphaned originals into a holding directory using
  same-filesystem renames, writing a JSONL manifest so the operation is exactly
  reversible.
- `restore --apply` — moves quarantined files back, optionally restricted with
  `--only`. Existing files at the destination win; unrestorable entries stay in
  the manifest so the command can simply be re-run.
- `purge --apply` — deletes the holding directory to actually free the space.
  Refuses to delete a directory it did not create unless `--force` is given.
- `db-clean --apply` — deletes EAV and gallery rows whose product or gallery
  entry no longer exists, in bounded batches, each inside its own transaction on
  a dedicated connection with foreign key checks disabled for the duration.
  Sweeps the ordered table list repeatedly, because deleting a row can orphan
  rows elsewhere.
- `verify` — re-runs the analysis and prints a checklist for after a cleanup.
- `config show` / `config template` — print the resolved configuration with
  passwords redacted, or a fully commented YAML template.
- Configuration resolved from five sources in increasing precedence: built-in
  defaults, a YAML file, Magento's `app/etc/env.php` (gap-filling only, never
  overwriting), `MAGEGC_*` environment variables, then command-line flags.
- A hand-written PHP lexer and parser for `app/etc/env.php`, so the database
  credentials, media path and table prefix are read with no PHP runtime, no
  `bin/magento` and no Composer on the host.
- Report output as a console table, JSON or a self-contained Markdown document.
  Reports are localized (`output.language`, `--language`, default `en`);
  logs, errors and the JSON payload are always English.
- Reference collection covers more than the gallery: product image-role
  attributes, category image attributes, images embedded in product
  descriptions, and images embedded in CMS pages and blocks.
- Magento's placeholder directory (`catalog/product/placeholder/`) is protected
  unconditionally. Those images are referenced only from `core_config_data`,
  which is a configuration table rather than a media table, so reference
  collection cannot see them; without the guard a stock installation would have
  its placeholders reported as orphans.
- Safety rails: dry run by default on every mutating command, same-filesystem
  enforcement for quarantine, an orphan-ratio abort threshold
  (`cleanup.maxDeleteFraction`, default 0.98), conservative case-insensitive
  path matching, and table-existence validation before any `DELETE`.
- Cross-device protection on `quarantine`, overridable with
  `--allow-cross-device`.
- `scan.excludeGlobs` to skip staging directories left behind by importers.
  This one is config-file only: it describes the installation, not a single run.

### Engineering

- Continuous integration: `gofmt`, `go vet`, `golangci-lint` (24 linters),
  `go mod tidy` idempotence, `go test -race` on Go 1.21–1.24 across Linux and
  macOS, an end-to-end integration suite against real MySQL 5.7 **and** 8.0,
  and a cross-compilation matrix.
- The integration suite builds a miniature but faithful Magento catalog schema,
  fills it with the debris the tool exists to remove, and asserts the full
  scan → analyze → dry run → clean → quarantine → restore → purge pipeline,
  including the cascade where removing a product's last gallery link orphans
  the gallery entry and then its per-store values.
- Release pipeline: goreleaser archives for linux/darwin/windows on amd64 and
  arm64, `deb`/`rpm`/`apk` packages, SHA-256 checksums, a grouped changelog, and
  a multi-architecture image on GHCR.
- Supply chain: CodeQL (`security-and-quality`) on push and weekly, plus
  Dependabot for Go modules, GitHub Actions and Docker.
- systemd units for a weekly read-only scan and a daily thumbnail-cache clear,
  both jittered, hardened and disabled by default. Quarantining and database
  cleanup are never scheduled.

[Unreleased]: https://github.com/shuaiZend/mage-mediagc/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/shuaiZend/mage-mediagc/releases/tag/v0.1.0
