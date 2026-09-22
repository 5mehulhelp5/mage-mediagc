# Contributing

Thanks for considering a contribution. This is a tool that deletes things, so
the bar for changes that touch the destructive paths is deliberately high.

## Ground rules

**Every change that can delete or move data needs a test that proves the failure
mode it prevents.** Not a test that the happy path works — a test that the guard
fires. If you change the orphan-ratio threshold, the test must show the run
aborting. If you change quarantine, the test must show that an interrupted run
still describes exactly what it moved.

**Never weaken the default in favour of convenience.** Dry run by default,
same-filesystem enforcement, the ratio guard and "move, never delete" are the
reasons this tool is safe to point at a production shop. A flag may relax a
default for a user who knows what they are doing; the default itself does not
move.

**A false "live" costs disk space; a false "orphan" loses an image.** When you
have to choose, choose the conservative option, and say why in a comment.

## Getting started

```sh
git clone https://github.com/shuaiZend/mage-mediagc
cd mage-mediagc
make build          # honors go env GOOS/GOARCH

make test           # go test -race on the host platform
make lint           # golangci-lint
make check          # fmt + vet + test, run this before opening a PR
```

`make help` lists every target.

### A note on `go env GOOS`

If your `go env` has `GOOS`/`GOARCH` set for cross-compilation — common on a
machine that also builds for servers — plain `go test` will try to run
cross-compiled binaries and fail with `exec format error`. The `Makefile` targets
already override to `GOHOSTOS`/`GOHOSTARCH` for this reason. If you invoke `go`
directly, do the same:

```sh
GOOS=$(go env GOHOSTOS) GOARCH=$(go env GOHOSTARCH) go test ./...
```

## Tests

### Unit tests

`go test ./...`. No external dependencies.

The parsers, configuration resolution, analysis and report rendering are all
covered here. `internal/report/render_test.go` includes cross-package invariants
worth knowing about before you edit either side:

- the language tags accepted by `config` and the message tables in `report` must
  not drift apart;
- the Markdown report must not recommend a flag that does not exist;
- Markdown headings must be title-cased, because they reuse console labels
  elsewhere;
- the orphan file list must stay out of the JSON payload.

### Integration tests

`internal/integration/mysql_test.go` runs the whole pipeline against a real
MySQL server. It is skipped unless you point it at one:

```sh
export MAGEGC_TEST_DB_HOST=127.0.0.1
export MAGEGC_TEST_DB_PORT=3306          # defaults to 3306 if unset
export MAGEGC_TEST_DB_NAME=magegc_test   # required, must contain "test"
export MAGEGC_TEST_DB_USER=root
export MAGEGC_TEST_DB_PASSWORD=itsecret

go test -run TestEndToEnd -v ./internal/integration/
```

`MAGEGC_TEST_DB_NAME` has no default on purpose: the test fails loudly rather
than guessing at a database name, so it can never be pointed at a production
catalog by omission.

Safety rails, because this test writes to whatever it is pointed at:

- the database name **must contain `test`**, or the test refuses to run;
- every table it creates is prefixed with a per-run random token, so concurrent
  runs and a shared server do not collide;
- it drops only the tables it created.

# A throwaway server, on a temporary filesystem so it leaves nothing behind.
# The fixture is regenerated every run, so no volume is needed.
docker run --rm -d --name mysql-test \
  -e MYSQL_ROOT_PASSWORD=itsecret -p 13306:3306 \
  --tmpfs /var/lib/mysql:rw,size=2g \
  mysql:8.0
```

Wait for it with a real query, not a ping — `mysqladmin ping` answers `0` against
MySQL's temporary initialization server, before the root password is applied, so
a ping-based loop races ahead and the next query fails with
`ERROR 1045 (28000): Access denied`:

```sh
until docker exec mysql-test \
        mysql -uroot -pitsecret -N -e 'SELECT 1' >/dev/null 2>&1; do sleep 1; done
```

Then point `MAGEGC_TEST_DB_PORT` at 13306.

## Code conventions

- Standard Go. `gofmt -s` and `golangci-lint run` must be clean; CI enforces
  both, along with `go vet` and `go mod tidy` idempotence.
- **Comments explain why, not what.** The interesting comments in this codebase
  are the ones that record a hazard: why a sweep repeats, why matching is
  case-insensitive, why an error is deliberately swallowed. Match that.
- **Options structs are passed by value**, and `hugeParam` is disabled in
  `.golangci.yml` on purpose. Do not "fix" it.
- **Logs, errors and JSON are English.** Reports are the only localized surface,
  via the message table in `internal/report`. If you add a user-visible string to
  a report, add it to *both* tables — `TestMessageTablesArePopulated` will catch
  you if you forget.
- Any new flag or config key must be documented in
  [`docs/reference.md`](docs/reference.md). That file is the contract.
- Prefer the standard library. New dependencies need a reason that survives
  review; the current dependency set is three packages
  (`cobra`, `go-sql-driver/mysql`, `yaml.v3`).

## Commits and pull requests

[Conventional Commits](https://www.conventionalcommits.org/) are used for the
generated changelog: `feat:`, `fix:`, `docs:`, `refactor:`, `perf:`, `test:`,
`build:`, `ci:`, `chore:`. `chore`, `ci:`, `test:` and `style:` commits are
filtered out of release notes.

A pull request should say what changed, why, and — for anything touching
quarantine, purge, cache clean or db-clean — what you did to convince yourself
it cannot lose data.

## Reporting bugs

Include the output of `mage-mediagc version` and, when configuration is
involved, `mage-mediagc config show` (it redacts passwords). For a wrong
classification, the exact relative path and why you believe the file is live is
far more useful than a screenshot.

**Never paste a database password.** Use `MAGEGC_DB_PASSWORD` rather than
`--db-password`, so it does not end up in your shell history or in `ps` output —
and therefore not in the bug report either.

## Security

Do not open a public issue for a vulnerability. See [SECURITY.md](SECURITY.md).

## License

Contributions are accepted under the Apache License 2.0. See [LICENSE](LICENSE).
