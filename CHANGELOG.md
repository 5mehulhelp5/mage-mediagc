# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`cache warm`** — refill Magento's derived thumbnail cache by requesting the
  cache URLs through the storefront. Every original is paired with every size
  set the theme uses, and Magento generates each missing variant through the
  same code path a visitor's browser would take. Nothing has to be installed on
  the shop: no PHP runtime, no `bin/magento`, no Composer, no module. It needs
  HTTP access and nothing else, so it runs from a laptop, a bastion host or a
  CI job against a shop it has no shell on.

  This addresses the cost `cache clean` defers onto the first visitors. The
  alternative, `bin/magento catalog:images:resize`, is single-threaded,
  re-resizes variants that already exist and cannot be interrupted; `cache warm`
  skips what exists with a local `stat` and issues the rest concurrently, so an
  interrupted run resumes for free.

  Three details matter for safety against a live shop. Size-set hashes are
  **discovered** from the cache directory rather than computed — Magento derives
  them from PHP-side parameters, and a wrong hash means requesting a URL space
  that does not exist, where every request returns 200 and generates nothing.
  One request is issued first, on its own, with its cache file checked for
  afterwards: a run where requests succeed and no file appears stops
  immediately, which is what a CDN or reverse proxy answering from the edge
  looks like. And the failure rate is gated at `warm.maxErrorFraction`, so a
  wrong host or a hostile WAF is reported rather than hammered.

  New flags: `--base-url`, `--cache-hash`, `--hash-file`, `--concurrency`,
  `--timeout`, `--method`, `--user-agent`, `--rate`, `--max-requests`,
  `--max-error-fraction`, `--live-only`, `--no-probe`, and `--apply`. New
  configuration section `warm`, documented in `docs/reference.md` and
  `docs/configuration.md`, and included in `config template` and
  `examples/mage-mediagc.yaml`.
- **`cache clean --save-hashes <path>`** — record the size-set hashes to a file
  before the cache is emptied. Emptying the tree destroys the only local record
  of which size sets the theme asks for, so without this snapshot a later
  `cache warm --hash-file` has nothing to read and the size sets have to be
  re-learned from traffic.
- `internal/warm`, and the `mage-mediagc cache warm` command surface.

### Changed

- **Filesystem-only commands no longer require database credentials.**
  `cache stats`, `cache clean`, `config show` and `cache warm` with an explicit
  `--base-url` never connect to MySQL, but the shared configuration validation
  demanded a database name and user for every command, so they refused to start
  without one. Validation is now scoped to what a command actually uses
  (`ModeAnalyze`, `ModeWrite`, `ModeLocal`), which also means `config show`
  works on the machine where the database *is* the thing that is misconfigured —
  the case it exists for. Commands that do connect still report a missing name
  or user in the same words, at the same point.
- The `scan` report's recommended first step now shows `cache clean --apply
  --save-hashes var/cache-hashes.txt` and names the follow-up
  `cache warm --hash-file`, because the previous text recommended the one action
  that discards what a rebuild needs to know.
- `docs/operations.md` explains the ordering trade-off between `quarantine` and
  `cache clean`: an orphan never comes back, while every cache file does, so
  quarantining first avoids generating thumbnails for images about to be
  removed.

### Fixed

- **The shipped configuration files made every command refuse to start.**
  `mage-mediagc config template` and `examples/mage-mediagc.yaml` both set
  `scan.workers: 0` and `cleanup.parallel: 0`, under comments saying zero means
  "auto" — which is what `--workers 0` means everywhere else, and what the
  defaults use. Validation, however, rejected any count below one, so copying
  the file the README points at produced a configuration that failed before the
  user had changed a single line. Zero is now resolved to the CPU-derived
  default while configuration is loaded, and a negative count is still reported.
  Two tests cover it: one on the resolution itself, and one that loads both
  shipped files and validates them in analyze and write mode, so a config the
  project recommends cannot silently stop working again.

## [0.2.1] - 2026-09-23

### Fixed

- The database port in `app/etc/env.php` is read through the helper that accepts
  every numeric form. It was read with a bare type assertion, so a port written
  as a quoted string — which is what Magento writes — or as a float was silently
  ignored and the operator was left on the default port.
- An out-of-range port is now reported instead of quietly falling back. The
  conversion to `int` was also unchecked, which CodeQL flags as
  `go/incorrect-integer-conversion`: on a platform where `int` is narrower than
  `int64`, a value like `4294967296` wraps to `0`, which this package reads as
  "no port configured". A TCP port is 16 bits, so the bound is a constant pair.
  The same range is enforced on a port embedded in the `host` field.

## [0.2.0] - 2026-09-23

### Changed

- **Dropped the Windows build target.** Releases and CI now cover Linux and
  macOS on `amd64` and `arm64`. Magento 2 shops run on Linux, and the guards that
  make this tool safe to point at a live catalog — same-filesystem detection and
  ownership preservation — are POSIX operations. The Windows binary in 0.1.0 had
  no CI job behind it.
- The non-Unix fallback for those guards no longer approximates the check with
  `filepath.VolumeName`; it returns an error. `quarantine` therefore refuses to
  run on a platform where it cannot prove a move is an inode rename rather than a
  copy that doubles disk usage. The package still compiles everywhere, so
  `go build ./...` keeps working on any developer machine.
- Release archives are `tar.gz` on every target; the Windows `zip` override is
  gone.

### Added

- `README.zh-CN.md` — a Simplified Chinese README with a five-minute quick start
  and a worked example of the Chinese report. Both READMEs link to each other.
- `docs/reference.md` now documents localization, including the two behaviours
  that matter in practice: warnings, skip reasons and error text stay English
  because they are diagnostic strings people paste into bug reports, and the
  console `table` format misaligns CJK labels because `text/tabwriter` measures
  cells in runes rather than display columns.

### Fixed

- Every example, README and documentation page uses generic placeholders
  (`/var/www/magento`, `magento`, `admin_x7k2p`). Catalog statistics are quoted
  as rounded aggregates, because the scale is the point and exact figures say
  more about the shop than about the problem.
- The sample console output in `README.md` listed raw table names
  (`catalog_product_entity_varchar`) as reference sources, but the tool reports
  logical source names (`product_image_attr`, `product_content`, …). The block
  did not match what `scan -v` prints; it is now generated by the renderer.
- `docs/deployment.md` quoted a reference-source breakdown in a format the
  renderer has not produced since it gained a description column.
- Removed the `linux/darwin/windows` cross-compilation matrix from the README
  and the changelog.
- `internal/action/fs_portable.go` is now `fs_unsupported.go`, because describing
  what it does is more accurate than describing where it runs.

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
- Release pipeline: goreleaser archives for linux/darwin on amd64 and
  arm64, `deb`/`rpm`/`apk` packages, SHA-256 checksums, a grouped changelog, and
  a multi-architecture image on GHCR.
- Supply chain: CodeQL (`security-and-quality`) on push and weekly, plus
  Dependabot for Go modules, GitHub Actions and Docker.
- systemd units for a weekly read-only scan and a daily thumbnail-cache clear,
  both jittered, hardened and disabled by default. Quarantining and database
  cleanup are never scheduled.

[Unreleased]: https://github.com/shuaiZend/mage-mediagc/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/shuaiZend/mage-mediagc/releases/tag/v0.2.1
[0.2.0]: https://github.com/shuaiZend/mage-mediagc/releases/tag/v0.2.0
