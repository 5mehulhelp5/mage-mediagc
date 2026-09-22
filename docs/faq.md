# FAQ

## Safety

### Is it safe to run on production?

`scan`, `list`, `cache stats`, `config show` and `verify` are read-only and safe
at any time, including during peak traffic.

`cache clean` is safe in practice — the files are derived data that Magento
regenerates on first request — but expect a slowdown until the cache warms.

`quarantine`, `restore` and `purge` touch original images. `quarantine` only
moves files and is fully reversible; `purge` is permanent. Neither runs without
`--apply`.

`db-clean` deletes rows and cannot be undone by this tool. Back up first.

### How do I know the tool did not get the classification wrong?

The design assumes it might. Three things mitigate that:

1. **`scan` reports before anything moves.** Check the numbers, and use
   `list --kind orphan` to inspect actual paths.
2. **`quarantine` moves rather than deletes**, recording every move in a
   manifest. `restore` puts everything back at the original paths.
3. **The ratio guard.** If the orphan ratio exceeds `cleanup.maxDeleteFraction`
   (default 0.98), the run aborts: a ratio that extreme nearly always means
   reference collection failed, not that the shop is 98% garbage.

The intended workflow leaves a safety window:

```sh
mage-mediagc scan                  # look at the numbers
mage-mediagc quarantine --apply    # isolate
# ... run for a full business cycle and look at the shop ...
mage-mediagc purge --apply         # only once you are confident
```

### What if a live image does get quarantined?

```sh
mage-mediagc restore --apply
```

Everything comes back. If the same path now holds a newer upload — which can
happen if someone re-uploaded the image in the meantime — that file is skipped
rather than overwritten, and the entry stays in the manifest so you can recover
the quarantined copy by hand from `<quarantine>/<path>`.

### Can I see what would be moved without moving it?

Yes — that is the default:

```sh
mage-mediagc quarantine
```

There is no `--dry-run` flag because the dry run is what happens when `--apply`
is absent. This is deliberate: the dangerous flag should be the one you have to
type.

## Classification

### It reports far more orphans than I expected. Is it broken?

Check the verbose reference sources first:

```sh
mage-mediagc scan -v
```

Common genuine causes:

- **Products were deleted outside the admin UI.** Bulk imports, direct `DELETE`,
  failed migrations and extensions leave gallery and EAV rows behind with no
  cascade. This is the primary reason the tool exists.
- **Images were replaced.** Magento does not delete the old file when you upload
  a new one.
- **Staging leftovers.** Importers often leave directories such as
  `catalog/product/import_*` that no database row ever references.

A genuine cause that is *not* a bug: if a shop has been running for years without
cleanup, 80–90% orphans is realistic. One measured production catalog was at
88.3%.

### Why does it say a file is live when I know the product is gone?

Probably conservative matching. A file counts as live if *any* candidate path
matches, including a case-insensitive match, because a false "live" costs disk
space while a false "orphan" loses an image. Use `-v` to see which source claimed
the path.

Another possibility: the path is referenced from content you asked it to skip.
If you used `--no-content-refs`, images referenced only from CMS blocks or
product descriptions will be treated as orphans. Do not use that flag on a shop
that embeds images in rich text.

### Why were my CMS block images nearly deleted?

They were not deleted, if you followed the workflow — `quarantine` moves, and
`restore` puts them back. But this is the exact failure mode of gallery-only
cleaners, which is why this tool also reads `cms_block` and `cms_page`.

If you used `--no-content-refs`, rerun without it.

### Will it remove the placeholder image?

No. `catalog/product/placeholder/` is protected unconditionally.

That protection is not incidental. Magento references placeholder images only
from `core_config_data` (the `catalog/placeholder/*` paths) — a *configuration*
table, not a media table — so reference collection cannot see them. Without an
explicit guard, a stock installation would have its placeholders reported as
orphans and quarantined. Protection is by exact directory name, so
`placeholder-cache/` and `placeholders/` are still treated as ordinary
directories.

Note also that the scan root is `pub/media/catalog/product`. Anything outside it
— including `pub/media/wysiwyg/` — is not scanned at all and cannot be touched.

## Operations

### How long does a scan take on a huge catalog?

On a catalog with a million-plus files and over 100 GB of media, a full scan is
minutes, not hours: the tree walk is parallel and sharded, and path lookups use
binary search over a sorted reference set rather than a per-file query.

The database orphan sweep (`--db-orphans`, on by default) is a separate cost. On
a table with millions of rows, use `--db-orphans=false` for a fast media-only
scan.

### Why is `quarantine` so much faster than `rm` would be?

Because it does not free space, it just moves it. On the same filesystem,
`os.Rename` is an inode operation — microseconds per file, no data copied. That
is why isolating hundreds of thousands of files takes seconds. The space is only
actually returned when you `purge`.

Corollary: **quarantining does not solve a disk-full emergency.** If the
filesystem is full, you need `purge`, and therefore you need to be confident
first.

### Can I put the quarantine directory on another disk?

Only with `--allow-cross-device`, and think twice. The move becomes a full copy:
disk usage temporarily doubles and it takes far longer. The default refusal
exists because a half-finished copy on a second volume is a much worse position
than an unchanged media tree.

### Do I need to stop the shop or put it in maintenance mode?

No. `scan` is read-only. `cache clean` is safe by design. `quarantine` changes
only files that no database row references, so the storefront cannot request
them.

The one caveat: if a product is created *during* the run, a file uploaded
mid-scan could be misclassified. Run during a quiet period, or re-run `verify`
afterwards.

### Can I run several instances against the same shop at once?

Do not run two mutating operations simultaneously. Concurrent scans are fine, but
two `quarantine` runs would race on the manifest, and `quarantine` racing
`purge` could delete files another run just moved.

### It refused to start with "invalid configuration". Why?

The configuration is validated up front, and all problems are reported at once.
The usual causes:

- not run from the Magento root, and no `--magento-root` or `--media-path`
  given, so neither could be discovered;
- no database name or user, because `app/etc/env.php` was not found;
- a typo in `output.format` (`markdown`, not `md`) or `output.language`;
- `cleanup.maxDeleteFraction` outside `(0, 1]`.

`mage-mediagc config show` prints what was actually resolved and where the config
file came from, which usually answers this immediately.

### Why does `config show` say the database is wrong?

Because of the precedence order. `app/etc/env.php` only *fills gaps* — it never
overrides something set more explicitly. Check, in order of increasing
precedence:

1. built-in defaults
2. the YAML config file
3. `app/etc/env.php`
4. `MAGEGC_*` environment variables (note: a leftover `MAGEGC_DB_HOST` in your
   shell is a common culprit)
5. command-line flags

## Database

### Do I need write access to the database?

Only for `db-clean`. Everything else works with a read-only user, and that is
the recommended setup.

### Is `db-clean` safe?

It deletes only rows whose referenced product or gallery entry no longer exists.
Before any `DELETE` it validates that every target table exists, deletes in
bounded batches each inside its own transaction, on a dedicated connection with
foreign key checks disabled and restored afterwards.

That said, it is a permanent change to your database. Back up first:

```sh
mysqldump --single-transaction <db> > pre-db-clean.sql
```

Then review the counts (`mage-mediagc db-clean` without `--apply`) before
applying.

### Why does `db-clean` run several passes?

Because deleting a row can orphan rows elsewhere. Removing a product's last
gallery link orphans the gallery entry; removing the gallery entry orphans its
per-store values. So the ordered table list is swept repeatedly until a sweep
changes nothing, up to a bounded number of passes.

### Will `db-clean` break my shop?

It only removes rows that already point at nothing, so the shop was not using
them. Run `verify` afterwards, followed by:

```sh
php bin/magento indexer:reindex
php bin/magento cache:flush
```

### Can I clean files and rows in one command?

No, deliberately. The file and database stages are independent so you can verify
one before doing the other. Doing both at once would leave you with no way to
tell which step caused a problem.

## Compatibility

### Does it work without PHP?

Yes. `app/etc/env.php` is parsed by a purpose-built PHP lexer, so there is no PHP
runtime, no `bin/magento` and no Composer anywhere in the picture. That means it
works on a replica, inside a container, or while the shop is down.

### Which Magento versions?

2.2, 2.3 and 2.4 schemas. The tool reads the schema it finds: tables that are
absent are reported as skipped with a reason rather than aborting the run. Table
prefixes from `env.php` are applied to every query.

### Does it work on Magento 1?

No. Magento 1 stores catalog images in a completely different structure with no
gallery tables.

### Can I use it on a database replica?

Yes, and it is a good idea — `scan` is read-only. Point `--db-host` at the
replica. Only `db-clean` would need to run against the primary.

### What about MariaDB?

Supported (10.2+). The queries avoid version-specific syntax.

## Reporting

### Can I get machine-readable output?

```sh
mage-mediagc scan --format json --output report.json
mage-mediagc scan --format markdown --output media-report.md
mage-mediagc list --kind orphan --output orphans.txt
```

The JSON payload deliberately excludes the orphan file list — a report of a
448,000-file catalog should not be a 448,000-line document. Use `list` for paths;
its `rsync --files-from`-style output is designed for exactly that.

### Can the reports be in Chinese?

Yes:

```sh
mage-mediagc scan --language zh
```

Reports are the only localized surface; logs, errors and the JSON payload stay
English so an issue report or an automation script reads the same regardless of
who ran the tool.

### Progress lines are polluting my pipeline

They go to stderr, so `mage-mediagc scan --format json > report.json` is already
clean. To silence them entirely, add `-q`.
