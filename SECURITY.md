# Security Policy

## Supported Versions

Only the latest released version (see GitHub Releases / git tags) receives
security fixes. Unreleased `main` builds are not covered.

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Send a private report via GitHub's
[Security Advisories](https://github.com/kongque/docktunnel/security/advisories/new)
feature ("Report a vulnerability"), or email the maintainer directly.

Please include:

- Affected version(s) / commit
- A minimal reproduction (config, labels, environment)
- Impact assessment if known

You should receive an acknowledgement within 48 hours. We will coordinate a
fix and a coordinated disclosure date with you.

## Security-relevant areas in this project

- **API token handling**: `DOCKTUNNEL_CLOUDFLARE_API_TOKEN` (env) or
  `cloudflare.apiToken` (config file). Use a token scoped to only
  Account/Zone/Tunnel permissions, rotate it regularly, and never commit it.
  `.env` / `.env.*` are ignored by `.gitignore` (`.env.example` is kept as a
  template) — never force-add a real `.env` to version control.
- **Docker socket**: mount read-only (`:ro`) and restrict the process to a
  non-root user (the shipped Dockerfile already does both). Note that `:ro`
  does **not** limit Docker API privileges — a compromised process can still
  issue privileged operations through the socket; for stricter isolation,
  evaluate a socket proxy such as
  [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy).
- **Diagnostics HTTP server** (`/metrics`, `/healthz`, `/debug/state`): binds
  to `127.0.0.1:9100` by default. If you set `server.bindAddr` to a
  non-loopback address, set `server.debugToken` so `/debug/state` (which
  exposes all hostnames and service URLs) requires a Bearer token; startup
  validation rejects a non-loopback bind without a token.
- **Cleanup on exit**: `cleanup.onExit` defaults to `false`; when `false`,
  no strategy ever cleans up, so a crash/restart never tears down DNS records
  for running services. When `onExit: true`, only
  `cleanup.strategy: graceful-cleanup` actually cleans up; `fast-exit` skips
  cleanup entirely.
- **Dependency scanning**: CI runs `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`
  (pinned `@latest`, not added to `go.mod`) on every push and pull request.
