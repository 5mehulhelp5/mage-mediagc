# Architecture

mage-mediagc is deliberately shaped as a pipeline with a hard boundary between
*deciding* and *acting*. Everything that decides is read-only; everything that
acts is reversible except where it says otherwise.

```
        app/etc/env.php ──► phpconfig ──► config ──┐
                                                   │
   MySQL ──► magento.CollectRefs ──► RefSet ──┐    │
                                              ▼    ▼
   pub/media ──► media.Scan ──► ScanResult ──► analyzer.Analyze ──► Result
                                                   │
                                                   ▼
                        ┌──────────────────────────┼──────────────────────────┐
                        │                          │                          │
                    report.Render            action.CleanCache          action.Quarantine
                 (table/json/markdown)          (delete)               (move + manifest)
                                                                             │
                                                          ┌──────────────────┴──────────────────┐
                                                          │                                     │
                                                    action.Restore                        action.Purge
                                                     (move back)                          (delete dir)
```

## Layers

### `internal/phpconfig` — reading env.php without PHP

Magento's `app/etc/env.php` is the only authoritative source for a shop's
database credentials, and it is PHP. The obvious options are both bad: require
a PHP runtime, or regex the file and hope.

So this package is a small hand-written lexer and recursive-descent parser for
the subset of PHP that appears in a config file: `return [...]`, `array(...)`,
nested arrays, `=>`, strings with PHP escape rules, numbers, booleans, `null`,
and comments. It knows the difference between `'` and `"` quoting, because a
database password containing `\n` or `$` parses differently in each.

Anything it cannot interpret — a constant, a function call, concatenation —
is a hard error naming the line and column, not a silent `null`. Guessing at
credentials is worse than failing loudly. Statements preceding the `return` are
skipped, because hand-edited files sometimes carry a stray assignment.

### `internal/config` — five layers, gaps filled not overwritten

Defaults, YAML, `env.php`, `MAGEGC_*` environment, flags. The subtle part is
`env.php`: it *fills gaps* rather than overriding, so a user who points
`--db-host` at a replica is never surprised by `env.php` winning.

`DerivePaths` then does the work that makes zero-configuration use possible:
from `magento.root` it derives `pub/media/catalog/product`, the media base, and
a quarantine directory on the same filesystem. From a media path alone it walks
up looking for `app/etc/env.php`.

### `internal/magento` — what keeps an image alive

`CollectRefs` walks five sources and unions their candidate paths:

| Source | Query | Why it matters |
| --- | --- | --- |
| `media_gallery` | gallery ⋈ link ⋈ product | the obvious one |
| `product_image_attr` | `_varchar` where attribute ∈ {image, small_image, thumbnail, swatch_image} | roles that drift out of the gallery |
| `category_image` | `catalog_category_entity_varchar` | **invisible to gallery-only tools** |
| `product_content` | `_text` descriptions, parsed for media paths | **invisible to gallery-only tools** |
| `cms_content` | `cms_page` / `cms_block` content | **invisible to gallery-only tools** |

Two details matter. Attribute ids are resolved through
`eav_entity_type.entity_type_code` rather than a hard-coded `entity_type_id`,
because that numeric id differs between installations. And each collector
records a `SourceStat` — rows scanned, paths added, or the reason it was
skipped — so a scan can show *why* a file was considered orphaned, which is the
difference between a trustworthy report and a number.

`RefSet` stores *candidate* paths rather than one canonical form: the literal
normalized path, its percent-decoded twin, and prefix-stripped variants. A file
is garbage only when no shape matches. This is what stops a percent-encoded CMS
link from looking like an orphan.

`stats.go` and `clean.go` extend the same package to the database rows, using
the same missing-parent trick (`LEFT JOIN ... WHERE r.key IS NULL`) that
survives a schema without foreign keys — which is exactly the situation in
every shop this tool is for.

### `internal/media` — the filesystem index

`Scan` shards the walk by top-level directory and runs the shards in parallel.
Magento's layout (`catalog/product/<c>/<c>/<file>`) makes that division free:
no lock contention, no coordination.

The cache directory is measured separately rather than indexed, because its
size is a headline number in the report but its files are never orphans — they
are derived data, regenerated on demand.

Results are sorted, so every later lookup is a binary search and the orphan
analysis needs no map proportional to the file count. On a 1.5-million-file
tree that is the difference between a few hundred MB of heap and almost none.

### `internal/analyzer` — the judgement call

Comparing two sets is trivial. Deciding *which way to be wrong* is not.

- A file is live if any reference shape matches, including case-insensitively.
  A false "live" costs disk space; a false "orphan" destroys an image.
- A reference with no file on disk is reported as *missing*, not as an error —
  and only when its leading directory exists inside the scanned tree, so CMS
  assets under `pub/media/wysiwyg` do not produce thousands of false alarms.
- Warnings are emitted for the situations that mean the whole analysis is
  suspect: an empty media root, zero references collected, an implausible
  orphan ratio, or missing files. These are printed *with* the report rather
  than swallowed, because a report that says "88% orphaned" is only useful if
  it also says "and here is why you should check that".

### `internal/action` — the only place that writes

Three operations, in increasing order of danger:

- `CleanCache` removes the derived cache and recreates the directory with its
  original uid/gid so the web server keeps working.
- `Quarantine` / `Restore` / `Purge` implement the move-and-manifest model.
  Moves run through a goroutine pool; the manifest is a JSONL file written as
  the moves happen; `Restore` never overwrites an existing destination and
  rewrites the manifest to hold only what it could not restore.
- Every path is validated against its root before it reaches `os.Rename`:
  absolute paths, `..` escapes and traversal are rejected per file, recorded as
  failures, and do not abort the run.

### `internal/warm` — refilling what `cache clean` emptied

This package is the odd one out: it neither reads the database nor writes to the
media tree. It plans one HTTP request per original image and issues them, letting
Magento's own image pipeline do the writing. Warming is therefore not a privileged
operation on the shop; it is what a visitor's browser would do, at scale, before
anybody is waiting.

**One request per original, not one per variant.** Since Magento 2.3, a request
for one cached URL regenerates that image's variant in *every* size set the theme
defines — 25 of them on a stock theme — so the job is as large as the catalog
rather than as large as the URL space. Measured on 2.3.7-p4: 25 sets from a single
request, 0.85–1.24 s to generate them and 0.25 ms when they are already there.

Five decisions shape it.

**The size-set hash is discovered, never computed.** Magento names each cache
directory with an md5 of PHP-side parameters. Reproducing that in Go would mean
reimplementing code that lives in a module the tool cannot see. So a set comes
from `--cache-hash`, from a `--hash-file` snapshot, from the directory listing, or
— when all three are empty — from a single request whose answer is then read back
off the disk. What is never done is request with a value nobody has confirmed:
Magento does not validate the hash in the path, so a made-up one still reaches the
resize service and regenerates the whole family, while the URL that was actually
asked for is never created. That asymmetry is why a supplied value is checked
against the tree and replaced when stale, and why the probe below is not optional
by default.

**Existence is checked with a local `stat`, not a request.** A skip costs
nothing, which is what makes an interrupted run nearly free to resume and
`--max-requests` a usable way to warm a large catalog in slices.

**The run is proved on one request before it is allowed to make a hundred
thousand.** A CDN or reverse proxy in front of the web server can answer from
the edge, returning 200 without PHP ever running; without the check, the entire
run looks successful and the cache stays empty. The probe is one request
followed by an `os.Stat` of the file it should have produced, and a mismatch
stops the run before any pool starts.

**That verdict is not given until the original has been stat'd, and the same
test is applied to the whole plan.** "200 with no file" has a second cause that
is not about the storefront: Magento serves the placeholder image when the
resize service cannot find the original — `Media::launch` catches its
`NotFoundException` — so a tree that is missing files produces exactly the
symptom an edge cache does. Stat'ing one file is cheaper than a wrong diagnosis,
and screening the rest of the plan the same way means the answer does not depend
on which image the probe happened to pick. Counted, never requested: a request
that cannot produce a file is not worth making, and booking it as a success is
how an incomplete media tree ends quietly.

**Magento 2.2 and earlier are refused before anything is sent.** Those releases
have no resize service — the media front controller only copies the requested
file out of the media storage backend — so a request for a missing variant
returns 404 and generates nothing. The check looks for the class file whose
presence *is* that boundary, which states the dependency in the thing the request
path relies on, and it runs before the media tree is walked so the operator sees
the real cause instead of the 404s the probe would have reported. Both this and
the probe exist because a run that cannot work should be reported as such, not
merely fail slowly.

**The rate cap and the failure-rate gate exist because the other end is
somebody else's server.** Concurrency defaults to four rather than to the scan
worker count, because every request spends the storefront's PHP workers. The
gate turns "a large share of these requests failed" into a reported error rather
than a number nobody reads — the usual causes being the wrong host and a WAF
rejecting the client.

Internally it is the same shape as `media.Scan`: a bounded job channel, a fixed
pool, atomic counters, and cancellation taken from the context `Execute` derives
from `SIGINT`. The differences are all about talking to a live storefront — a
ticker for the rate cap, an `io.Copy(io.Discard)` of every body so connections
are reused, and a `Transport` sized to the pool.

### `internal/report` and `internal/cli`

Rendering is pure: `Payload` in, bytes out, in table, JSON or Markdown. The CLI
is a thin cobra layer whose only real job is merging flags into `Overrides` —
and distinguishing a flag the user typed from a flag that merely has a default,
which is why `flags.Changed()` is checked rather than comparing values.

## Why Go

The tool is dropped onto servers that already have a full PHP application, a
web server and a database on them. A single static binary that reads the PHP
config itself means:

- nothing is installed into the shop, so nothing can break it
- it runs when the shop does not — during an upgrade, on a broken deployment,
  against a copied backup directory
- the expensive part (walking 1.5 million files, then querying 2.3 million rows)
  is fast enough to run interactively rather than as an overnight batch
- it can be cross-compiled for the target and shipped as one file

## Known limits

- Reference collection covers the sources listed above. An image referenced
  only by a third-party extension's own table, or by a theme layout file on
  disk, will look orphaned. `scan --format json` lists every path it is about
  to touch, and `quarantine` is reversible — check before purging.
- The scanner reads what is on disk now. A file that is referenced by a product
  currently being edited elsewhere can change state between scan and act;
  this is why `verify` exists.
- `db-clean` removes rows whose referenced product is missing. It does not
  rewrite `url_rewrite`, flat tables or search index tables; re-run Magento's
  own indexers afterwards, as the command output says.
- `cache warm` cannot warm webp or CMYK variants, because Magento decides
  whether to produce those from configuration this tool does not read. It also
  cannot discover a size set that the shop would not produce from a request to
  the base URL it was given: the cold-start recovery asks one URL, so sets
  belonging to a different store view will not appear in the answer. Warm each
  store view separately, one `--base-url` at a time, if that matters.
