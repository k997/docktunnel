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
- **Docker socket**: mount read-only (`:ro`) and restrict the process to a
  non-root user (the shipped Dockerfile already does both).
- **Diagnostics HTTP server** (`/metrics`, `/debug/state`): binds to
  `127.0.0.1:9100` by default. If you set `server.bindAddr` to a non-loopback
  address, set `server.debugToken` so `/debug/state` (which exposes all
  hostnames and service URLs) requires a Bearer token.
- **Cleanup on exit**: defaults to `false` so a crash/restart never tears down
  DNS records for running services.
