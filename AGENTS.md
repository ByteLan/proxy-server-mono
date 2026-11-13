# Proxy monorepo

Read [architecture](docs/architecture.md), [API contract](docs/api.md), and [README](README.md) before changes.

- Keep runtime small: SQLite only, no Redis/PostgreSQL, a single manager process serves the compiled frontend (`internal/webui/dist` embedded via `go:embed`). Target Linux 1 CPU / 1 GiB; prefer building on a development host.
- `proxy-manage-server` is a control plane ONLY. Never add proxy listeners, proxy forwarding or proxy cores to its deployment.
- `proxy-server` is the node Agent; Hysteria2/Xray run separately on proxy nodes.
- Stable subscription URLs read current selections on every refresh. Adding/removing nodes or disabling/deleting nodes must affect the same URL. Revoking a URL does not revoke already downloaded proxy credentials.
- Subscription Clash YAML (`internal/clash/render.go`) explicitly defines `GLOBAL` with `🌍 代理` first (so Global mode uses the same node as Rule mode rather than `DIRECT`), and keeps Claude / ChatGPT / Google dedicated groups as `REJECT`-first `url-test` groups (auto-selecting the fastest protocol across assigned nodes and never leaking to dirty general nodes or `DIRECT` when disabled/deleted).
- Never commit operational credentials, node configs, SQLite files or generated client YAML. Endpoint server private keys stay local.
- Go module: `proxy-mono`; API types: `internal/model`. Run `go test -tags stdjson,gjson ./...`, `go vet -tags stdjson,gjson ./...`, `npm --prefix web run build`, and `make build` for changes spanning backend/frontend.

## DevOps & Release Workflows Index

End-to-end zero-server-compile workflows are documented in `README.md` and `docs/`:

- **Local build & GitHub Release publishing**: [README — 本地开发与发布 GitHub Release](README.md#本地开发与发布-github-release) · [docs/releases.md](docs/releases.md#维护者手动构建与发布) · [scripts/publish-release.sh](scripts/publish-release.sh) · [scripts/build-release.sh](scripts/build-release.sh)
- **Server zero-compile installation (Manager, Node fresh install & `--adopt-existing` companion mode)**: [README — Server 端免编译安装](README.md#server-端免编译安装) · [docs/deployment.md](docs/deployment.md) · [scripts/install.sh](scripts/install.sh) · [scripts/install-proxy-manage-server.sh](scripts/install-proxy-manage-server.sh) · [scripts/install-proxy-server.sh](scripts/install-proxy-server.sh)
- **Server zero-compile update (frontend + backend via embedded binary) & `.previous` rollback**: [README — Server 端免编译更新与回退](README.md#server-端免编译更新与回退) · [docs/releases.md](docs/releases.md#查看检查与更新) · [internal/selfupdate](internal/selfupdate/update.go)
- **Subscription routing & node enrollment**: [README — 订阅分流与节点接入说明](README.md#订阅分流与节点接入说明) · [docs/architecture.md](docs/architecture.md) · [docs/api.md](docs/api.md)

## Skills

Canonical skills live in `skills/`; `.agents/skills`, `.claude/skills`, `.codex/skills` link to them for agent discovery.

- [proxy-server-setup](skills/proxy-server-setup/SKILL.md): original protocol deployment reference, with product compatibility notes. Read for Hysteria2/Xray details.
- [install-proxy-server](skills/install-proxy-server/SKILL.md): build/install/enroll a proxy node.
- [install-proxy-manage-server](skills/install-proxy-manage-server/SKILL.md): install the SQLite manager and serve the embedded UI/subscriptions.

## Manual build and update invariants

- No GitHub Actions workflows, automatic builds, automatic releases or scheduled cleanup are configured. A push or tag does not create downloadable binaries.
- Build on a development/build host. `make build` rebuilds the React UI into `internal/webui/dist` and builds both binaries; direct `go build -tags stdjson,gjson` (`make build-go`) uses the committed embedded frontend without Node. See [source-build assessment](docs/source-build.md).
- `make release` explicitly creates local Linux packages only. `make publish` explicitly builds and uploads a complete release (`amd64`/`arm64`/`armv7` binaries, `install.sh`, `install-bundle.tar.gz`, `SHA256SUMS`) using the local GitHub CLI (`gh`); it requires a clean worktree with committed, pushed source and does not invoke Actions. Existing Release download/install/update commands require an already published asset set.
- `internal/selfupdate` (`update` subcommand) changes only a verified server executable (updating `proxy-manage-server` updates both backend and embedded frontend together), keeps `.previous`, and never updates proxy cores, configuration or SQLite. It downloads binaries and does not compile source. Service restart remains explicit.
- Private GitHub read tokens (`Contents: Read-only`) are download credentials, separate from publish tokens (`Contents: Read and write`) and admin/enrollment/node identities. Keep them out of logs, command arguments and long-lived service environments.
- Run `python3 -m unittest discover -s scripts -p 'test_*.py'` for the manual Release installer tests. Source-based self-update is not implemented.
