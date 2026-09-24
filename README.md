# mage-mediagc

**A standalone, reversible garbage collector for Magento 2 catalog media.**

**English** · [简体中文](README.zh-CN.md)

[![CI](https://github.com/shuaiZend/mage-mediagc/actions/workflows/ci.yml/badge.svg)](https://github.com/shuaiZend/mage-mediagc/actions/workflows/ci.yml)
[![CodeQL](https://github.com/shuaiZend/mage-mediagc/actions/workflows/codeql.yml/badge.svg)](https://github.com/shuaiZend/mage-mediagc/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/shuaiZend/mage-mediagc)](https://github.com/shuaiZend/mage-mediagc/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.21%2B-00ADD8.svg)](https://go.dev)

---

## The problem

Magento 2 has no garbage collector for catalog media.

Delete a product through the admin UI and the catalog cleans up after itself.
Delete products the way real shops actually delete them — a bulk import, a
direct `DELETE`, a failed migration, an extension — and nothing cascades:

- rows survive in `catalog_product_entity_media_gallery`,
  `..._media_gallery_value_to_entity`, `..._media_gallery_value` and the five
  product EAV tables (`_varchar`, `_int`, `_text`, `_decimal`, `_datetime`)
- the image files those rows referenced stay on disk forever
- and so does the derived thumbnail cache, one directory per size variant per
  image

Nothing in `bin/magento` removes any of it. There is no official image GC
command. The growth is silent and permanent.

This is not a theoretical concern. Profiling one long-running production
catalog produced the following — figures rounded, because the exact ones say
more about the shop than about the problem:

| Measurement | Value |
| --- | --- |
| `pub/media/catalog/product` | **~130 GB**, ~1.2 million files |
| Original images still referenced | **~36,000** files, ~10% of the original-image bytes |
| Orphaned originals | **~370,000** files, **~90% of the original-image bytes** |
| Derived thumbnail cache | ~800,000 files, ~27% of the directory's bytes |
| Products in the database | ~5,900 |
| Residue EAV entity ids | ~57,000 |
| Orphaned database rows | ~1.9 million |

Accretion was ongoing, not historical: tens of thousands of new orphan files
per year, every year since 2020.

`mage-mediagc` finds that garbage and removes it — without ever deleting an
original image in a step you cannot undo.

## Why not one of the existing modules

There are several Magento modules that do part of this. Two problems make them
unsafe to point at a live shop.

**They compare against the gallery only.** An image is kept alive by more than
`catalog_product_entity_media_gallery`:

| Reference source | Gallery-only tools | mage-mediagc |
| --- | :---: | :---: |
| `catalog_product_entity_media_gallery` | yes | yes |
| `image` / `small_image` / `thumbnail` / `swatch_image` attributes | no | yes |
| category `image` / `thumbnail` attributes | no | yes |
| images embedded in product descriptions | no | yes |
| images embedded in CMS pages and blocks | no | yes |
| Magento's placeholder images | no | yes |

A category banner or a product photo inside a CMS block is referenced by
nothing in the gallery. A gallery-only tool reports it as an orphan and
deletes it. That is data loss on a live shop.

Placeholders are a subtler case: they are referenced only from
`core_config_data`, which is a configuration table rather than a media table, so
even a tool that reads every media table will not see them. `mage-mediagc`
protects `catalog/product/placeholder/` outright.

**They delete in place.** There is no quarantine, no manifest, no rollback.

## What it does

| Stage | Command | Risk | Reversible |
| --- | --- | --- | --- |
| Report | `scan` | none, read-only | n/a |
| Clear thumbnails | `cache clean --apply` | none, Magento regenerates them | n/a |
| Refill thumbnails | `cache warm --apply` | none, but the load lands on the storefront | n/a |
| Isolate orphans | `quarantine --apply` | files moved, originals intact | `restore --apply` |
| Free the space | `purge --apply` | **permanent** | no |
| Remove stale rows | `db-clean --apply` | **permanent** | restore from backup |

The design rule is simple: **nothing deletes an original image.** Removing an
orphan means *moving* it into a holding directory on the same filesystem,
where `os.Rename` is an inode operation — microseconds per file, no extra disk
space, no copy. Isolating hundreds of thousands of files takes seconds. If the
shop looks wrong afterwards, `restore` puts every file back exactly where it
came from.

## Install

### Release binary

```sh
VERSION=$(curl -sSL https://api.github.com/repos/shuaiZend/mage-mediagc/releases/latest \
  | grep -o '"tag_name": *"v[^"]*"' | cut -d'"' -f4)

curl -sSLO "https://github.com/shuaiZend/mage-mediagc/releases/download/${VERSION}/mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
tar xzf "mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
sudo install -m 0755 mage-mediagc /usr/local/bin/
mage-mediagc version
```

Or pick the archive you want from the
[releases page](https://github.com/shuaiZend/mage-mediagc/releases) —
Linux and macOS, `amd64` and `arm64`, all statically linked with
`CGO_ENABLED=0`. No runtime, no `composer require`, nothing installed into the
shop.

### Packages

`.deb`, `.rpm` and `.apk` packages are attached to each release. They install
the binary to `/usr/local/bin`, a commented config to `/etc/mage-mediagc/`,
and the optional systemd units, which are left **disabled** on purpose:

```sh
sudo apt install ./mage-mediagc_*_linux_amd64.deb
sudo $EDITOR /etc/mage-mediagc/mage-mediagc.env     # set MAGEGC_MAGENTO_ROOT
mage-mediagc scan --config /etc/mage-mediagc/mage-mediagc.yaml
```

### Docker

```sh
docker run --rm \
  --network host \
  -v /data/wwwroot/shop:/magento \
  ghcr.io/shuaiZend/mage-mediagc scan --magento-root /magento
```

`--network host` is needed when MySQL listens on the host rather than in a
container. The image runs as root by default because its job is renaming files
owned by the web server; pass `--user` to run as the shop's own uid instead.

### From source

```sh
git clone https://github.com/shuaiZend/mage-mediagc
cd mage-mediagc
make build          # honors GOHOSTOS/GOHOSTARCH
sudo ./deploy/install.sh --binary ./mage-mediagc
```

## Quick start

Run it from the Magento root. There is nothing to configure — the database
credentials, the media path and the table prefix all come from
`app/etc/env.php`, which is parsed directly, with no PHP runtime required.

```sh
cd /data/wwwroot/shop

# 1. Read-only. Safe on production, safe at any time.
mage-mediagc scan

# 2. Isolate what step 1 reported. Nothing is deleted.
mage-mediagc quarantine --apply

# 3. Verify the shop. Then either free the space...
mage-mediagc purge --apply

# ...or roll the whole thing back.
mage-mediagc restore --apply
```

Example output against a catalog of that size. The shop is illustrative; the
format is exactly what the tool prints.

```
mage-mediagc 0.2.0
──────────────────────────────────────────────────────────────
media root /var/www/magento/pub/media/catalog/product
magento    /var/www/magento
database   magento (8.0.36)

  files on disk    1212481  128.10 GB
  thumbnail cache  806540   34.80 GB
  db references    36482
  still used       36482   9.00 GB
  orphaned         369440  84.30 GB (30.5%)

reclaimable
  cache 34.80 GB + orphans 84.30 GB = 119.10 GB

reference sources
  media_gallery          31500 rows  +31500 paths  catalog_product_entity_media_gallery joined to existing products
  product_image_attr     10240 rows  +1240 paths   image, small_image, thumbnail, swatch_image attributes
  product_content        5990 rows   +3530 paths   images embedded in product descriptions
  category_image         172 rows    +172 paths    category image and thumbnail attributes
  cms_content_cms_block  61 rows     +19 paths     images embedded in cms_block
  cms_content_cms_page   48 rows     +21 paths     images embedded in cms_page

orphan hotspots
  2/1  13200 / 17900  2.83 GB  73.7%
  3/4  11650 / 15760  2.64 GB  73.9%
  h/-  27160 / 72650  6.05 GB  37.4%
  s/-  13100 / 33800  2.83 GB  38.8%
  w/-  10170 / 27700  2.44 GB  36.7%

database orphans
  products       5850
  orphaned rows  1929660 / 2380870
  catalog_product_entity_varchar                        584590 / 584590  100.0%
  catalog_product_entity_int                            567870 / 567870  100.0%
  catalog_product_entity_media_gallery_value_to_entity  479280 / 479280  100.0%
  catalog_product_entity_decimal                        172450 / 174660  98.7%
  catalog_product_entity_text                           104470 / 104470  100.0%

warnings
  ! 30.5% of files look orphaned (abort threshold 98%)
```

`-v` adds the reference-source breakdown shown here; `--top-dirs` controls how
many orphan hotspots are listed.

`--format json` and `--format markdown` produce machine-readable and
ticket-ready versions of the same report. The Markdown version is a
self-contained document with numbered next steps.

## Commands

| Command | Purpose |
| --- | --- |
| `scan` | Index the media tree, collect references, report the difference |
| `list --kind orphan\|live\|missing` | Print individual paths |
| `cache stats` / `cache clean --apply` | Measure or clear the thumbnail cache |
| `cache warm --apply` | Refill the thumbnail cache by requesting the derived URLs |
| `quarantine --apply` | Move orphans into the holding directory |
| `restore --apply` | Put quarantined files back |
| `purge --apply` | Delete the holding directory (refuses one it did not create) |
| `db-clean --apply` | Delete rows pointing at deleted products |
| `verify` | Re-check a media tree against the database after a change |
| `config show` / `config template` | Print the resolved configuration |
| `version` | Print version, commit and build date |

Every mutating command is a dry run unless `--apply` is given. Full flag
reference: [docs/reference.md](docs/reference.md).

## Refilling the thumbnail cache

Magento stores every resized variant under
`catalog/product/cache/<md5(size params)>/…` and generates one the first time
somebody asks for it. Emptying that tree is the safest large win available —
15–30% of media on a mature catalog — and the cost is paid by the first
visitors, who wait for PHP to resize images on the request path.

`cache warm` moves that cost off them. It requests one cache URL per original
image, and Magento generates each variant through exactly the code path a
visitor's browser would take. Nothing is installed on the shop: no PHP runtime,
no `bin/magento`, no Composer, no module. The tool needs HTTP access and nothing
else, so it runs from a laptop, a bastion host or a CI job against a shop it has
no shell on.

It requests one URL per image, not per variant, because since Magento 2.3 a
single request regenerates that image's variant in every size set the theme
defines — twenty-five of them on a stock theme. A 60,000-image catalog is 60,000
requests, not 1.5 million.

```sh
# Empty the cache, recording which size sets exist before they are destroyed
mage-mediagc cache clean --apply --save-hashes var/cache-hashes.txt

# Refill it: a dry run to see the size of the job, then the real thing
mage-mediagc cache warm --hash-file var/cache-hashes.txt
mage-mediagc cache warm --hash-file var/cache-hashes.txt --apply
```

A request has to name a size set in its path, and Magento names those
directories with an md5 of private PHP-side parameters built from the theme's
`view.xml` and from store configuration. The tool **discovers** one rather than
computing it: from the cache tree, from a `--hash-file` snapshot, or — when both
are empty — by asking the shop once and reading the answer off the disk. Only one
is needed, since the request regenerates the rest, but it has to be one the theme
currently asks for, and a stale value is detected and replaced.

Four things make it safe against a live shop. Images that already have a
variant are skipped with a local `stat`, so an interrupted run resumes almost
free and re-running never re-generates anything. One request is issued first, on
its own, with its cache file checked for afterwards: a run where requests return
200 and no file appears stops right there, because that is what a CDN or reverse
proxy answering from the edge looks like. Before giving that verdict the original
is `stat`'d — Magento answers a missing original with the placeholder image and a
200, so without it an incomplete media tree would be misdiagnosed as an edge
cache. And the whole plan is screened the same way: images that are listed but
not on disk are reported as `missing` and never requested, because a request that
cannot produce a file would only book a success. Finally, Magento 2.2 and earlier
are refused outright — they generate nothing on request, so the run would be
wasted — with the check pointing at `bin/magento catalog:images:resize` instead.

It is also the better answer to "just run `php bin/magento
catalog:images:resize`". That command is single-threaded, re-resizes variants
that already exist and cannot be interrupted; `cache warm` skips what exists and
does the rest concurrently, across several workers.

| Flag | Purpose |
| --- | --- |
| `--apply` | Issue the requests. Without it, only the plan is reported. |
| `--base-url` | Storefront URL. Defaults to `web/secure/base_url` from `core_config_data`. |
| `--hash-file` | Read the size set from a snapshot. |
| `--cache-hash` | Route the requests through one size set. |
| `--loopback` | Dial `127.0.0.1` while keeping the Host header, for a run on the shop's own server. |
| `--resolve` | Replace the address dialed for one host and port. |
| `--insecure-skip-verify` | Accept a self-signed certificate, which Go otherwise rejects for having no SAN. |
| `--skip-support-check` | Warm anyway on an install that looks like Magento 2.2. |
| `--concurrency` | Requests in flight (default 4). |
| `--rate` | Cap request starts per second, for a shop that is busy. |
| `--max-requests` | Stop after N requests, to warm a large catalog in slices. |
| `--live-only` | Warm only images that are still referenced (needs the database). |
| `--no-probe` | Skip the pre-flight check, once you have verified the URL mapping by hand. |

Four limits are worth knowing before relying on it. It needs the storefront
reachable **at the origin**, not through a CDN that already holds the images. It
does not warm webp or CMYK variants, because Magento decides whether to produce
those from configuration this tool does not read. `--live-only`, and the default
base-URL lookup, need database access — passing `--base-url` explicitly is what
makes the command usable with no database at all. And after a `cache clean` with
no snapshot, the size sets have to be re-learned from traffic.

Full flag reference, including the pre-flight check in detail:
[docs/reference.md](docs/reference.md).

## Configuration

Five sources, later ones winning:

1. built-in defaults
2. `mage-mediagc.yaml` (from `--config`, or auto-discovered in the working
   directory)
3. `<magento root>/app/etc/env.php` — **fills gaps only, never overwrites**
4. `MAGEGC_*` environment variables
5. command-line flags

The gap-filling rule in step 3 is what makes the tool usable against a replica
or a socket without editing anything: whatever you set explicitly is never
replaced.

Start from [examples/mage-mediagc.yaml](examples/mage-mediagc.yaml) (fully
commented) and [examples/mage-mediagc.env](examples/mage-mediagc.env) (for the
systemd units). Details: [docs/configuration.md](docs/configuration.md).

## Deployment

The binary is static and self-contained, so deployment is a file copy. Two
ways, depending on how much automation you want.

**Manual / ad hoc** — copy the binary and run it. Nothing else is needed.

**Scheduled, safe-only** — the packages ship two systemd timers:

| Timer | Schedule | What it does |
| --- | --- | --- |
| `mage-mediagc-scan.timer` | weekly, jittered | writes a read-only markdown report to `/var/log/mage-mediagc/scan-latest.md` |
| `mage-mediagc-cache.timer` | daily, jittered | clears the derived thumbnail cache |

Both are disabled by default and are read-only or regenerable by construction.
Quarantining and database cleanup are **never** scheduled — those stay manual,
because they are the steps a human should look at the numbers before running.

```sh
sudo systemctl enable --now mage-mediagc-scan.timer mage-mediagc-cache.timer
journalctl -u mage-mediagc-scan.service -n 50
```

Full runbook, including the recommended staged rollout and the exact rollback
procedure: [docs/deployment.md](docs/deployment.md) and
[docs/operations.md](docs/operations.md).

## Documentation

| Document | Contents |
| --- | --- |
| [README.zh-CN.md](README.zh-CN.md) | 简体中文 README — quick start and a worked example of the Chinese report |
| [docs/reference.md](docs/reference.md) | Every command, flag, config key, environment variable and on-disk artefact |
| [docs/configuration.md](docs/configuration.md) | The five configuration sources and how they interact |
| [docs/deployment.md](docs/deployment.md) | Install paths, packages, container, systemd, staged rollout |
| [docs/operations.md](docs/operations.md) | Runbook: what to check, what to run, how to roll back |
| [docs/architecture.md](docs/architecture.md) | How the pieces fit, and why each layer exists |
| [docs/faq.md](docs/faq.md) | Safety questions, surprising results, compatibility |
| [CHANGELOG.md](CHANGELOG.md) | Release history |

## Safety model

- **Read before write.** `scan`, `list`, `cache stats`, `config show` and
  `verify` cannot modify anything.
- **Refilling the cache writes only derived files.** `cache warm` sends HTTP
  requests to the storefront and lets Magento generate variants through its own
  code path. It never touches an original, and everything it creates is what
  `cache clean` would delete again.
- **Dry run by default.** Every destructive command requires `--apply`.
- **Move, never delete.** Orphans are renamed into a quarantine directory, with
  a JSONL manifest recording every path, so `restore` is exact.
- **Same filesystem enforced.** The run refuses to cross devices, because a
  silent copy would double disk usage and take hours. `--allow-cross-device`
  overrides it.
- **Ratio guard.** `quarantine` aborts when the orphan ratio exceeds
  `cleanup.maxDeleteFraction` (default 0.98). A ratio that extreme nearly
  always means reference collection failed, not that the shop really is that
  full of garbage.
- **Conservative matching.** A file counts as live if *any* candidate path
  matches, including a case-insensitive one. A false "live" costs disk space; a
  false "orphan" loses an image.
- **Guard rails on the database.** Every table is validated to exist before a
  single `DELETE` runs, rows are removed in bounded batches inside individual
  transactions, and foreign key checks are disabled only for the duration on a
  dedicated connection.
- **Purge refuses to improvise.** `purge` will not delete a directory that has
  no manifest, unless you pass `--force`.

## Project layout

```
.
├── cmd/mage-mediagc/         entry point
├── internal/
│   ├── phpconfig/            PHP lexer/parser for app/etc/env.php (no PHP needed)
│   ├── config/               five-layer configuration resolution
│   ├── magento/              reference collection, orphan stats, row cleanup
│   ├── media/                parallel media-tree scan
│   ├── analyzer/             live/orphan classification and statistics
│   ├── action/               cache clean, quarantine, restore, purge
│   ├── warm/                 thumbnail cache warm-up over HTTP
│   ├── report/               table / JSON / Markdown rendering
│   └── cli/                  cobra command surface
├── internal/integration/     end-to-end tests against real MySQL
├── deploy/                   systemd units, installer, package hooks
├── examples/                 commented config and environment files
├── docs/                     architecture, configuration, deployment, ops, FAQ
└── .github/workflows/        CI, release, CodeQL
```

Architecture and the reasoning behind each layer:
[docs/architecture.md](docs/architecture.md).

## Delivery standards

This project is built to the conventions expected of a production open-source
tool, and every one of these is enforced in CI:

| Gate | How |
| --- | --- |
| Formatting | `gofmt -s -l` must be empty |
| Static analysis | `go vet` + `golangci-lint` (24 linters, zero findings) |
| Module hygiene | `go mod tidy` must be a no-op |
| Unit tests | `go test -race` on Linux and macOS |
| Go compatibility | 1.21, 1.22, 1.23, 1.24 |
| Integration tests | full pipeline against real MySQL 5.7 **and** 8.0 |
| Cross-compilation | linux/darwin × amd64/arm64 |
| Release artefacts | goreleaser: archives, deb/rpm/apk, checksums, changelog |
| Containers | multi-arch `linux/amd64` + `linux/arm64` image on GHCR |
| Supply chain | CodeQL (security-and-quality), Dependabot, SHA-256 checksums |

The integration suite is the part that matters most: it builds a miniature but
faithful Magento catalog schema, fills it with the exact debris this tool
exists to remove, then runs scan → analyze → dry run → clean → quarantine →
restore → purge and asserts the outcome, including the cascade where removing
a product's last gallery link orphans the gallery entry and then its per-store
values.

## Comparison

| | mage-mediagc | Magento modules doing this |
| --- | --- | --- |
| Language | Go, single static binary | PHP, installed into the shop |
| Install | copy a file | `composer require` + `setup:upgrade` |
| Works while the shop is down | yes | no (needs Magento bootstrap) |
| Reference sources | 5, including CMS and descriptions | gallery only |
| Rollback | quarantine + manifest + `restore` | none |
| Runs at scale | parallel, sharded, binary-search lookups | single process, in-process |
| Machine-readable output | JSON and Markdown | no |
| Deletes database rows | opt-in, batched, validated | varies |

## Requirements

- A Magento 2 installation (tested against 2.2, 2.3 and 2.4 schemas) or any
  `magento_*`-shaped database
- MySQL 5.7+ or MariaDB 10.2+ reachable with read/write access for
  `db-clean`; read-only is enough for everything else. Not every command needs
  it at all: `cache stats`, `cache clean`, `config show` and `cache warm
  --base-url …` never connect, so they run on a host with no route to the
  database
- No PHP on the host, no `bin/magento`, no Composer
- A Unix-like host. Binaries are published for Linux and macOS only: the safety
  guarantees rest on POSIX device numbers and file ownership, and the steps that
  depend on them refuse to run on a platform without them rather than guess.

## Contributing

Issues and pull requests are welcome. Please read
[CONTRIBUTING.md](CONTRIBUTING.md) first — in particular, a destructive-tool
change needs a test that proves the failure mode it prevents.

## License

Apache License 2.0. See [LICENSE](LICENSE).
