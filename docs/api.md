# API contract (v1)

All JSON errors: `{ "error": "message" }`. List responses are bare arrays, never null. IDs opaque strings. Times Unix seconds.

## Admin

All `/api/admin/*` require `Authorization: Bearer <ADMIN_TOKEN>` (static high-entropy admin token; UI login stores it in sessionStorage). `GET /api/admin/health` -> `{ "ok": true }`.

- `GET /api/admin/nodes` -> `model.Node[]`, endpoint secrets redacted (password, username, uuid, public_key, short_id); heartbeat online != protocol connectivity.
- `PATCH /api/admin/nodes/:id` JSON `{ "name": "...", "enabled": true }` -> Node. Both fields required.
- `DELETE /api/admin/nodes/:id` -> 204. Permanently revokes agent token and removes it from subscription selections; requires re-enrollment with a new identity.
- `GET /api/admin/subscriptions` -> `model.Subscription[]` (includes decrypted `url` and optional `http_url` for authenticated admins so links can be viewed/copied repeatedly).
- `POST /api/admin/subscriptions` JSON `{ "name":"...", "node_ids":["..."], "protocols":["hysteria2","vless"], "routes":{ "claude":{"custom":true,"node_ids":["..."]}, "chatgpt":{"custom":false,"node_ids":[]}, "google":{"custom":false,"node_ids":[]} }, "enabled":true }` -> 201 `{ "subscription": Subscription, "url":"<PUBLIC_URL>/sub/<token>", "http_url":"<HTTP_SUB_PUBLIC_URL>/sub/<token>" }` (`http_url` included when `HTTP_SUB_PUBLIC_URL` is configured). `routes` optional (defaults to `custom:false`, inheriting `node_ids`); when `custom:true`, that service group strictly uses only its selected `node_ids` (`url-test` with `REJECT` fallback, never falling back to `node_ids` or `DIRECT`, even if its nodes are later disabled or deleted). Empty protocols = all supported protocols. Empty node_ids allowed, returns empty general configuration.
- `PUT /api/admin/subscriptions/:id` same body -> Subscription (with `url` / `http_url`). Selection changes affect next GET of existing URL, without token rotation.
- `POST /api/admin/subscriptions/:id/rotate` -> `{ "subscription": Subscription, "url": "...", "http_url": "..." }`, invalidates old link and stores the new token encrypted at rest (`token_enc`).
- `DELETE /api/admin/subscriptions/:id` -> 204, invalidates link.
- `GET /api/admin/subscriptions/:id/preview` -> full YAML (contains credentials; do not log).

## Agent (all requests bearer-authenticated)

- `POST /api/agent/register` uses ENROLL_TOKEN, body model.RegisterRequest, returns 201 `{ "token":"<new node token>" }`. Existing node ID -> 409, never overwrites an identity. Persist returned token locally.
- `POST /api/agent/heartbeat` uses node token, body model.HeartbeatRequest. 200 `{ "ok":true }`. Send complete endpoints on first heartbeat after process start and whenever config_hash changes; ordinary heartbeats omit endpoints. Manager does not derive endpoint addresses from source IP. Node token is bound to ID; disabled node may heartbeat but is excluded from subscriptions.

## Public

- `GET /healthz` -> `{ "ok":true }`.
- `GET /sub/:token` -> full mihomo YAML, dynamic database lookup; no login; token is capability credential (encrypted at rest via `master.key` for admin retrieval). `Cache-Control: no-store`, `profile-update-interval: 1`. Disabled or revoked token -> 404. Empty selection returns a valid config with DIRECT only. Offline heartbeat does NOT automatically remove a selected endpoint.

Frontend: Vite proxies `/api`, `/sub` to localhost:8080. Production assets embedded in manager binary. No npm/Node required on target VPS. Manager supports direct HTTPS via the paired `TLS_CERT_FILE`/`TLS_KEY_FILE` environment variables (or an existing TLS reverse proxy), plus an optional read-only HTTP subscription listener via `HTTP_SUB_LISTEN`/`HTTP_SUB_PUBLIC_URL` (serving `/healthz` and `/sub/:token` only, never `/api/admin/*` or `/api/agent/*`) for IP-only Clash clients. Management server never handles proxy traffic.
