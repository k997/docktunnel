# Contributing to DockTunnel

Thanks for your interest in contributing! This project is a Go application
that bridges Docker containers and Cloudflare Tunnel.

## Development setup

- Go 1.24+ (see `go.mod`)
- Docker (for the Docker provider integration and integration tests)

```bash
go build ./...        # compile
go test ./...         # unit tests
go test -race ./...   # unit tests with the race detector (required before merge)
go vet ./...          # static checks
make fmt-check        # formatting gate
```

## Coding standards

- Follow [Effective Go](https://go.dev/doc/effective_go) and `gofmt` style.
- Every exported symbol needs a doc comment.
- Keep unit test coverage meaningful on changed code (target 80%+ on
  new logic; the `internal/cloudflareManager` integration layer is the
  highest-value place to add tests).
- Error handling: wrap errors with context (`fmt.Errorf("...: %w", err)`)
  and classify retryable vs permanent failures via `pkg/types` errors.
- Run `go test -race ./...` before pushing — the CI gate enforces it.

## What to work on

Check the [Improvement Roadmap](IMPROVEMENT_ROADMAP.md) and `docs/superpowers/`
for the design specs and phase plans. Large behavior changes should start
with a spec/plan under `docs/superpowers/` before code.

## Pull request checklist

1. `gofmt -s -w .` and `make fmt-check` pass.
2. `go vet ./...` and `go test -race ./...` pass.
3. New behavior has unit tests.
4. Update `README.md` / `config.yaml` / `.env.example` when configuration
   or deployment behavior changes.
5. If it's a user-visible behavior change, note it in the PR description.

## Reporting bugs

Open a GitHub issue with:
- DockTunnel version / commit
- Docker and OS versions
- Container labels and config (redact API tokens)
- Logs at `DOCKTUNNEL_LOG_LEVEL=debug`

For security vulnerabilities, see [SECURITY.md](SECURITY.md) — do not open a
public issue.
