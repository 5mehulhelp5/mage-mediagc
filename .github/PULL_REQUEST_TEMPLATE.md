## What changed

<!-- A short description, and the reason for it. Link any related issue. -->

## Type

- [ ] Bug fix
- [ ] New feature
- [ ] Behavior change
- [ ] Refactor
- [ ] Documentation
- [ ] Build, CI or packaging

## Risk

<!-- Tick the highest one that applies. -->

- [ ] Read-only (reporting, classification, output)
- [ ] Moves files, and is reversible
- [ ] **Deletes files or database rows**
- [ ] No runtime behavior affected

## Before submitting

- [ ] `make check` passes (gofmt, vet, `go test -race`)
- [ ] `make lint` passes (golangci-lint)
- [ ] `go mod tidy` produces no diff
- [ ] New or changed flags and config keys are documented in `docs/reference.md`
- [ ] Any new user-visible report string exists in **both** message tables
      (`en` and `zh`)
- [ ] Commits follow Conventional Commits (`feat:`, `fix:`, `docs:`, …)

## If this touches quarantine, purge, cache clean or db-clean

<!--
Delete this section if it does not apply. The maintainers will ask for the
answer anyway, so it is quicker to write it now.
-->

- **What is the failure mode this change could introduce?**
- **What did you do to convince yourself it cannot lose data?**
- **Which test proves the guard fires?** Not the happy path — the guard.
- Does any default change? If so, why is that the right trade-off?

## If this changes classification

<!-- Delete if not applicable. -->

- **Which reference source is affected?**
- Could this cause a live image to be treated as an orphan? If so, what limits
  the blast radius?
