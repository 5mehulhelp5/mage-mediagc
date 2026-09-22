# Operations runbook

The order below is not a suggestion. Each stage is reversible or read-only, and
each one produces the evidence needed to justify the next.

```
1. measure  ──►  2. thumbnails  ──►  3. isolate  ──►  4. verify  ──►  5. free space
   scan            cache clean         quarantine        shop looks       purge
                   --apply             --apply           right?

                                          │
                                          └──► rollback: restore --apply

6. database rows  ──►  db-clean --apply  ──►  reindex
   (independent of 1–5; back up first)
```

---

## Stage 1 — Measure

```sh
cd /data/wwwroot/shop
mage-mediagc scan -v --format markdown --output /tmp/media-report.md
```

Read four numbers before doing anything else:

| Number | What it should look like | If it does not |
| --- | --- | --- |
| `reference paths` | tens of thousands on a real shop | near zero → wrong database or table prefix |
| `files on disk` | matches `du -sh` roughly | way off → wrong media path |
| orphan ratio | 20–90% is normal after years of imports | >98% → a reference source failed, check `-v` |
| `missing` | small | thousands → wrong media path, or the analysis is looking at the wrong tree |

`-v` prints the per-source breakdown. Every source must show a plausible row
count. The one that is silently zero is the one that would have caused data
loss.

**Nothing has been modified. This is safe on production at any time.**

---

## Stage 2 — Thumbnails (zero risk)

```sh
mage-mediagc cache clean --apply
```

Deletes `pub/media/catalog/product/cache` and recreates the directory with the
original ownership. Magento regenerates each variant on first request.

Expect a temporary CPU spike on the first day of traffic as variants rebuild.
Nothing can be lost: no database row references these files by name.

This is the one destructive operation worth scheduling automatically. The
package ships a daily timer for it.

---

## Stage 3 — Isolate the orphans

```sh
# Dry run: prints the ratio and refuses if it exceeds cleanup.maxDeleteFraction.
mage-mediagc quarantine

# Move. Originals are intact; they are simply elsewhere.
mage-mediagc quarantine --apply
```

Requirements:

- the quarantine directory must be on the **same filesystem** as the media tree
  (the tool checks and refuses otherwise) — this makes each move an inode
  rename, so 450,000 files take seconds and consume no additional space
- enough inodes, not space: `df -i` rather than `df -h`

The manifest is written to
`<quarantine>/_mage-mediagc-manifest.jsonl` and records every path as it moves.
Copy it somewhere safe before the next stage.

| Symptom | Cause |
| --- | --- |
| `refusing to quarantine: N% of files look orphaned` | the ratio guard fired — check the per-source counts from stage 1, then `--force` if you are satisfied |
| `different filesystem` | point `--quarantine-dir` at a directory on the media volume |
| `failed: N` | usually files that changed between scan and move; the manifest lists exactly which |

---

## Stage 4 — Verify the shop

Before freeing space, confirm nothing visible broke:

```sh
# 1. Re-run the analysis: every remaining reference must resolve to a file.
mage-mediagc verify

# 2. Open a sample of product pages, category pages and CMS pages.
#    Check: product images, category banners, CMS block imagery, swatches.

# 3. Check the web server and PHP logs for new 404s on /media/.
tail -n 200 /var/log/nginx/error.log | grep -c '/media/.*404'

# 4. Magento's own view.
php bin/magento cache:flush
```

`verify` exits non-zero if any referenced file is missing, which is the signal
that something was isolated that should not have been.

Give it a real traffic cycle — a day, ideally — before stage 5. Browsing the
front end yourself does not exercise every theme, store view or extension.

---

## Stage 5 — Free the space

```sh
mage-mediagc purge                      # report what would be freed
mage-mediagc purge --apply
```

Deletes the quarantine directory and everything in it. **This is the point of
no return.** Before running it:

- the manifest has been archived somewhere off-host
- the shop has survived a full traffic cycle
- you accept that rebuilding the images from source is the only recovery path

`purge` refuses to touch a directory that has no mage-mediagc manifest, so it
cannot be pointed at the wrong path by accident. `--force` overrides that.

### Rollback (any time before purge)

```sh
mage-mediagc restore --apply
```

Every file returns to its original relative path. Existing files are never
overwritten: an entry whose destination already exists is skipped, counted, and
left in the manifest so it can be retried after the conflict is resolved.

---

## Stage 6 — Database rows

Independent of stages 2–5. Typically run afterwards, but it can be run at any
time.

```sh
# 1. Always. This is the only rollback for db-clean.
mysqldump --single-transaction --routines --triggers shop_prod > /backup/pre-db-clean-$(date +%F).sql

# 2. Count only. Note the per-table numbers.
mage-mediagc db-clean

# 3. Delete.
mage-mediagc db-clean --apply --batch-size 1000

# 4. Magento must rebuild what it derives from these tables.
php bin/magento indexer:reindex
php bin/magento cache:flush
```

### What it removes

Rows whose referenced product, or gallery entry, no longer exists:

`catalog_product_entity_media_gallery_value`,
`..._media_gallery_value_video`, `..._media_gallery`,
`..._media_gallery_value_to_entity`, `catalog_product_entity_{int,varchar,text,
decimal,datetime}`, `catalog_product_entity_gallery`,
`cataloginventory_stock_item`, `catalog_category_product`,
`catalog_product_website`, `catalog_product_link`, `catalog_product_super_link`.

### Why it sweeps more than once

Removing a row orphans rows elsewhere: dropping a deleted product's last gallery
link orphans the gallery entry, which orphans its per-store values. A single
pass cannot see the second-order effect. The tool sweeps the ordered list until
a sweep changes nothing, which is why the reported total is higher than the dry
run predicts — the dry run can only count what is already dangling.

### Safety properties

- Every table and its reference table are checked to exist **before** any
  `DELETE` runs, so a partial schema cannot become mass deletion.
- Rows are deleted in bounded batches, each in its own transaction.
- `FOREIGN_KEY_CHECKS=0` is scoped to one dedicated connection and restored
  afterwards.
- A dry run is the default. There is no way to delete without `--apply`.

### Sizing a run

| Orphan rows | `--batch-size` | Notes |
| --- | --- | --- |
| < 100k | 1000 | default |
| 100k – 1M | 1000–5000 | watch replica lag |
| > 1M | 500–1000 | run off-peak; expect tens of minutes |
| busy shop, lagging replica | 200 | longer, but gentle |

On the catalog in the README (~2.33M orphan rows) the run took minutes at
`--batch-size 1000`. Table-rewrite time, not row count, dominates on a badly
fragmented InnoDB tablespace; check `SHOW TABLE STATUS` afterwards and
`OPTIMIZE TABLE` during a maintenance window if the tables did not shrink on
disk.

---

## Monitoring

### Weekly, from the report

```sh
# Orphan ratio trend — the number that should fall after a cleanup
grep -i 'orphaned' /var/log/mage-mediagc/scan-latest.md
```

Two things to watch:

- **the ratio climbing steadily** — imports or a broken extension are deleting
  products without cleanup again; the trend tells you the rate
- **`missing` climbing** — references pointing at files that no longer exist,
  usually an incomplete restore or a partially copied migration

### Disk

```sh
du -sh /data/wwwroot/shop/pub/media/catalog/product
du -sh /data/wwwroot/shop/pub/media/catalog/product/cache
df -i /data          # inodes, not bytes
```

Inode exhaustion is the failure mode that takes a shop down. The cache
directory is the usual culprit: one directory per size variant per image.

### A scheduled scan that never runs

```sh
systemctl status mage-mediagc-scan.timer
systemctl list-timers --all | grep mage-mediagc
journalctl -u mage-mediagc-scan.service -n 50 --no-pager
```

`Persistent=true` means a missed run (host down) fires on next boot rather than
being skipped.

---

## Incident playbooks

### "Product images disappeared after a cleanup"

```sh
# 1. Stop generating traffic that makes Magento cache the 404s
php bin/magento cache:disable full_page

# 2. If the quarantine directory still exists, put everything back
mage-mediagc restore --apply

# 3. Confirm
mage-mediagc verify
php bin/magento cache:flush

# 4. Only if purge already ran: recover from backup or the original
#    supplier assets, then re-import through Magento.
```

This is why `quarantine` and `purge` are separate commands, and why the README
recommends waiting a traffic cycle between them.

### "db-clean deleted something it should not have"

```sh
# Restore the dump taken before the run. Nothing else is reliable: Magento's
# own tables cannot be reconstructed from the media files.
mysql shop_prod < /backup/pre-db-clean-YYYY-MM-DD.sql
php bin/magento indexer:reindex
php bin/magento cache:flush
```

Then investigate before retrying: `mage-mediagc db-clean` prints what it would
remove per table, and an unexpected number there is the signal.

### "The scan says 99% of files are orphans"

Almost always a reference-collection failure, not real garbage.

```sh
mage-mediagc scan -v --format json | jq '.analysis.refStats'

# Wrong table prefix?
mage-mediagc config show | grep -i prefix

# Is the database the live one?
mysql -e "SELECT value FROM shop_prod.core_config_data WHERE path='web/unsecure/base_url'"

# Are the stored paths absolute URLs, or missing the catalog/product prefix?
mysql shop_prod -e "SELECT value FROM catalog_product_entity_media_gallery LIMIT 20"
```

Do not pass `--force` to work around this. Fix the reference source.

### "Quarantine ran out of space"

It should not — a move within one filesystem consumes no space. If it did,
the quarantine directory was on another filesystem and
`--allow-cross-device` was passed, turning moves into copies. Remove the
partially written copies and start over with the quarantine directory on the
media volume:

```sh
mage-mediagc purge --apply --force   # only if the manifest is partial and unusable
df -h /data
```

---

## Capacity planning

Rough figures from the catalog profiled in the README, for a single 4-core
server with SSD storage. They are orders of magnitude, not benchmarks:

| Operation | Volume | Time |
| --- | --- | --- |
| `scan` | ~1.2M files, ~130 GB | ~2–4 minutes |
| `cache clean` | ~800k files, ~35 GB | ~1–2 minutes |
| `quarantine` | ~370k files, ~84 GB | ~1–3 minutes |
| `restore` | ~370k files | ~1–3 minutes |
| `db-clean` | ~1.9M rows | ~5–15 minutes |

`scan` is IO-bound and parallelizes across top-level directories; set
`scan.workers` to the spindle count on mechanical storage and to the core count
on SSD. The file operations are metadata-only and are dominated by directory
lookups, so high `cleanup.parallel` values help.
