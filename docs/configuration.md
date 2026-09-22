# Configuration

## Resolution order

Later sources win, with one deliberate exception noted below.

| # | Source | Notes |
| --- | --- | --- |
| 1 | Built-in defaults | assume a typical Magento layout on localhost |
| 2 | YAML config file | `--config PATH`, else `mage-mediagc.yaml`, `mage-mediagc.yml`, `.mage-mediagc.yaml` in the working directory |
| 3 | `<magento root>/app/etc/env.php` | **fills gaps only — never overwrites** |
| 4 | `MAGEGC_*` environment variables | |
| 5 | Command-line flags | |

### The gap-filling rule

`env.php` is the only source that does not overwrite. It only supplies a value
when nothing has set one yet:

```
cfg.DB.Name == ""      → take dbname from env.php
cfg.DB.User == ""      → take username from env.php
cfg.DB.Host == default → take host from env.php
```

That is what makes both of these work without editing anything:

```sh
# Zero configuration: everything comes from env.php
cd /data/wwwroot/shop && mage-mediagc scan

# Override just the host, keep the credentials from env.php
mage-mediagc scan --db-host 10.0.0.9
```

### Finding the Magento root

In order of precedence:

1. `--magento-root`
2. `magento.root` in the config file
3. `MAGEGC_MAGENTO_ROOT`
4. walking up from the working directory looking for `app/etc/env.php`
5. walking up from `--media-path/../..` when only a media path is known

If the root is found, the media path defaults to
`<root>/pub/media/catalog/product` and the quarantine directory to
`<root>/var/mage-mediagc/quarantine`.

## Environment variables

| Variable | Equivalent |
| --- | --- |
| `MAGEGC_MAGENTO_ROOT` | `--magento-root`, `magento.root` |
| `MAGEGC_MEDIA_PATH` | `--media-path`, `magento.mediaPath` |
| `MAGEGC_DB_HOST` | `--db-host`, `database.host` |
| `MAGEGC_DB_PORT` | `--db-port`, `database.port` |
| `MAGEGC_DB_NAME` | `--db-name`, `database.name` |
| `MAGEGC_DB_USER` | `--db-user`, `database.user` |
| `MAGEGC_DB_PASSWORD` | `--db-password`, `database.password` |
| `MAGEGC_DB_SOCKET` | `--db-socket`, `database.socket` |
| `MAGEGC_WORKERS` | `--workers`, `scan.workers` |
| `MAGEGC_QUARANTINE_DIR` | `--quarantine-dir`, `cleanup.quarantineDir` |
| `MAGEGC_FORMAT` | `--format`, `output.format` |

`MAGEGC_DB_PASSWORD` is read with `LookupEnv`, so an explicitly empty value is
respected rather than treated as unset.

## YAML reference

A fully commented example lives at
[`examples/mage-mediagc.yaml`](../examples/mage-mediagc.yaml).

```yaml
magento:
  root: /data/wwwroot/shop          # holds app/etc/env.php
  mediaPath: ""                     # default <root>/pub/media/catalog/product

database:
  host: ""                          # all read from env.php when empty
  port: 0
  name: ""
  user: ""
  password: ""
  socket: ""
  charset: utf8mb4

scan:
  workers: 0                        # 0 = one per CPU, capped at 16
  includeContentRefs: true          # parse descriptions and CMS content
  includeCache: false               # index the cache as ordinary files
  excludeGlobs: []                  # skip matching paths or base names

cleanup:
  quarantineDir: ""                 # default <root>/var/mage-mediagc/quarantine
  batchSize: 1000                   # rows per DELETE statement
  parallel: 0                       # parallel file moves
  allowCrossDevice: false           # permit a cross-filesystem move (a copy)
  maxDeleteFraction: 0.98           # abort above this orphan ratio

output:
  format: table                     # table | json | markdown
  verbose: 0
  quiet: false
```

### Fields worth understanding

**`scan.includeContentRefs`** (default `true`) — parses product descriptions
and CMS page/block content for embedded image references. Turning it off makes
the scan noticeably faster on a large catalog, and makes it wrong: any image
appearing only in page copy becomes an "orphan". Leave it on.

**`scan.excludeGlobs`** — matched against each entry's path relative to
`mediaPath` *and* its base name. A matching directory is skipped whole. An
invalid pattern never matches, so a typo cannot silently exclude the tree.

```yaml
scan:
  excludeGlobs:
    - staging                       # any entry named "staging"
    - "*.tmp"
    - "catalog/product/tmp"         # this exact relative path
```

**`cleanup.quarantineDir`** — must be on the same filesystem as the media tree.
A move within one filesystem is an inode rename: instant, and free. Across
filesystems it is a copy, which temporarily doubles disk usage and turns a
seconds-long run into an hours-long one. The tool refuses unless
`--allow-cross-device` is passed.

**`cleanup.maxDeleteFraction`** (default `0.98`) — `quarantine` aborts when the
orphan ratio exceeds it. The guard exists because a ratio above ~98% nearly
always means the database connection or a reference source failed, not that the
shop really lost 98% of its images. Real catalogs do exceed 88%
(see the case study in the README), so the default leaves room for a genuinely
rotten shop while still catching a broken run.

**`cleanup.batchSize`** — rows per `DELETE`. Smaller batches keep row locks
short and replication lag predictable. 1000 is a reasonable default; drop to
200 on a busy shop with a lagging replica.

**`output.verbose`** — `0` summary, `1` per-reference-source detail (use this
when the orphan ratio looks wrong), `2` includes the orphan and missing file
lists in the report.

## Common scenarios

### Local MySQL via socket

```yaml
database:
  socket: /var/run/mysqld/mysqld.sock
```

### Read-only user

Everything except `db-clean` works with read-only access:

```sql
CREATE USER 'magegc_ro'@'localhost' IDENTIFIED BY '...';
GRANT SELECT ON shop_prod.* TO 'magegc_ro'@'localhost';
```

```sh
mage-mediagc scan --db-user magegc_ro --db-password '...'
```

### Replica, with the analysis following the primary's media

```sh
mage-mediagc scan --db-host 10.0.0.9 --db-port 3306 --magento-root /data/wwwroot/shop
```

### Quarantine on a separate volume

```sh
mage-mediagc quarantine --apply --quarantine-dir /data/quarantine
```

Fails fast with a clear message if `/data` and the media tree are not on the
same filesystem.

### Several shops on one host

```sh
mage-mediagc scan --config /etc/mage-mediagc/shop-a.yaml
mage-mediagc scan --config /etc/mage-mediagc/shop-b.yaml
```

Give each shop its own quarantine directory, or at least its own `--config`,
so manifests never mix.

### Tuning a large catalog

On a 1.5-million-file tree:

```yaml
scan:
  workers: 8                        # IO-bound; more is not always better
cleanup:
  parallel: 16                      # renames are cheap and parallelize well
```

If the tree is on spinning disks rather than SSD, lower `scan.workers` — random
read-ahead on many parallel walkers makes mechanical disks slower, not faster.

## Inspecting the resolved configuration

`config show` prints exactly what the tool will use, with the password
redacted, so it is safe to paste into a ticket:

```sh
mage-mediagc config show --config /etc/mage-mediagc/mage-mediagc.yaml
mage-mediagc config show --format json | jq .
```

`config template` prints a commented starting point.
