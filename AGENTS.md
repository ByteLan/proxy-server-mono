# Proxy monorepo

Read [architecture](docs/architecture.md) and [API contract](docs/api.md) before changes.

- Keep runtime small: SQLite only, no Redis/PostgreSQL, a single manager process serves the compiled frontend. Target Linux 1 CPU / 1 GiB; build off-target.
- `proxy-manage-server` is a control plane ONLY. Never add proxy listeners, proxy forwarding or proxy cores to its deployment.
- `proxy-server` is the node Agent; Hysteria2/Xray run separately on proxy nodes.
- Stable subscription URLs read current selections on every refresh. Adding/removing nodes or disabling/deleting nodes must affect the same URL. Revoking a URL does not revoke already downloaded proxy credentials.
- Never commit operational credentials, node configs, SQLite files or generated client YAML. Endpoint server private keys stay local.
- Go module: `proxy-mono`; API types: `internal/model`. Run `go test -tags stdjson,gjson ./...`, `go vet -tags stdjson,gjson ./...`, `npm --prefix web run build`, and `make build` for changes spanning backend/frontend.

## Skills

Canonical skills live in `skills/`; `.agents/skills`, `.claude/skills`, `.codex/skills` link to them for agent discovery.

- [proxy-server-setup](skills/proxy-server-setup/SKILL.md): original protocol deployment reference, with product compatibility notes. Read for Hysteria2/Xray details.
- [install-proxy-server](skills/install-proxy-server/SKILL.md): build/install/enroll a proxy node.
- [install-proxy-manage-server](skills/install-proxy-manage-server/SKILL.md): install the SQLite manager and serve the embedded UI/subscriptions.

## Release and update invariants

- GitHub Actions tests/builds Linux amd64, arm64 and ARMv7; `main-<sha12>` prereleases are immutable. Do not replace published assets or move release tags.
- `internal/selfupdate` changes only a verified server executable, keeps `.previous`, and never updates proxy cores, configuration or SQLite. Service restart remains explicit.
- Private GitHub read tokens are download credentials, separate from admin/enrollment/node identities. Keep them out of logs, command arguments and long-lived service environments.
- CI artifact cleanup is scoped to this workflow. Release cleanup is disabled unless repository variables explicitly enable it; formal versions and Git tags are retained.
- Run Python installer/retention tests via `python3 -m unittest discover -s scripts -p 'test_*.py'` when changing release automation.
