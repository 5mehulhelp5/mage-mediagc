# Deployment

mage-mediagc is a static binary with no runtime dependencies. Deployment is
copying a file. Everything below is about making that repeatable and observable.

## What gets deployed

| Artefact | Where | Purpose |
| --- | --- | --- |
| `mage-mediagc` | `/usr/local/bin` | the tool |
| `mage-mediagc.yaml` | `/etc/mage-mediagc/` | configuration |
| `mage-mediagc.env` | `/etc/mage-mediagc/` | environment for the systemd units |
| `mage-mediagc-scan.{service,timer}` | `/etc/systemd/system/` | weekly read-only report |
| `mage-mediagc-cache.{service,timer}` | `/etc/systemd/system/` | daily thumbnail cache purge |
| quarantine directory | `<magento root>/var/mage-mediagc/quarantine` | isolated files |
| reports | `/var/log/mage-mediagc/` | scan output |

Nothing is written into the Magento installation itself, and nothing is added
to `composer.json`. Removing every file above leaves the shop exactly as it was.

## Method 1 — package (recommended)

```sh
sudo apt install ./mage-mediagc_0.1.0_linux_amd64.deb      # or dnf/yum, or apk
sudo $EDITOR /etc/mage-mediagc/mage-mediagc.env
sudo -u www-data mage-mediagc scan --config /etc/mage-mediagc/mage-mediagc.yaml
```

The package installs but does not enable the timers. That is intentional: a
scan pointed at the wrong database is worse than no scan, so the last step is
always a human confirming the target.

```sh
sudo systemctl enable --now mage-mediagc-scan.timer
sudo systemctl enable --now mage-mediagc-cache.timer
```

## Method 2 — binary and the installer script

```sh
# from a release archive
tar xzf mage-mediagc_0.1.0_linux_amd64.tar.gz
sudo ./deploy/install.sh --binary ./mage-mediagc

# or from a checkout, building on the server
sudo ./deploy/install.sh --source
```

Useful options: `--prefix /opt/mage-mediagc`, `--bindir`, `--no-systemd`.
The script never overwrites an existing `/etc/mage-mediagc/*` config.

## Method 3 — copy the binary, nothing else

For a one-off cleanup this is entirely sufficient:

```sh
scp mage-mediagc root@shop:/usr/local/bin/
ssh root@shop 'cd /data/wwwroot/shop && mage-mediagc scan'
```

No config file, no units, no state. The quirks of a long-lived installation —
a stale config, a half-finished upgrade, a unit pointing at a shop that has
moved — are not worth taking on for a task you run twice a year.

## Method 4 — container

```sh
docker run --rm \
  --network host \
  -v /data/wwwroot/shop:/magento \
  -v /data/quarantine:/quarantine \
  ghcr.io/shuaiZend/mage-mediagc \
  scan --magento-root /magento --format markdown
```

Notes:

- `--network host` is required when MySQL listens on the host. In a compose
  stack, put the container on the same network and use `--db-host mysql`.
- Mount the quarantine directory from the same volume as the media tree, or the
  same-filesystem check will refuse the move.
- The image runs as root by default, because its job is renaming files owned by
  the web server. Add `--user "$(id -u www-data):$(id -g www-data)"` to run as
  the shop's own account instead; `app/etc/env.php` must then be readable by
  that user.

## systemd units

### `mage-mediagc-scan.timer` — weekly report

Runs `scan --format markdown` and writes
`/var/log/mage-mediagc/scan-latest.md`. Read-only: the unit has no write access
to the media tree, and the report is the input to a human decision.

```sh
systemctl list-timers mage-mediagc-*
journalctl -u mage-mediagc-scan.service -n 100
cat /var/log/mage-mediagc/scan-latest.md
```

### `mage-mediagc-cache.timer` — daily cache purge

Runs `cache clean --apply`. This is the only destructive operation that is
scheduled, and only because it cannot lose data: the thumbnail cache is derived
data, Magento regenerates each variant on first request, and nothing references
those files by name. The cost of an unnecessary run is a little CPU.

The unit deliberately passes no `--save-hashes`. A shop busy enough to warrant a
daily timer has enough traffic to relearn its size sets immediately, so there is
nothing to rebuild ahead of time. On a quiet shop, or when you want a clean to
be followed by a warm rather than by the first visitor, run the pair by hand:

```sh
mage-mediagc cache clean --apply --save-hashes var/cache-hashes.txt
mage-mediagc cache warm  --hash-file var/cache-hashes.txt --apply
```

### Deliberately not scheduled

`quarantine`, `purge` and `db-clean` have no units. They are permanent or
high-consequence, and the whole point of the report is that somebody reads it
first. Automating them is possible — write your own unit and gate it on the
report — but it is not something this project will do for you.

`cache warm` has no unit either, for a different reason: it is harmless but
expensive, and its cost is paid by the storefront rather than by this machine.
Any unit for it should be gated on `warm.rate` and run outside trading hours,
which is a decision only the shop's operators can make.

### Site-specific settings

The units read `/etc/mage-mediagc/mage-mediagc.env`. Set at minimum:

```
MAGEGC_MAGENTO_ROOT=/data/wwwroot/shop
```

To run as the web server user, uncomment `User=`/`Group=` in the unit *and*
make sure `/var/lib/mage-mediagc`, `/var/log/mage-mediagc` and the quarantine
directory are writable by it:

```sh
sudo chown -R www-data:www-data /var/lib/mage-mediagc /var/log/mage-mediagc
```

Note that `ProtectSystem=full` in the units makes `/usr`, `/boot`, `/efi` and
`/etc` read-only. Media trees under `/data`, `/var/www` or `/srv` are
unaffected; a shop under `/etc` is not supported.

## Verifying a deployment

```sh
# 1. The binary runs and reports a version.
mage-mediagc version

# 2. It can see the shop and reads credentials from env.php.
mage-mediagc config show

# 3. It can reach MySQL and both media/DB sides agree.
mage-mediagc scan --db-orphans -v

# 4. The full pipeline works end to end, moving nothing.
mage-mediagc quarantine
```

Step 2 is the one that catches most problems: if `database.name` is empty, the
Magento root was not found. Step 3 catches a wrong media path, which shows up as
a `Missing` count in the thousands.

### A useful sanity check

`scan` reports both "files on disk" and "reference paths collected". On a
healthy shop the ratio of live files to reference paths is close to 1, and a
ratio far below 1 is the first sign that a reference source failed. Use
`-v` to see the per-source counts:

```
reference sources
  media_gallery          31500 rows  +31500 paths  catalog_product_entity_media_gallery joined to existing products
  product_image_attr     10240 rows  +1240 paths   image, small_image, thumbnail, swatch_image attributes
  product_content        5990 rows   +3530 paths   images embedded in product descriptions
  category_image         172 rows    +172 paths    category image and thumbnail attributes
  cms_content_cms_block  61 rows     +19 paths     images embedded in cms_block
  cms_content_cms_page   48 rows     +21 paths     images embedded in cms_page
```

## Upgrading

The binary is stateless. Replace it and you are done:

```sh
sudo apt install ./mage-mediagc_0.2.0_linux_amd64.deb
mage-mediagc version
```

Configuration is forward compatible: unknown keys in a newer file are ignored
by an older binary, and new options always have defaults, so a config written
for 0.1.0 keeps working. Read [CHANGELOG.md](../CHANGELOG.md) before a minor
bump — the release notes call out any change to a default.

## Uninstalling

```sh
sudo systemctl disable --now mage-mediagc-scan.timer mage-mediagc-cache.timer
sudo apt remove mage-mediagc
sudo rm -rf /etc/mage-mediagc /var/lib/mage-mediagc /var/log/mage-mediagc
```

Before removing, check whether a quarantine directory is still holding files:

```sh
ls -la /data/wwwroot/shop/var/mage-mediagc/quarantine
```

If it is non-empty, either `restore --apply` it or `purge --apply` it. Deleting
the directory by hand works too, but you lose the manifest.

## Hardening notes

- Grant the database user `SELECT` only, unless you intend to run `db-clean`.
- `db-clean` needs `DELETE`. Give it a dedicated user if your policy separates
  duties.
- The tool never executes PHP and never writes into the shop, so it cannot
  introduce a code-execution path into the application.
- Reports contain the database name and host but never the password: use
  `config show`, which redacts it, rather than pasting a config file into a
  ticket.
