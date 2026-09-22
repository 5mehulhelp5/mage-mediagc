# Reference

Complete reference for the command surface, configuration keys, environment
variables and on-disk artefacts.

- [Invocation](#invocation)
- [Global flags](#global-flags)
- [Commands](#commands)
  - [`scan`](#scan)
  - [`list`](#list)
  - [`cache`](#cache)
  - [`quarantine`](#quarantine)
  - [`restore`](#restore)
  - [`purge`](#purge)
  - [`db-clean`](#db-clean)
  - [`verify`](#verify)
  - [`config`](#config)
  - [`version`](#version)
- [Exit codes](#exit-codes)
- [Configuration file](#configuration-file)
- [Environment variables](#environment-variables)
- [Reference sources](#reference-sources)
- [On-disk artefacts](#on-disk-artefacts)
- [Output formats](#output-formats)
- [Compatibility notes](#compatibility-notes)

---

## Invocation

```
mage-mediagc [command] [flags]
```

With no command, the help text is printed. Running from a Magento installation
root requires no configuration at all: the database credentials, media path and
table prefix are read from `app/etc/env.php`.

Mutating commands (`cache clean`, `quarantine`, `restore`, `purge`, `db-clean`)
perform a **dry run** unless `--apply` is given. There is no `--dry-run` flag,
because the dry run is the default.

## Global flags

Every command accepts these.

| Flag | Default | Description |
| --- | --- | --- |
| `--config <path>` | `./mage-mediagc.yaml` | Path to the YAML config file. A missing file at the default path is not an error. |
| `--magento-root <path>` | discovered / from `env.php` | Magento installation root, i.e. the directory containing `app/etc/env.php`. |
| `--media-path <path>` | `<root>/pub/media/catalog/product` | Directory holding original catalog images. |
| `--db-host <host>` | from `env.php` | MySQL host. |
| `--db-port <int>` | from `env.php` | MySQL port. |
| `--db-name <name>` | from `env.php` | Database name. |
| `--db-user <user>` | from `env.php` | Database user. |
| `--db-password <pass>` | from `env.php` | Database password. Prefer the environment variable: a password on the command line is visible in `ps`. |
| `--db-socket <path>` | — | Unix socket path. Overrides host and port. |
| `--workers <int>` | one per CPU, capped at 16 | Parallel workers for scanning and file moves. `0` means auto. |
| `-f`, `--format <table\|json\|markdown>` | `table` | Report encoding. |
| `--language <en\|zh>` | `en` | Report language. Affects `scan`, `verify` and `config show` output only. |
| `--no-content-refs` | off | Skip parsing product descriptions and CMS content for image references. Faster, and **riskier**: images referenced only from rich text will be reported as orphans. |
| `-v`, `--verbose` | `0` | Repeatable. Adds the per-source reference breakdown and per-table database detail. |
| `-q`, `--quiet` | off | Suppress progress output on stderr. Report output is unaffected. |
| `-h`, `--help` | — | Help for the command. |

### Discovery

If `--magento-root` and `--media-path` are both absent, the working directory and
its ancestors are searched for `app/etc/env.php`. If only `--media-path` is
given, the same search starts from `<media-path>/../../..`.

---

## Commands

### `scan`

Read-only. Indexes the media tree, collects references, and reports the
difference. Runs safely on production at any time.

| Flag | Default | Description |
| --- | --- | --- |
| `--db-orphans` | `true` | Include database orphan statistics. Use `--db-orphans=false` to skip the (potentially slow) database sweep. |
| `-o`, `--output <path>` | stdout | Write the report to a file. When writing to a file, ANSI color is disabled automatically. |
| `--top-dirs <int>` | `10` | How many directory buckets to list. Markdown defaults to 20. |

```sh
mage-mediagc scan
mage-mediagc scan --format json --output report.json
mage-mediagc scan --format markdown -vv --output media-report.md
mage-mediagc scan --db-orphans=false --workers 8
```

To skip staging directories left behind by importers, set `scan.excludeGlobs` in
the config file — it has no command-line flag, because it is a property of the
installation rather than of a single run.

Reports, in this order: files on disk, derived thumbnail cache, reference paths
in the database, live files, orphaned files, reclaimable space, the per-source
reference breakdown (verbose), orphan hotspots by directory, database orphan
statistics, and any warnings.

### `list`

Prints bare paths, one per line, with nothing else — suitable for
`rsync --files-from`.

| Flag | Default | Description |
| --- | --- | --- |
| `-k`, `--kind <orphan\|live\|missing>` | `orphan` | Which set to print. |
| `-o`, `--output <path>` | stdout | Write to a file instead of stdout. |
| `--limit <int>` | `0` (all) | Maximum number of paths to print. |

```sh
# Back up every orphan without moving anything
mage-mediagc list --kind orphan --output orphans.txt
rsync -a --files-from=orphans.txt --relative \
  ./pub/media/catalog/product/ /backup/orphans/

# The keep-list, for a migration that only needs live images
mage-mediagc list --kind live --output keep.txt
```

### `cache`

Magento stores every resized variant under
`catalog/product/cache/<md5(parameters)>/<first char>/<second char>/<basename>`.
These files are derived data: no database row names them, and Magento
regenerates each on first request. Removing them is the lowest-risk large win
available.

| Subcommand | Flags | Description |
| --- | --- | --- |
| `cache stats` | — | Report the file count and size of the derived cache. |
| `cache clean` | `--apply` | Empty the cache tree and recreate it with the same ownership, so the web server can keep writing to it. |

`cache clean` only ever targets the cache directory below the resolved media
path, and recreates it immediately, so there is no `--force` because there is no
path to get wrong by hand.

```sh
mage-mediagc cache stats
mage-mediagc cache clean            # report what would be freed
mage-mediagc cache clean --apply
```

Expect a temporary slowdown on the first requests afterwards. Consider warming
the cache with `php bin/magento catalog:images:resize`.

### `quarantine`

Moves every file that no reference keeps alive into a holding directory,
preserving the relative directory structure. Files are **moved, not copied**.

| Flag | Default | Description |
| --- | --- | --- |
| `--apply` | off | Perform the move. Without it, only a report is produced. |
| `--quarantine-dir <path>` | `<root>/var/mage-mediagc/quarantine` | Holding directory. Must be on the same filesystem as the media tree. |
| `--max-fraction <float>` | `cleanup.maxDeleteFraction` (`0.98`) | Abort when the orphan ratio exceeds this value. Range `(0, 1]`. |
| `--allow-cross-device` | from `cleanup.allowCrossDevice` | Permit a move across filesystems. The move becomes a full copy, which doubles disk usage for the duration and takes far longer. |
| `--force` | off | Ignore the orphan-ratio abort threshold. |

Refuses to run when:

- no file looks orphaned (nothing to do);
- the orphan ratio exceeds `--max-fraction`, unless `--force` is given;
- the quarantine directory is on a different device, unless
  `--allow-cross-device` is given.

```sh
mage-mediagc quarantine                       # measure
mage-mediagc quarantine --apply               # move
mage-mediagc quarantine --apply --quarantine-dir /data/media-quarantine
```

### `restore`

Reads the manifest written by `quarantine` and moves every recorded file back.
This is the exact inverse of `quarantine`, and the reason quarantining is safe.

| Flag | Default | Description |
| --- | --- | --- |
| `--apply` | off | Perform the restore. |
| `--quarantine-dir <path>` | as above | Holding directory to restore from. |
| `--only <p1,p2>` | all entries | Restore only these relative paths. Comma separated, repeatable. |

A file that already exists at the destination is **skipped, not overwritten**: a
newer upload at the same path should win. If any entries cannot be restored, the
manifest is rewritten to contain only those, so a second `restore` retries just
what is left. Once everything is restored the manifest is removed.

```sh
mage-mediagc restore
mage-mediagc restore --apply
mage-mediagc restore --apply --only h/-/a.jpg,s/-/b.jpg
```

### `purge`

Deletes the contents of the holding directory. This is the point of no return:
quarantining only reserves space by moving files out of the media tree; the
bytes are not actually freed until this runs.

| Flag | Default | Description |
| --- | --- | --- |
| `--apply` | off | Perform the deletion. |
| `--quarantine-dir <path>` | as above | Holding directory to delete. |
| `--force` | off | Delete even without a `mage-mediagc` manifest. |

Refuses to delete a directory that holds files but no manifest, which is the
signature of pointing it at the wrong path.

```sh
mage-mediagc purge            # report what would be freed
mage-mediagc purge --apply
```

Purging does **not** remove orphaned database rows. Run `db-clean` for that.

### `db-clean`

Removes the EAV and gallery rows that survive when products are deleted outside
the admin UI — bulk imports, direct `DELETE`, failed migrations, extensions.

| Flag | Default | Description |
| --- | --- | --- |
| `--apply` | off | Perform the deletion. Without it, only counts are printed. |
| `--batch-size <int>` | `cleanup.batchSize` (`1000`) | Rows per `DELETE` statement. |

Scope is strictly limited to rows whose referenced product or gallery entry no
longer exists. Rows are deleted in bounded batches, each inside its own
transaction, on a dedicated connection with foreign key checks disabled and
restored afterwards. Every table is validated to exist before a single `DELETE`
runs.

Because deleting a row can orphan rows elsewhere — a product's last gallery link
orphans the gallery entry, which orphans its per-store values — the ordered
table list is swept repeatedly until a sweep changes nothing (at most 5 passes).

Back up first:

```sh
mysqldump --single-transaction <db> > pre-db-clean.sql
mage-mediagc db-clean
mage-mediagc db-clean --apply
mage-mediagc db-clean --apply --batch-size 5000
```

### `verify`

Re-runs the analysis and prints a checklist. The quickest way to confirm a
cleanup did what you expected and broke nothing.

Healthy state after a full cleanup:

| Measure | Expected |
| --- | --- |
| orphaned files | `0` |
| missing files | unchanged — cleanup never touches referenced files |
| derived cache | small, and growing again as visitors browse |
| database orphans | `0` |

### `config`

| Subcommand | Description |
| --- | --- |
| `config show` | Print the fully resolved configuration. Passwords are redacted. |
| `config template` | Print a commented YAML configuration template. |

`config show` is the fastest way to answer "why is it talking to the wrong
database": it prints the effective value of every setting and where the config
file was loaded from.

### `version`

| Flag | Description |
| --- | --- |
| `--json` | Print version, commit and build date as JSON. |

Version, commit and build date are injected at build time via `-ldflags`, so a
binary from a release or from `make build` reports real values; `go run` reports
`dev`.

---

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. A dry run that reports work to do still exits `0`. |
| `1` | Any error: invalid configuration, unreachable database, a safety rail triggered, or a batch of per-file failures. |

Errors are written to stderr prefixed with `error: `. Progress output also goes
to stderr, so stdout carries only the report and can be piped safely.

`SIGINT` and `SIGTERM` cancel the current operation; the process exits non-zero.

---

## Configuration file

YAML. Every key is optional. Later sources override earlier ones.

```yaml
magento:
  root: /data/wwwroot/shop                 # contains app/etc/env.php
  mediaPath: /data/wwwroot/shop/pub/media/catalog/product

database:
  host: 127.0.0.1
  port: 3306
  name: shop_prod
  user: magegc_ro
  password: ""
  socket: ""                               # overrides host/port when set
  charset: utf8mb4

scan:
  workers: 0                               # 0 = one per CPU, capped at 16
  includeContentRefs: true                 # parse product descriptions and CMS content
  includeCache: false                      # count the derived cache as files on disk
  excludeGlobs: []                         # e.g. ["import_*", "*.tmp"]

cleanup:
  quarantineDir: /data/wwwroot/shop/var/mage-mediagc/quarantine
  batchSize: 1000                          # rows per DELETE
  parallel: 0                              # 0 = one per CPU, capped at 16
  allowCrossDevice: false
  maxDeleteFraction: 0.98                  # abort above this orphan ratio, (0, 1]

output:
  format: table                            # table | json | markdown
  language: en                             # en | zh
  verbose: 0
  quiet: false
```

### Precedence

1. built-in defaults
2. the YAML config file (`--config`, or `mage-mediagc.yaml` in the working
   directory)
3. `<magento root>/app/etc/env.php` — **fills gaps only, never overwrites**
4. `MAGEGC_*` environment variables
5. command-line flags

Step 3 is what makes the tool usable against a replica or a socket without
editing anything: `env.php` supplies the database credentials and media path
only where nothing more explicit has been set.

### Validation

The configuration is validated before any work starts, and all problems are
reported at once rather than one per run:

- `magento.root` or `magento.mediaPath` must be set
- `database.name` and `database.user` are required
- `output.format` must be `table`, `json` or `markdown`
- `output.language` must be `en` or `zh`
- `scan.workers`, `cleanup.batchSize` and `cleanup.parallel` must be `>= 1`
- `cleanup.maxDeleteFraction` must be in `(0, 1]`

Write operations additionally require a derivable quarantine directory and a
readable media path.

---

## Environment variables

| Variable | Equivalent to |
| --- | --- |
| `MAGEGC_MAGENTO_ROOT` | `--magento-root` |
| `MAGEGC_MEDIA_PATH` | `--media-path` |
| `MAGEGC_DB_HOST` | `--db-host` |
| `MAGEGC_DB_PORT` | `--db-port` |
| `MAGEGC_DB_NAME` | `--db-name` |
| `MAGEGC_DB_USER` | `--db-user` |
| `MAGEGC_DB_PASSWORD` | `--db-password` |
| `MAGEGC_DB_SOCKET` | `--db-socket` |
| `MAGEGC_WORKERS` | `--workers` |
| `MAGEGC_QUARANTINE_DIR` | `--quarantine-dir` |
| `MAGEGC_FORMAT` | `--format` |
| `MAGEGC_LANGUAGE` | `--language` |

`MAGEGC_DB_PASSWORD` is read even when empty, so setting it to an empty string
explicitly clears a password taken from `env.php`.

Settings with no environment variable (`cleanup.maxDeleteFraction`,
`cleanup.batchSize`, `scan.excludeGlobs`, `scan.includeContentRefs`, …) belong in
the config file.

For the systemd units, these are set in `/etc/mage-mediagc/mage-mediagc.env`,
which uses systemd's `EnvironmentFile` format: plain `KEY=value`, no `export`,
and quotes are taken literally.

### Test hook

`MAGEGC_TEST_DB_HOST` gates the integration test suite. See
[CONTRIBUTING.md](../CONTRIBUTING.md).

---

## Reference sources

A file counts as **live** if any of these holds. Everything else under the scan
root is an orphan.

| Source | What it covers |
| --- | --- |
| `catalog_product_entity_media_gallery` | the product gallery, i.e. everything a gallery-only tool would keep |
| `catalog_product_entity_media_gallery_value` | per-store gallery values, including disabled entries |
| `catalog_product_entity_media_gallery_value_to_entity` | the gallery-to-product links |
| `catalog_product_entity_varchar` | the `image`, `small_image`, `thumbnail`, `swatch_image` and `media_gallery` attributes |
| `catalog_category_entity_varchar` | category `image` and `thumbnail` attributes |
| `catalog_product_entity_text` | images embedded in product descriptions |
| `cms_block`, `cms_page` | images embedded in CMS content |
| `placeholder/` (built in) | Magento's placeholder images, protected without a database lookup — see below |

Attribute lookup goes through `eav_entity_type` rather than a hard-coded
`entity_type_id`, because that id differs between installations.

### The placeholder directory

`catalog/product/placeholder/` is protected unconditionally. Magento references
those files only from `core_config_data` (the `catalog/placeholder/*` paths),
which is a **configuration** table rather than a media table, so reference
collection cannot see them. Without the guard, a stock installation would have
its placeholders reported as orphans and moved out of the media tree.

Protection is by exact directory name: `placeholder-cache/` and `placeholders/`
are ordinary directories and are still eligible for cleanup.

Matching is deliberately conservative: a file is live if *any* candidate path
matches, including a case-insensitive match. A false "live" costs disk space; a
false "orphan" loses an image.

Use `--no-content-refs` (or `scan.includeContentRefs: false`) to skip the
text-parsing sources. That is faster, and it is what makes a shop that embeds
images in CMS blocks unsafe to clean.

---

## On-disk artefacts

`mage-mediagc` writes nothing outside two directories, both of which you choose.

### Quarantine directory

Default `<magento root>/var/mage-mediagc/quarantine`. Contains the moved files
at their original relative paths, plus:

| File | Contents |
| --- | --- |
| `_mage-mediagc-manifest.jsonl` | One JSON object per line: `{"path":"h/-/a.jpg","size":20480,"modTime":"2026-09-23T02:00:00Z"}`. Appended as files move, so an interrupted run still describes exactly what moved. |
| `_mage-mediagc-meta.json` | The parameters of the run that populated the directory: media root, whether it was a dry run, the configured threshold. Its presence is what `purge` requires before deleting. |

Both files are rewritten or removed by `restore`; the manifest shrinks to just
the entries that could not be restored, and disappears entirely once everything
is back.

To roll back after a complete purge there is no artefact — that is what makes
`purge` permanent. Take a backup first if the shop cannot tolerate the risk.

### Reports

Only where you ask for them, via `scan --output`. Nothing is written to
`var/log` or anywhere else by the binary itself.

---

## Output formats

### `table` (default)

Human-readable summary on stdout, progress on stderr. ANSI color is used only
when stdout is a TTY and the report is not going to a file.

### `json`

The full machine-readable payload: tool version and commit, generation
timestamp, the target (media root, Magento root, database, MySQL version), the
analysis, and the database orphan report. The orphan **file list is deliberately
excluded** — a report of a 400,000-file catalog should not be a
400,000-line document. Use `list` for paths.

Stable enough to parse; keys are `camelCase`.

### `markdown`

A self-contained document suitable for attaching to a ticket: header,
summary table, per-source reference breakdown, orphan hotspots, database orphans,
warnings, numbered next steps using real commands, and the first 50 missing
files if any. Localized via `--language`.

In all three formats, logs, errors and the JSON payload stay English.

### Localization

`--language zh` translates the report labels, the summary table, the orphan
hotspots and the numbered next steps.

Strings produced by the analysis itself stay English: warnings, per-source skip
reasons and error text. They are diagnostic, and they are the strings people
paste into bug reports, so translating them would make reports harder to compare
across installations.

One known cosmetic limitation: the `table` format aligns its columns with Go's
`text/tabwriter`, which measures a cell in runes rather than display columns. A
CJK label is therefore credited with half the columns it occupies, and the
`table` output of a Chinese report is visibly misaligned. `markdown` and `json`
are unaffected, and `markdown` is the format intended for a ticket. Correcting
it needs a display-width-aware column writer rather than `tabwriter`.

---

## Compatibility notes

- **Magento 2.2, 2.3 and 2.4 schemas** are all supported. The tool reads the
  schema it finds rather than assuming one; tables that are absent are reported
  as skipped with a reason instead of aborting the run.
- **Table prefixes** are read from `env.php` (`db.table_prefix`) and applied to
  every query.
- **MySQL 5.7+ and MariaDB 10.2+.** Only `db-clean` needs write access; every
  other command works with a read-only user.
- **No PHP, no `bin/magento`, no Composer.** `app/etc/env.php` is parsed by a
  purpose-built parser, so the tool works on a replica, in a container, or while
  the shop is down.
- **No Magento bootstrap.** The tool never includes Magento code, so it cannot
  be broken by a module conflict or a broken `di:compile`.
