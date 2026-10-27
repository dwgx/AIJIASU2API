import type { DocsPage } from './types'

/**
 * Curated public documentation, in both site languages.
 *
 * Every claim here is taken from `docs/API-GUIDE.md` (and `ARCHIVE.md` for the
 * state-journal limit); endpoints and field names come from `docs/openapi.json`.
 * The generator (`scripts/gen-docs.mjs`) renders these pages to
 * `public/docs/<slug>.md` and lists them in `public/llms.txt`.
 *
 * Only public and scoped routes appear below. Admin-only surfaces are named as
 * a category, never as a callable route, so the Markdown mirror cannot teach a
 * customer key an operation it can never reach.
 */
export const docsPages: DocsPage[] = [
  {
    slug: 'overview',
    title: { en: 'Overview and quickstart', zh: '概览与快速上手' },
    summary: {
      en: 'Read the catalog, create a lease for one account, and connect through the loopback gate.',
      zh: '读取目录、为一个账号创建租约，并通过回环网关建立连接。',
    },
    sections: [
      {
        id: 'base-url',
        title: { en: 'Base URL and health', zh: '基础地址与健康检查' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The control plane listens on `http://127.0.0.1:2261` by default (`AJS_LISTEN`). `GET /healthz` and `GET /readyz` are unauthenticated; every other route, including `/metrics`, needs a bearer.',
              zh: '控制面默认监听 `http://127.0.0.1:2261`（`AJS_LISTEN`）。`GET /healthz` 与 `GET /readyz` 无需认证；其余路由（包括 `/metrics`）都需要 Bearer 凭据。',
            },
          },
          {
            kind: 'callout',
            tone: 'pending',
            title: { en: 'Loopback by default', zh: '默认仅回环' },
            text: {
              en: 'The API and the gate data plane are separate. This archive has no public proxy endpoint and no public ingress; a remote client needs an operator-provided tunnel or ingress, which is pending.',
              zh: 'API 与网关数据面是两套东西。本归档没有公网代理端点，也没有公网入口；远程客户端需要运维提供隧道或入口，目前尚未启用。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Health without a token', zh: '无需令牌的健康检查' },
            code: `curl -sS http://127.0.0.1:2261/healthz
curl -sS http://127.0.0.1:2261/readyz`,
          },
        ],
      },
      {
        id: 'story',
        title: { en: 'The story used by these examples', zh: '示例中的固定场景' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'One user, one account, one node, one gate: `u-01`, `acc-02`, `node-04`, port `13003`. The values are illustrative and match the examples in the OpenAPI contract; substitute your own ids. The scoped key is assumed to exist already: an operator mints it in Settings → Access, and key management is not part of the scoped reference.',
              zh: '示例固定使用一个用户、一个账号、一个节点和一个网关：`u-01`、`acc-02`、`node-04`、端口 `13003`。这些值仅作示例，与 OpenAPI 契约中的示例一致，请替换为你自己的 id。示例假定受限密钥已经存在：由管理员在 Settings → Access 中创建，密钥管理不在受限接口参考中。',
            },
          },
          {
            kind: 'table',
            columns: [
              { en: 'Variable', zh: '变量' },
              { en: 'Value', zh: '值' },
              { en: 'Meaning', zh: '含义' },
            ],
            rows: [
              [
                { en: '`AJS_API_KEY`', zh: '`AJS_API_KEY`' },
                { en: '`ajs_sk_...`', zh: '`ajs_sk_...`' },
                {
                  en: 'The scoped secret, shown once when the key is minted.',
                  zh: '受限密钥的明文，仅在创建时展示一次。',
                },
              ],
              [
                { en: '`BASE`', zh: '`BASE`' },
                { en: '`http://127.0.0.1:2261`', zh: '`http://127.0.0.1:2261`' },
                { en: 'Control-plane origin.', zh: '控制面地址。' },
              ],
              [
                { en: '`u-01`', zh: '`u-01`' },
                { en: 'Lease owner', zh: '租约所有者' },
                {
                  en: 'The key subject; a non-admin key only sees its own leases.',
                  zh: '密钥主体；非管理员密钥只能看到自己的租约。',
                },
              ],
              [
                { en: '`acc-02`', zh: '`acc-02`' },
                { en: 'Account grant', zh: '账号授权' },
                {
                  en: 'The account the key names in `accountIds`.',
                  zh: '密钥在 `accountIds` 中声明的账号。',
                },
              ],
              [
                { en: '`node-04`', zh: '`node-04`' },
                { en: 'Live node', zh: '在线节点' },
                {
                  en: 'A `LIVE` node on `acc-02`; its gate is the port a lease points at.',
                  zh: '`acc-02` 上的 `LIVE` 节点；其网关端口就是租约指向的端口。',
                },
              ],
              [
                { en: '`13003`', zh: '`13003`' },
                { en: 'Gate port', zh: '网关端口' },
                {
                  en: 'Loopback SOCKS5 listener for that node.',
                  zh: '该节点对应的回环 SOCKS5 监听端口。',
                },
              ],
            ],
          },
        ],
      },
      {
        id: 'key-shape',
        title: { en: '1. Know what the key can do', zh: '1. 确认密钥的权限' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A key for this walkthrough carries `catalog:read`, `leases:read`, `leases:write`, `proxy:connect` and `usage:read`, is bound to `u-01`, and names `acc-02` in `accountIds`. The secret is returned exactly once; only its SHA-256 is persisted, so store it in `$AJS_API_KEY` and never commit it.',
              zh: '本演练使用的密钥带有 `catalog:read`、`leases:read`、`leases:write`、`proxy:connect` 和 `usage:read`，绑定到 `u-01`，并在 `accountIds` 中声明 `acc-02`。明文只在创建时返回一次，服务端只保存它的 SHA-256；请存入 `$AJS_API_KEY`，不要提交到仓库。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Shell setup', zh: 'Shell 变量' },
            code: `export BASE=http://127.0.0.1:2261
export AJS_API_KEY=ajs_sk_...   # shown once by the key-management surface`,
          },
          {
            kind: 'p',
            text: {
              en: 'Every request below sends `Authorization: Bearer $AJS_API_KEY`. Unsafe methods also send `X-AJS-CSRF: 1`.',
              zh: '下面每个请求都携带 `Authorization: Bearer $AJS_API_KEY`；非安全方法还要携带 `X-AJS-CSRF: 1`。',
            },
          },
        ],
      },
      {
        id: 'list-nodes',
        title: { en: '2. Find a live node', zh: '2. 查找在线节点' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`GET /v1/nodes` needs `catalog:read`. Filter by `state=LIVE` and `account_id=acc-02`; the row for `node-04` carries the `gatePort` a lease will point at. `limit` and `offset` page the result.',
              zh: '`GET /v1/nodes` 需要 `catalog:read`。用 `state=LIVE` 和 `account_id=acc-02` 过滤；`node-04` 这一行会带上租约将指向的 `gatePort`。`limit` 与 `offset` 用于分页。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'List live nodes', zh: '列出在线节点' },
            code: `curl -sS "$BASE/v1/nodes?state=LIVE&account_id=acc-02&limit=50&offset=0" \\
  -H "Authorization: Bearer $AJS_API_KEY"`,
          },
          {
            kind: 'code',
            lang: 'json',
            label: { en: 'The row to keep', zh: '需要记住的一行' },
            code: `{
  "id": "node-04",
  "accountId": "acc-02",
  "region": "jp-tyo",
  "state": "LIVE",
  "gatePort": 13003,
  "protocol": "socks5"
}`,
          },
        ],
      },
      {
        id: 'create-lease',
        title: { en: '3. Create a lease', zh: '3. 创建租约' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`POST /v1/leases` needs `leases:write`. A non-admin key must name an `accountId` inside its grant, and the key subject fills `userId`; a different `userId` is refused with `403` before anything is allocated. `ttlSeconds` accepts `0` for the sticky TTL, `60` or more up to `604800`; one active lease per user and platform is allowed, so a second create answers `409`.',
              zh: '`POST /v1/leases` 需要 `leases:write`。非管理员密钥必须指定其授权范围内的 `accountId`，`userId` 由密钥主体填充；指定其他 `userId` 会在分配前直接返回 `403`。`ttlSeconds` 传 `0` 表示使用粘性 TTL，或传 `60` 到 `604800`；同一用户与平台只允许一个活动租约，重复创建返回 `409`。',
            },
          },
          {
            kind: 'p',
            text: {
              en: 'Send an `Idempotency-Key` (up to 128 characters) so a retry replays the original lease instead of allocating another one. The replay is re-checked against both the key subject and the account grant.',
              zh: '请携带 `Idempotency-Key`（最多 128 个字符），这样重试会重放原租约，而不是再分配一个。重放时会重新校验密钥主体和账号授权。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Create a one-hour lease', zh: '创建一小时租约' },
            code: `curl -sS -X POST "$BASE/v1/leases" \\
  -H "Authorization: Bearer $AJS_API_KEY" \\
  -H "X-AJS-CSRF: 1" \\
  -H "Idempotency-Key: $(openssl rand -hex 16)" \\
  -H "Content-Type: application/json" \\
  -d '{"userId":"u-01","platform":"ios","accountId":"acc-02","ttlSeconds":3600}'`,
          },
          {
            kind: 'code',
            lang: 'json',
            label: { en: '201 Created', zh: '201 已创建' },
            code: `{
  "lease": {
    "id": "lease-09",
    "userId": "u-01",
    "accountId": "acc-02",
    "nodeId": "node-04",
    "protocol": "socks5",
    "gatePort": 13003,
    "expiresAt": "2026-10-27T17:15:00Z",
    "status": "active"
  },
  "replayed": false
}`,
          },
        ],
      },
      {
        id: 'read-lease',
        title: { en: '4. Read the connection block', zh: '4. 读取连接信息' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`GET /v1/leases/{id}` needs `leases:read` and returns the lease plus the loopback endpoint to dial. `port` follows the lease: a switch that rebinds it to another gate also moves the value. The secret is never returned — `credentialKind` only names the shape, and the password is the caller’s own key carrying `proxy:connect`.',
              zh: '`GET /v1/leases/{id}` 需要 `leases:read`，返回租约以及可拨号的回环端点。`port` 跟随租约：切换到其他网关后该值也会变化。接口不会返回密钥明文——`credentialKind` 只说明凭据形态，密码就是调用方自己那张带 `proxy:connect` 的密钥。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Read the lease', zh: '读取租约' },
            code: `curl -sS "$BASE/v1/leases/$LEASE_ID" \\
  -H "Authorization: Bearer $AJS_API_KEY"`,
          },
          {
            kind: 'code',
            lang: 'json',
            label: { en: 'Lease plus connection', zh: '租约与连接信息' },
            code: `{
  "lease": {
    "id": "lease-09",
    "userId": "u-01",
    "accountId": "acc-02",
    "nodeId": "node-04",
    "gatePort": 13003,
    "status": "active"
  },
  "connection": {
    "host": "127.0.0.1",
    "port": 13003,
    "protocol": "socks5",
    "username": "u-01",
    "credentialKind": "ajs_sk"
  }
}`,
          },
        ],
      },
      {
        id: 'dial-gate',
        title: { en: '5. Dial the gate', zh: '5. 连接网关' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'Dial `127.0.0.1:<gatePort>` with SOCKS5. The username is the panel user id; the password is the scoped secret carrying `proxy:connect`. The gate re-checks the lease at `CONNECT`, so a revoked or expired lease refuses the flow instead of relaying it, and it also enforces the user’s allowlist and monthly quota.',
              zh: '用 SOCKS5 连接 `127.0.0.1:<gatePort>`。用户名是面板用户 id，密码是带 `proxy:connect` 的受限密钥。网关会在 `CONNECT` 时重新校验租约，被撤销或已过期的租约会被拒绝而不是继续转发；网关还会执行该用户的允许列表和月度配额。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'SOCKS5 through the gate', zh: '通过网关的 SOCKS5' },
            code: `curl --socks5-hostname 127.0.0.1:13003 \\
  -U "u-01:$AJS_API_KEY" \\
  https://example.com/`,
          },
          {
            kind: 'callout',
            tone: 'warn',
            title: { en: 'The gate is not a public proxy', zh: '网关不是公网代理' },
            text: {
              en: 'All gates bind `127.0.0.1`. There is no public listener, tunnel or router in this archive; a client on another host needs an operator-provided tunnel or ingress (pending). Do not assume the gate port is reachable off the daemon host.',
              zh: '所有网关都绑定 `127.0.0.1`。本归档没有公网监听、隧道或路由；其他主机上的客户端需要运维提供隧道或入口（尚未启用）。不要假设网关端口可以从守护进程所在主机之外访问。',
            },
          },
        ],
      },
      {
        id: 'usage',
        title: { en: '6. Read usage', zh: '6. 读取用量' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`GET /v1/usage` needs `usage:read`. A non-admin key is served a summary over its own request rows only, filtered before aggregation; naming another `userId` answers `403`. The log stores a recyclable account label rather than a stable account id, so a scoped `account=` filter answers `400 INVALID_ARGUMENT` for every value — do not send it.',
              zh: '`GET /v1/usage` 需要 `usage:read`。非管理员密钥只会拿到自己请求行的汇总，过滤发生在聚合之前；指定其他 `userId` 会返回 `403`。请求日志保存的是可复用的账号标签而非稳定账号 id，因此受限密钥传 `account=` 过滤时，任何值都会返回 `400 INVALID_ARGUMENT`——不要传这个参数。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Usage window', zh: '用量时间窗口' },
            code: `curl -sS "$BASE/v1/usage?from=2026-10-01&to=2026-10-31" \\
  -H "Authorization: Bearer $AJS_API_KEY"`,
          },
          {
            kind: 'p',
            text: {
              en: 'A bare date is UTC midnight (`from`) or the end of that day (`to`); an empty `to` means now, an empty `from` means 30 days before `to`, and the widest window is 366 days. The response carries `requests`, `bytes`, `errors`, `accounts[]`, `users[]`, `days[]`, `topTargets[]`, `truncated`, `rowsRetained` (10,000) and `retentionDays`.',
              zh: '纯日期表示 UTC 零点（`from`）或当天结束（`to`）；`to` 为空表示现在，`from` 为空表示 `to` 之前 30 天，最宽窗口为 366 天。响应包含 `requests`、`bytes`、`errors`、`accounts[]`、`users[]`、`days[]`、`topTargets[]`、`truncated`、`rowsRetained`（10,000）和 `retentionDays`。',
            },
          },
        ],
      },
    ],
  },
  {
    slug: 'authentication',
    title: { en: 'Authentication', zh: '认证' },
    summary: {
      en: 'Bearer credentials, CSRF on unsafe methods, and the rate-limit headers a scoped key receives.',
      zh: 'Bearer 凭据、非安全方法上的 CSRF，以及受限密钥会收到的速率限制响应头。',
    },
    sections: [
      {
        id: 'credentials',
        title: { en: 'Two credentials, one header', zh: '两种凭据，一个请求头' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The core API accepts two kinds of bearer credential. They share the `Authorization` header but not their authority.',
              zh: '核心 API 接受两种 Bearer 凭据。它们共用 `Authorization` 请求头，但权限范围不同。',
            },
          },
          {
            kind: 'table',
            columns: [
              { en: 'Credential', zh: '凭据' },
              { en: 'Format', zh: '格式' },
              { en: 'Authority', zh: '权限' },
            ],
            rows: [
              [
                { en: 'Admin token', zh: '管理员令牌' },
                {
                  en: '`AJS_ADMIN_TOKEN`, at least 32 characters',
                  zh: '`AJS_ADMIN_TOKEN`，至少 32 个字符',
                },
                { en: 'Every route.', zh: '所有路由。' },
              ],
              [
                { en: 'Scoped key', zh: '受限密钥' },
                {
                  en: '`ajs_sk_...`, returned once when the key is created',
                  zh: '`ajs_sk_...`，创建密钥时只返回一次',
                },
                {
                  en: 'Only the routes its scopes name, narrowed by its account/region grants; the gate accepts it only with `proxy:connect`.',
                  zh: '仅限其作用域声明的路由，并受账号/区域授权收窄；网关只接受带 `proxy:connect` 的密钥。',
                },
              ],
            ],
          },
          {
            kind: 'p',
            text: {
              en: 'In a strict-RBAC deployment (`AJS_RBAC_STRICT=1`) the shared admin token is retired; use an operator `admin` token instead. Scoped keys keep their own policy.',
              zh: '在严格 RBAC 部署中（`AJS_RBAC_STRICT=1`），共享管理员令牌会被停用，改用运维 `admin` 令牌；受限密钥仍按自己的策略生效。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Authenticated read', zh: '带认证的读取' },
            code: `curl -sS "$BASE/v1/nodes?limit=50&offset=0" \\
  -H "Authorization: Bearer $AJS_API_KEY"`,
          },
        ],
      },
      {
        id: 'csrf',
        title: { en: 'CSRF and Origin', zh: 'CSRF 与 Origin' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'Unsafe methods (`POST`, `PUT`, `PATCH`, `DELETE`) also require `X-AJS-CSRF: 1`. If an `Origin` header is present it must exactly match the configured `AJS_ADMIN_ORIGINS`; CLI and SDK clients omit `Origin` and are treated as the non-browser path.',
              zh: '非安全方法（`POST`、`PUT`、`PATCH`、`DELETE`）还必须携带 `X-AJS-CSRF: 1`。如果请求带有 `Origin`，它必须与配置的 `AJS_ADMIN_ORIGINS` 完全一致；CLI 和 SDK 不发送 `Origin`，按非浏览器路径处理。',
            },
          },
          {
            kind: 'callout',
            tone: 'warn',
            title: { en: 'A bearer alone is not enough', zh: '只有 Bearer 还不够' },
            text: {
              en: 'A browser request from another origin is refused even when the bearer is valid. Send both the CSRF header and an allowed origin, or call from a CLI/SDK.',
              zh: '即使 Bearer 有效，来自其他源的浏览器请求也会被拒绝。请同时发送 CSRF 头并处于允许的源，或改用 CLI/SDK。',
            },
          },
          {
            kind: 'code',
            lang: 'bash',
            label: { en: 'Unsafe request', zh: '非安全请求' },
            code: `curl -sS -X POST "$BASE/v1/leases" \\
  -H "Authorization: Bearer $AJS_API_KEY" \\
  -H "X-AJS-CSRF: 1" \\
  -H "Content-Type: application/json" \\
  -d '{"userId":"u-01","platform":"ios","accountId":"acc-02","ttlSeconds":3600}'`,
          },
        ],
      },
      {
        id: 'rate-limits',
        title: { en: 'Rate limits', zh: '速率限制' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'Scoped-key calls are limited per key. The default is 60 requests per minute; `rateLimitPerMin: 0` means unlimited. A scoped response carries the three headers below except on `401`. Admin tokens do not receive them.',
              zh: '受限密钥按密钥限流，默认每分钟 60 次；`rateLimitPerMin: 0` 表示不限制。除 `401` 外，受限密钥的响应都会带上下面三个响应头；管理员令牌不会收到它们。',
            },
          },
          {
            kind: 'table',
            columns: [
              { en: 'Header', zh: '响应头' },
              { en: 'Meaning', zh: '含义' },
            ],
            rows: [
              [
                { en: '`X-RateLimit-Limit`', zh: '`X-RateLimit-Limit`' },
                {
                  en: 'The key’s per-minute budget; `0` means unlimited.',
                  zh: '密钥的每分钟预算；`0` 表示不限制。',
                },
              ],
              [
                { en: '`X-RateLimit-Remaining`', zh: '`X-RateLimit-Remaining`' },
                {
                  en: 'Tokens left in the current bucket after this request.',
                  zh: '本次请求之后当前桶内剩余的令牌数。',
                },
              ],
              [
                { en: '`X-RateLimit-Reset`', zh: '`X-RateLimit-Reset`' },
                {
                  en: 'Unix epoch seconds when the bucket is full again; `0` when unlimited.',
                  zh: '桶再次充满的 Unix 秒时间戳；不限制时为 `0`。',
                },
              ],
            ],
          },
          {
            kind: 'p',
            text: {
              en: 'A refusal is `429 RATE_LIMITED` with `Retry-After`. A guarded-switch cooldown and an exhausted quota can also answer `429`; honour `Retry-After` instead of retrying immediately.',
              zh: '被限流时返回 `429 RATE_LIMITED` 并带 `Retry-After`。受保护切换的冷却和配额耗尽也可能返回 `429`；请遵守 `Retry-After`，不要立即重试。',
            },
          },
        ],
      },
      {
        id: 'failures',
        title: { en: 'Authentication failures', zh: '认证失败' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A missing or invalid bearer answers `401 UNAUTHORIZED` with the JSON error envelope. A key that lacks the route scope answers `403 FORBIDDEN` with `reason: "scope_required"`; a key whose grants exclude the target answers `403` with `reason: "scoped_key_restricted"`. Clients branch on `reason`, not on the human-readable message.',
              zh: '缺少或无效的 Bearer 会返回 `401 UNAUTHORIZED` 和 JSON 错误信封。密钥缺少路由所需作用域时返回 `403 FORBIDDEN`，`reason` 为 `scope_required`；授权范围排除目标时返回 `403`，`reason` 为 `scoped_key_restricted`。客户端应依据 `reason` 分支，而不是人类可读的 `message`。',
            },
          },
          {
            kind: 'code',
            lang: 'json',
            label: { en: '401 envelope', zh: '401 错误信封' },
            code: `{
  "error": {
    "code": "UNAUTHORIZED",
    "message": "missing or invalid bearer token"
  }
}`,
          },
          {
            kind: 'callout',
            tone: 'info',
            title: { en: 'Validate with a route you can call', zh: '用可调用的路由验证' },
            text: {
              en: 'A scoped key can check itself against any route its scopes already cover — for example `GET /v1/nodes` with `catalog:read`. Admin-only identity routes are not part of the scoped surface.',
              zh: '受限密钥可以用其作用域已覆盖的路由自检，例如带 `catalog:read` 的 `GET /v1/nodes`。管理员专属的身份路由不属于受限接口范围。',
            },
          },
        ],
      },
    ],
  },
  {
    slug: 'scopes',
    title: { en: 'Scopes and grants', zh: '作用域与授权' },
    summary: {
      en: 'The scope table, the key subject rule, and how accountIds/regions narrow every read and write.',
      zh: '作用域表、密钥主体规则，以及 accountIds/regions 如何收窄所有读写。',
    },
    sections: [
      {
        id: 'model',
        title: { en: 'One exact scope per route', zh: '每个路由一个精确作用域' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A scoped key needs the exact scope of the matched route. A key carrying `admin` bypasses the table, and the admin token bypasses it by definition. Every operation a scoped key can reach declares its grant in the spec as `x-required-scope`, together with the `403` and `429` responses and the `X-RateLimit-*` headers; an operation without that extension is not reachable with a scoped key.',
              zh: '受限密钥必须精确匹配所调用路由的作用域。带 `admin` 作用域的密钥可以绕过该表，管理员令牌本身也天然绕过。受限密钥能到达的每个操作都在契约中用 `x-required-scope` 声明授权，并同时声明 `403`、`429` 响应和 `X-RateLimit-*` 响应头；没有该扩展的操作无法用受限密钥访问。',
            },
          },
          {
            kind: 'p',
            text: {
              en: '`proxy:connect` is the one scope with no HTTP route: it authorizes the loopback gate. Admin-only surfaces — device pool, users, API keys, scoped-key management, webhooks, backup, lab telemetry — stay admin-only even for a key that carries a scoped grant.',
              zh: '`proxy:connect` 是唯一没有 HTTP 路由的作用域：它授权回环网关。管理员专属能力——设备池、用户、API 密钥、受限密钥管理、Webhook、备份、Lab 遥测——即使密钥带有受限授权也仍然只有管理员可用。',
            },
          },
        ],
      },
      {
        id: 'table',
        title: { en: 'Scope catalogue', zh: '作用域目录' },
        blocks: [
          {
            kind: 'table',
            columns: [
              { en: 'Scope', zh: '作用域' },
              { en: 'Grants', zh: '授予' },
              { en: 'Representative routes', zh: '代表性路由' },
            ],
            rows: [
              [
                { en: '`accounts:read`', zh: '`accounts:read`' },
                {
                  en: 'Read accounts and account IO capabilities/device.',
                  zh: '读取账号以及账号 IO 能力/设备。',
                },
                {
                  en: '`GET /v1/accounts`, `GET /v1/accounts/{id}`, `GET /v1/accounts/{id}/device`',
                  zh: '`GET /v1/accounts`、`GET /v1/accounts/{id}`、`GET /v1/accounts/{id}/device`',
                },
              ],
              [
                { en: '`accounts:write`', zh: '`accounts:write`' },
                {
                  en: 'Create, patch or delete accounts; login and logout.',
                  zh: '创建、修改或删除账号；登录与登出。',
                },
                {
                  en: '`POST /v1/accounts`, `PATCH /v1/accounts/{id}`, `POST /v1/accounts/{id}/actions/login`',
                  zh: '`POST /v1/accounts`、`PATCH /v1/accounts/{id}`、`POST /v1/accounts/{id}/actions/login`',
                },
              ],
              [
                { en: '`catalog:read`', zh: '`catalog:read`' },
                {
                  en: 'Read the catalog, nodes, operations, topology and the debug trace.',
                  zh: '读取目录、节点、操作、拓扑和调试追踪。',
                },
                {
                  en: '`GET /v1/accounts/{id}/servers`, `GET /v1/nodes`, `GET /v1/operations`, `GET /v1/topology`',
                  zh: '`GET /v1/accounts/{id}/servers`、`GET /v1/nodes`、`GET /v1/operations`、`GET /v1/topology`',
                },
              ],
              [
                { en: '`catalog:write`', zh: '`catalog:write`' },
                {
                  en: 'Selection and guarded switch; node drain/probe; operation cancel.',
                  zh: '选择与受保护切换；节点摘流/探测；取消操作。',
                },
                {
                  en: '`PUT /v1/accounts/{id}/selection`, `POST /v1/accounts/{id}/actions/connect`, `POST /v1/nodes/{id}/actions/drain`',
                  zh: '`PUT /v1/accounts/{id}/selection`、`POST /v1/accounts/{id}/actions/connect`、`POST /v1/nodes/{id}/actions/drain`',
                },
              ],
              [
                { en: '`leases:read`', zh: '`leases:read`' },
                {
                  en: 'List and read the key user’s leases.',
                  zh: '列出并读取密钥所属用户的租约。',
                },
                { en: '`GET /v1/leases`, `GET /v1/leases/{id}`', zh: '`GET /v1/leases`、`GET /v1/leases/{id}`' },
              ],
              [
                { en: '`leases:write`', zh: '`leases:write`' },
                {
                  en: 'Create, rotate and revoke the key user’s leases.',
                  zh: '创建、轮换和撤销密钥所属用户的租约。',
                },
                {
                  en: '`POST /v1/leases`, `POST /v1/leases/{id}/actions/rotate`, `DELETE /v1/leases/{id}`',
                  zh: '`POST /v1/leases`、`POST /v1/leases/{id}/actions/rotate`、`DELETE /v1/leases/{id}`',
                },
              ],
              [
                { en: '`proxy:connect`', zh: '`proxy:connect`' },
                {
                  en: 'Authenticate at the loopback gate and connect through the key user’s lease.',
                  zh: '在回环网关认证，并通过密钥所属用户的租约建立连接。',
                },
                {
                  en: '`SOCKS5 127.0.0.1:<gatePort>`, `HTTP CONNECT 127.0.0.1:<gatePort+offset>`',
                  zh: '`SOCKS5 127.0.0.1:<gatePort>`、`HTTP CONNECT 127.0.0.1:<gatePort+offset>`',
                },
              ],
              [
                { en: '`requests:read`', zh: '`requests:read`' },
                { en: 'Read the request log.', zh: '读取请求日志。' },
                { en: '`GET /v1/requests`', zh: '`GET /v1/requests`' },
              ],
              [
                { en: '`audit:read`', zh: '`audit:read`' },
                { en: 'Read and export the audit log.', zh: '读取并导出审计日志。' },
                {
                  en: '`GET /v1/audit`, `GET /v1/audit/export.csv`',
                  zh: '`GET /v1/audit`、`GET /v1/audit/export.csv`',
                },
              ],
              [
                { en: '`metrics:read`', zh: '`metrics:read`' },
                { en: 'Prometheus and JSON metrics.', zh: 'Prometheus 与 JSON 指标。' },
                { en: '`GET /metrics`, `GET /v1/metrics`', zh: '`GET /metrics`、`GET /v1/metrics`' },
              ],
              [
                { en: '`settings:read`', zh: '`settings:read`' },
                { en: 'Read settings and schedules.', zh: '读取设置与调度。' },
                {
                  en: '`GET /v1/settings`, `GET /v1/schedules`',
                  zh: '`GET /v1/settings`、`GET /v1/schedules`',
                },
              ],
              [
                { en: '`settings:write`', zh: '`settings:write`' },
                { en: 'Patch settings and schedules.', zh: '修改设置与调度。' },
                {
                  en: '`PATCH /v1/settings`, `POST /v1/schedules`',
                  zh: '`PATCH /v1/settings`、`POST /v1/schedules`',
                },
              ],
              [
                { en: '`usage:read`', zh: '`usage:read`' },
                { en: 'Usage aggregation.', zh: '用量聚合。' },
                { en: '`GET /v1/usage`', zh: '`GET /v1/usage`' },
              ],
              [
                { en: '`renewal:read`', zh: '`renewal:read`' },
                {
                  en: 'Renewal plan table and per-account expiry.',
                  zh: '续费套餐表与按账号的到期状态。',
                },
                {
                  en: '`GET /v1/renewal/plans`, `GET /v1/renewal/accounts/{id}`',
                  zh: '`GET /v1/renewal/plans`、`GET /v1/renewal/accounts/{id}`',
                },
              ],
              [
                { en: '`health:read`', zh: '`health:read`' },
                { en: 'Loopback gate health.', zh: '回环网关健康状态。' },
                { en: '`GET /v1/health/nodes`', zh: '`GET /v1/health/nodes`' },
              ],
              [
                { en: '`events:read`', zh: '`events:read`' },
                { en: 'Event replay and the SSE stream.', zh: '事件重放与 SSE 流。' },
                {
                  en: '`GET /v1/events`, `GET /v1/events/stream`',
                  zh: '`GET /v1/events`、`GET /v1/events/stream`',
                },
              ],
              [
                { en: '`admin`', zh: '`admin`' },
                {
                  en: 'Every route, including admin-only surfaces.',
                  zh: '所有路由，包括管理员专属能力。',
                },
                { en: 'All routes.', zh: '全部路由。' },
              ],
            ],
          },
        ],
      },
      {
        id: 'proxy-connect',
        title: { en: 'proxy:connect is the gate', zh: 'proxy:connect 就是网关权限' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The gate accepts a scoped key only when the key is active, belongs to the gate user, carries `proxy:connect`, and allows the gate’s account through its `accountIds`/`regions` grant. A `proxy:connect` key with no grant is refused, exactly as on the API. The flow is then bound to the user’s single active lease on that account: no lease, a lease on another account, or more than one active lease for the same account is refused. The gate keeps only the lease id; the secret is not retained in the principal, flow records or logs.',
              zh: '只有当密钥处于活动状态、属于网关用户、带有 `proxy:connect`，并且其 `accountIds`/`regions` 授权允许该网关账号时，网关才会接受它。没有授权的 `proxy:connect` 密钥会被拒绝，行为与 API 一致。随后流量绑定到该用户在该账号上的唯一活动租约：没有租约、租约属于其他账号、或同一账号存在多个活动租约都会被拒绝。网关只保留租约 id；密钥明文不会进入主体、流记录或日志。',
            },
          },
        ],
      },
      {
        id: 'subject',
        title: { en: 'The subject is the lease owner', zh: '主体就是租约所有者' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A non-admin scoped key is bound to its own `userId`. `GET /v1/leases` returns only leases whose `userId` equals the key’s user; naming another `user_id`/`userId` answers `403 FORBIDDEN` with `reason: "scoped_key_restricted"` and does not reveal whether that user holds a lease. `POST /v1/leases` fills an empty `userId` with the key’s user; a different id is refused with the same `403` before anything is allocated. An `Idempotency-Key` replay is re-checked against both the user and the account grant, so a retry never returns another user’s lease.',
              zh: '非管理员受限密钥绑定到自己的 `userId`。`GET /v1/leases` 只返回 `userId` 等于该密钥用户的租约；指定其他 `user_id`/`userId` 会返回 `403 FORBIDDEN`，`reason` 为 `scoped_key_restricted`，且不会暴露该用户是否持有租约。`POST /v1/leases` 会把空的 `userId` 填成密钥用户；不同 id 会在分配前以同样的 `403` 拒绝。`Idempotency-Key` 重放会同时重新校验用户和账号授权，因此重试不会返回其他用户的租约。',
            },
          },
          {
            kind: 'p',
            text: {
              en: '`GET /v1/leases/{id}`, `DELETE /v1/leases/{id}` and `POST /v1/leases/{id}/actions/rotate` resolve the owner first. A missing id and another user’s lease answer the same `404 NOT_FOUND`; the key’s own lease on an account outside its grant answers `403`. The detail read returns the lease and its loopback `connection` block and never the scoped-key secret. Accounts and nodes are operator assets: they are filtered by the key’s grants, never by `userId`, and no customer key owns them.',
              zh: '`GET /v1/leases/{id}`、`DELETE /v1/leases/{id}` 和 `POST /v1/leases/{id}/actions/rotate` 会先解析所有者。不存在的 id 与其他用户的租约返回相同的 `404 NOT_FOUND`；密钥自己的租约若落在授权之外的账号上则返回 `403`。详情读取返回租约及其回环 `connection` 块，绝不会返回受限密钥明文。账号和节点属于运维资产：它们按键的授权过滤，而不是按 `userId`，客户密钥不拥有它们。',
            },
          },
        ],
      },
      {
        id: 'grants',
        title: { en: 'Narrowing with accountIds and regions', zh: '用 accountIds 与 regions 收窄' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'Both lists narrow reads and writes. An account matches when the id list is empty or contains its id, and the region list is empty or overlaps the account’s `regions`. Empty lists on an account-bound key mean “not narrowed by account” — which is why a new non-admin key carrying `accounts:*`, `catalog:*`, `leases:*` or `proxy:connect` must name at least one `accountIds` or `regions` entry; otherwise key creation answers `400 INVALID_ARGUMENT`.',
              zh: '两个列表都会收窄读写。当 id 列表为空或包含该账号 id，且区域列表为空或与该账号的 `regions` 有交集时，账号匹配。账号绑定密钥的两个列表都为空表示“不按账号收窄”——因此新建的非管理员密钥只要带有 `accounts:*`、`catalog:*`、`leases:*` 或 `proxy:connect`，就必须至少给出一个 `accountIds` 或 `regions` 条目，否则创建返回 `400 INVALID_ARGUMENT`。',
            },
          },
          {
            kind: 'p',
            text: {
              en: 'Writes are narrowed too. A key restricted to `acc-02` is refused on every mutation of another account, including `PATCH`/`DELETE /v1/accounts/{id}`, the login/logout/connect/disconnect/reconnect/select-node actions, the selection routes, lease create/rotate/revoke, node drain/undrain/probe and operation cancel. `POST /v1/leases` must name an allowed `accountId`; a restricted key that omits it is refused, because the store would otherwise pick any free account. A write naming an account-shaped id the store cannot resolve answers the same `403` as a foreign account, so a mutation cannot probe whether an account, node or operation exists.',
              zh: '写入同样被收窄。仅授权 `acc-02` 的密钥对其他账号的所有变更都会被拒绝，包括 `PATCH`/`DELETE /v1/accounts/{id}`、login/logout/connect/disconnect/reconnect/select-node 动作、选择路由、租约创建/轮换/撤销、节点摘流/恢复/探测以及取消操作。`POST /v1/leases` 必须指定允许的 `accountId`；受限密钥省略该字段会被拒绝，否则存储层可能挑选任意空闲账号。写入若指定了存储层无法解析的账号形态 id，会返回与外部账号相同的 `403`，因此变更操作无法用来探测账号、节点或操作是否存在。',
            },
          },
          {
            kind: 'p',
            text: {
              en: 'Reads keep `404` for an unknown id and `403` for a known account outside the key. Global, unattributed surfaces are still refused to a restricted key rather than guessed: `GET /v1/audit`, `GET /metrics` and the event stream. `GET /v1/topology` is projected to the key’s visible nodes, and `GET /v1/health/nodes` is filtered. A key carrying `admin` ignores both lists.',
              zh: '读取时，未知 id 返回 `404`，已知但在授权外的账号返回 `403`。全局、无归属的接口对受限密钥仍然直接拒绝，而不是猜测：`GET /v1/audit`、`GET /metrics` 和事件流。`GET /v1/topology` 会投影为该密钥可见的节点，`GET /v1/health/nodes` 会被过滤。带 `admin` 的密钥忽略这两个列表。',
            },
          },
        ],
      },
      {
        id: 'reissue',
        title: { en: 'Keys minted before the grant rule', zh: '授权规则之前创建的密钥' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A key minted before the account-grant rule with an account, catalog, lease or `proxy:connect` scope fails closed with `403` on every non-admin route until it is reissued. A key whose scopes are all global (`usage:read`, `requests:read`, `metrics:read`, `events:read`, `settings:*`, `audit:read`, `renewal:read`, `health:read`) may omit both lists; that means “not narrowed by account”.',
              zh: '在账号授权规则之前创建、且带有账号、目录、租约或 `proxy:connect` 作用域的密钥，会在所有非管理员路由上以 `403` 失败关闭，直到重新签发。作用域全部为全局的密钥（`usage:read`、`requests:read`、`metrics:read`、`events:read`、`settings:*`、`audit:read`、`renewal:read`、`health:read`）可以省略两个列表，表示“不按账号收窄”。',
            },
          },
        ],
      },
    ],
  },
  {
    slug: 'errors',
    title: { en: 'Errors', zh: '错误' },
    summary: {
      en: 'One envelope, a closed code enum, and which failures are safe to retry.',
      zh: '统一的错误信封、封闭的错误码枚举，以及哪些失败可以安全重试。',
    },
    sections: [
      {
        id: 'envelope',
        title: { en: 'The envelope', zh: '错误信封' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'Every failure uses one JSON shape. `code` is stable; `reason` is optional and may be absent — treat an unknown or missing value as generic. `details` carries small, non-secret context such as the current `snapshotId`, an `operationId` or an `ineligibleReason`.',
              zh: '所有失败都使用同一种 JSON 结构。`code` 是稳定的；`reason` 可选，可能缺失——未知或缺失时按通用原因处理。`details` 携带少量非敏感上下文，例如当前 `snapshotId`、`operationId` 或 `ineligibleReason`。',
            },
          },
          {
            kind: 'code',
            lang: 'json',
            label: { en: 'A conflict with a reason', zh: '带原因码的冲突' },
            code: `{
  "error": {
    "code": "CONFLICT",
    "message": "catalog changed since it was read",
    "reason": "snapshot_changed",
    "details": { "snapshotId": "sha256:..." }
  }
}`,
          },
        ],
      },
      {
        id: 'codes',
        title: { en: 'Closed code enum', zh: '封闭的错误码枚举' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`code` is one of: `INVALID_ARGUMENT`, `NOT_FOUND`, `CONFLICT`, `UPSTREAM_UNAVAILABLE`, `QUOTA_EXCEEDED`, `RATE_LIMITED`, `UNAUTHORIZED`, `FORBIDDEN`, `INVITE_EXPIRED`, `INVITE_REDEEMED`, `METHOD_NOT_ALLOWED`, `INTERNAL`, `BACKUP_BUSY`, `BACKUP_LIVE_NODE`, `BACKUP_DAEMON_RUNNING`, `BACKUP_UNAVAILABLE`, `CURSOR_EXPIRED`, `OPERATION_TIMEOUT`, `PRECONDITION_FAILED`, `PROVIDER_REFUND_UNSUPPORTED`, `CONFIRMATION_REQUIRED`. `FROZEN` maps to `423` but is reserved: no current route produces it.',
              zh: '`code` 只能是以下之一：`INVALID_ARGUMENT`、`NOT_FOUND`、`CONFLICT`、`UPSTREAM_UNAVAILABLE`、`QUOTA_EXCEEDED`、`RATE_LIMITED`、`UNAUTHORIZED`、`FORBIDDEN`、`INVITE_EXPIRED`、`INVITE_REDEEMED`、`METHOD_NOT_ALLOWED`、`INTERNAL`、`BACKUP_BUSY`、`BACKUP_LIVE_NODE`、`BACKUP_DAEMON_RUNNING`、`BACKUP_UNAVAILABLE`、`CURSOR_EXPIRED`、`OPERATION_TIMEOUT`、`PRECONDITION_FAILED`、`PROVIDER_REFUND_UNSUPPORTED`、`CONFIRMATION_REQUIRED`。`FROZEN` 映射到 `423`，但属于保留值：当前没有任何路由会产生它。',
            },
          },
        ],
      },
      {
        id: 'status',
        title: { en: 'Status mapping', zh: '状态码映射' },
        blocks: [
          {
            kind: 'table',
            columns: [
              { en: 'Status', zh: '状态码' },
              { en: 'Code', zh: '错误码' },
              { en: 'Typical reason values', zh: '典型 reason' },
            ],
            rows: [
              [
                { en: '400', zh: '400' },
                { en: '`INVALID_ARGUMENT`', zh: '`INVALID_ARGUMENT`' },
                {
                  en: 'Bad body, missing `confirm`, bad `limit` or window.',
                  zh: '请求体错误、缺少 `confirm`、`limit` 或时间窗口不合法。',
                },
              ],
              [
                { en: '401', zh: '401' },
                { en: '`UNAUTHORIZED`', zh: '`UNAUTHORIZED`' },
                { en: 'Missing or invalid bearer; revoked key.', zh: '缺少或无效的 Bearer；密钥已撤销。' },
              ],
              [
                { en: '403', zh: '403' },
                { en: '`FORBIDDEN`', zh: '`FORBIDDEN`' },
                {
                  en: '`scope_required`, `scoped_key_restricted`.',
                  zh: '`scope_required`、`scoped_key_restricted`。',
                },
              ],
              [
                { en: '404', zh: '404' },
                { en: '`NOT_FOUND`', zh: '`NOT_FOUND`' },
                {
                  en: '`unknown_server`, `no_target`, unknown account/user/lease/operation; another user’s lease answers as unknown.',
                  zh: '`unknown_server`、`no_target`、未知账号/用户/租约/操作；其他用户的租约按未知处理。',
                },
              ],
              [
                { en: '409', zh: '409' },
                { en: '`CONFLICT` / `CONFIRMATION_REQUIRED`', zh: '`CONFLICT` / `CONFIRMATION_REQUIRED`' },
                {
                  en: '`snapshot_changed`, `switch_in_progress`, `ALREADY_CURRENT`, `SERVER_UNAVAILABLE`, `NO_ALTERNATE_SERVER`, `lease_publish_failed`; `confirmation_required` when a destructive schedule needs `confirm: "ARM"` or `"RUN"`.',
                  zh: '`snapshot_changed`、`switch_in_progress`、`ALREADY_CURRENT`、`SERVER_UNAVAILABLE`、`NO_ALTERNATE_SERVER`、`lease_publish_failed`；破坏性调度需要 `confirm: "ARM"` 或 `"RUN"` 时为 `confirmation_required`。',
                },
              ],
              [
                { en: '410', zh: '410' },
                { en: '`CURSOR_EXPIRED` / `INVITE_EXPIRED`', zh: '`CURSOR_EXPIRED` / `INVITE_EXPIRED`' },
                {
                  en: 'The event cursor fell out of the ring (restart from `oldestCursor`), or an invitation expired before redemption.',
                  zh: '事件游标已滑出环形缓冲（从 `oldestCursor` 重新开始），或邀请在兑换前过期。',
                },
              ],
              [
                { en: '412', zh: '412' },
                { en: '`PRECONDITION_FAILED`', zh: '`PRECONDITION_FAILED`' },
                {
                  en: '`stale_revision` on `PATCH /v1/schedules/{id}` with `details.currentRevision`, or `SNAPSHOT_CHANGED` on a waited `select-node`; nothing was mutated.',
                  zh: '`PATCH /v1/schedules/{id}` 的 `stale_revision`（带 `details.currentRevision`），或等待式 `select-node` 的 `SNAPSHOT_CHANGED`；没有任何变更发生。',
                },
              ],
              [
                { en: '413', zh: '413' },
                { en: '`INVALID_ARGUMENT`', zh: '`INVALID_ARGUMENT`' },
                { en: 'The request body exceeds the route limit.', zh: '请求体超过该路由上限。' },
              ],
              [
                { en: '429', zh: '429' },
                { en: '`QUOTA_EXCEEDED` / `RATE_LIMITED`', zh: '`QUOTA_EXCEEDED` / `RATE_LIMITED`' },
                {
                  en: '`account_slots_exhausted`, user quota, `connect_cooldown` with `Retry-After`, scoped-key bucket.',
                  zh: '`account_slots_exhausted`、用户配额、带 `Retry-After` 的 `connect_cooldown`、受限密钥桶。',
                },
              ],
              [
                { en: '500', zh: '500' },
                { en: '`INTERNAL`', zh: '`INTERNAL`' },
                { en: '`store_failed`, unclassified internal failure.', zh: '`store_failed`、未分类的内部失败。' },
              ],
              [
                { en: '502', zh: '502' },
                { en: '`UPSTREAM_UNAVAILABLE`', zh: '`UPSTREAM_UNAVAILABLE`' },
                {
                  en: '`session_open_failed`, `gate_start_failed`, `session_unverified`.',
                  zh: '`session_open_failed`、`gate_start_failed`、`session_unverified`。',
                },
              ],
              [
                { en: '503', zh: '503' },
                { en: '`UPSTREAM_UNAVAILABLE`', zh: '`UPSTREAM_UNAVAILABLE`' },
                {
                  en: 'Controller stopped, catalog unavailable, `lease_publish_failed`, disabled adapter.',
                  zh: '控制器停止、目录不可用、`lease_publish_failed`、适配器被禁用。',
                },
              ],
              [
                { en: '504', zh: '504' },
                { en: '`OPERATION_TIMEOUT`', zh: '`OPERATION_TIMEOUT`' },
                {
                  en: 'A waited `select-node` timed out; read `details.operationId` and poll.',
                  zh: '等待式 `select-node` 超时；读取 `details.operationId` 并轮询。',
                },
              ],
            ],
          },
          {
            kind: 'p',
            text: {
              en: 'Success statuses worth handling explicitly: `201` (lease, account or scoped key created), `202` (async operation accepted) and `204` (lease revoke, scoped-key revoke, account or API-key delete).',
              zh: '需要显式处理的成功状态码：`201`（租约、账号或受限密钥已创建）、`202`（异步操作已接受）和 `204`（租约撤销、受限密钥撤销、账号或 API 密钥删除）。',
            },
          },
        ],
      },
      {
        id: 'retry',
        title: { en: 'What to retry', zh: '哪些请求可以重试' },
        blocks: [
          {
            kind: 'list',
            items: [
              {
                en: 'Retry transport failures, `429` and `5xx` with backoff; honour `Retry-After` when it is present. The Go SDK retries `500/502/503/504`; the TypeScript SDK retries every `5xx`.',
                zh: '传输失败、`429` 和 `5xx` 可以退避重试；存在 `Retry-After` 时必须遵守。Go SDK 重试 `500/502/503/504`；TypeScript SDK 重试所有 `5xx`。',
              },
              {
                en: 'Do not retry `400`, `401`, `403`, `404`, `409` or `412` unchanged: the request or the state must change first. `409` with `confirmation_required` needs an explicit confirmation token, not a retry.',
                zh: '`400`、`401`、`403`、`404`、`409`、`412` 不要原样重试：必须先改变请求或状态。`409` 且为 `confirmation_required` 时需要显式确认令牌，而不是重试。',
              },
              {
                en: 'A `504` on a waited operation carries `details.operationId`; poll `GET /v1/operations/{id}` to a terminal state instead of repeating the switch.',
                zh: '等待式操作的 `504` 会带 `details.operationId`；应轮询 `GET /v1/operations/{id}` 直到终态，而不是重复发起切换。',
              },
              {
                en: 'A `202` is accepted work, not completion. Poll the operation until `succeeded`, `failed` or `canceled`.',
                zh: '`202` 表示工作已接受，并不代表完成。请轮询操作直到 `succeeded`、`failed` 或 `canceled`。',
              },
              {
                en: 'Make lease creation safe to retry with an `Idempotency-Key`; the original lease is replayed instead of allocating a second one.',
                zh: '用 `Idempotency-Key` 让租约创建可以安全重试；服务端会重放原租约，而不是再分配一个。',
              },
            ],
          },
        ],
      },
    ],
  },
  {
    slug: 'sdks',
    title: { en: 'SDKs', zh: 'SDK' },
    summary: {
      en: 'The Go and TypeScript clients, their retry policy, and what has actually been run.',
      zh: 'Go 与 TypeScript 客户端、重试策略，以及实际验证到什么程度。',
    },
    sections: [
      {
        id: 'go',
        title: { en: 'Go: module ajssdk', zh: 'Go：模块 ajssdk' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The Go client lives in `sdk/go` (module `ajssdk`). It is hand-written against `docs/openapi.json`, dependency-free, and covers accounts, catalog, select-node operations, leases, usage and events. It sends `Authorization: Bearer` and `X-AJS-CSRF: 1` on unsafe methods, omits `Origin`, retries transport errors plus `429`/`500`/`502`/`503`/`504` with backoff, honours `Retry-After`, polls a `202` to a terminal operation, and generates an `Idempotency-Key` for `CreateLease`.',
              zh: 'Go 客户端位于 `sdk/go`（模块 `ajssdk`），针对 `docs/openapi.json` 手写，无第三方依赖，覆盖账号、目录、select-node 操作、租约、用量和事件。它会在非安全方法上发送 `Authorization: Bearer` 和 `X-AJS-CSRF: 1`，不发送 `Origin`，对传输错误以及 `429`/`500`/`502`/`503`/`504` 退避重试，遵守 `Retry-After`，把 `202` 轮询到终态，并为 `CreateLease` 生成 `Idempotency-Key`。',
            },
          },
          {
            kind: 'code',
            lang: 'go',
            label: { en: 'Accounts, select-node, lease', zh: '账号、选择节点、租约' },
            code: `client, err := ajs.New("http://127.0.0.1:2261", os.Getenv("AJS_ADMIN_TOKEN"))
accounts, err := client.ListAccounts(ctx, ajs.ListAccountsOptions{State: ajs.AccountActive})
result, err := client.SelectNode(ctx, accounts[0].ID, ajs.SelectNodeRequest{
    Target: &ajs.OperationTarget{Region: "jp-tyo"},
})
lease, err := client.CreateLease(ctx, ajs.CreateLeaseInput{UserID: "u-01", Platform: ajs.PlatformIOS})`,
          },
          {
            kind: 'p',
            text: {
              en: 'Exported methods: `ListAccounts`, `CreateAccount`, `GetAccount`, `PatchAccount`, `DeleteAccount`, `LoginAccount`, `LogoutAccount`, `ListAccountServers`, `SelectNode`, `WaitOperation`, `ListOperations`, `GetOperation`, `CancelOperation`, `ListLeases`, `CreateLease`, `RotateLease`, `ReleaseLease`, `GetUsage`, `ListEvents`, `StreamEvents`, `FollowEvents`.',
              zh: '导出的方法：`ListAccounts`、`CreateAccount`、`GetAccount`、`PatchAccount`、`DeleteAccount`、`LoginAccount`、`LogoutAccount`、`ListAccountServers`、`SelectNode`、`WaitOperation`、`ListOperations`、`GetOperation`、`CancelOperation`、`ListLeases`、`CreateLease`、`RotateLease`、`ReleaseLease`、`GetUsage`、`ListEvents`、`StreamEvents`、`FollowEvents`。',
            },
          },
        ],
      },
      {
        id: 'typescript',
        title: { en: 'TypeScript: @ajs/sdk', zh: 'TypeScript：@ajs/sdk' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The TypeScript client lives in `sdk/ts` (package `@ajs/sdk`). It exposes the same methods in camelCase, plus `ApiError`, `OperationFailure` and `streamEvents()` as an async generator. It retries every `5xx`; the rest of the policy matches the Go client.',
              zh: 'TypeScript 客户端位于 `sdk/ts`（包名 `@ajs/sdk`），以 camelCase 暴露同样的方法，并额外提供 `ApiError`、`OperationFailure` 和异步生成器 `streamEvents()`。它重试所有 `5xx`，其余策略与 Go 客户端一致。',
            },
          },
          {
            kind: 'code',
            lang: 'ts',
            label: { en: 'Accounts, select-node, lease', zh: '账号、选择节点、租约' },
            code: `import { Client } from "@ajs/sdk";

const ajs = new Client({
  baseUrl: "http://127.0.0.1:2261",
  token: process.env.AJS_ADMIN_TOKEN!,
});

const [account] = await ajs.listAccounts({ state: "active" });
const result = await ajs.selectNode(account.id, { target: { region: "jp-tyo" } });
const { lease } = await ajs.createLease({ userId: "u-01", platform: "ios" });`,
          },
        ],
      },
      {
        id: 'honest',
        title: { en: 'What the SDKs do not prove', zh: 'SDK 不能证明什么' },
        blocks: [
          {
            kind: 'callout',
            tone: 'warn',
            title: { en: 'Admin API, not the gate', zh: '调用的是管理 API，不是网关' },
            text: {
              en: 'Both clients call the admin API, not the proxy gate; a successful SDK call does not prove vendor reachability. Neither SDK has been run against a live daemon outside `httptest` or an injected `fetch`.',
              zh: '两个客户端调用的都是管理 API，而不是代理网关；SDK 调用成功并不证明厂商侧可达。除 `httptest` 或注入的 `fetch` 之外，两个 SDK 都还没有在真实守护进程上运行过。',
            },
          },
        ],
      },
    ],
  },
  {
    slug: 'limits',
    title: { en: 'Honest limits', zh: '真实的限制' },
    summary: {
      en: 'Loopback gates, no public ingress, one session slot, and the opt-in state journal.',
      zh: '回环网关、没有公网入口、单会话槽位，以及需显式开启的状态日志。',
    },
    sections: [
      {
        id: 'data-plane',
        title: { en: 'The gate binds 127.0.0.1', zh: '网关绑定 127.0.0.1' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'All gates bind `127.0.0.1`. SOCKS5 is the documented protocol; `AJS_GATE_HTTP=1` adds an HTTP `CONNECT` listener at `gatePort + AJS_GATE_HTTP_OFFSET` (default offset `1000`) with the same admission pipeline. There is no public listener, tunnel or router in this archive. A remote client needs an operator-provided tunnel or ingress — pending, and not something this API can enable.',
              zh: '所有网关都绑定 `127.0.0.1`。文档化的协议是 SOCKS5；`AJS_GATE_HTTP=1` 会在 `gatePort + AJS_GATE_HTTP_OFFSET`（默认偏移 `1000`）上增加一个 HTTP `CONNECT` 监听，准入流程相同。本归档没有公网监听、隧道或路由。远程客户端需要运维提供隧道或入口——尚未启用，也不是这个 API 能开启的能力。',
            },
          },
          {
            kind: 'callout',
            tone: 'pending',
            title: { en: 'No public proxy endpoint', zh: '没有公网代理端点' },
            text: {
              en: 'Do not treat `http://127.0.0.1:2261` or a gate port as a public endpoint. The admin API may be widened with `AJS_LISTEN` plus a bearer, or served through a private admin vhost, but the gate data plane stays loopback.',
              zh: '不要把 `http://127.0.0.1:2261` 或网关端口当成公网端点。管理 API 可以通过 `AJS_LISTEN` 加 Bearer 扩大监听，或通过私有管理虚拟主机提供服务，但网关数据面始终保持回环。',
            },
          },
        ],
      },
      {
        id: 'ingress',
        title: { en: 'No public ingress', zh: '没有公网入口' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'The remote-ingress credential store is experimental and store-only: `AJS_REMOTE_INGRESS=1` plus a 32-byte HMAC pepper file. No HTTP route, listener or router serves it, so no public ingress exists. Enabling the store does not create a proxy endpoint.',
              zh: '远程入口凭据存储是实验性的，而且只做存储：`AJS_REMOTE_INGRESS=1` 加一个 32 字节 HMAC pepper 文件。没有任何 HTTP 路由、监听器或路由组件提供它，因此不存在公网入口。开启该存储并不会创建代理端点。',
            },
          },
        ],
      },
      {
        id: 'slots',
        title: { en: 'One active lease per user and platform', zh: '每个用户与平台一个活动租约' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'A second active lease for the same user and platform answers `409`. The live account has `useCount=1` and `effectiveSlots=1`; this archive admits one slot. A lease on an account whose display capacity is above one carries `warning: "multi_session_unimplemented"`, and no `UseCount=2` coexistence has been proven. The gate refuses a user with no lease, a lease on another account, or more than one active lease for the same account.',
              zh: '同一用户与平台的第二个活动租约会返回 `409`。线上账号的 `useCount=1`、`effectiveSlots=1`，本归档只支持一个槽位。若账号的展示容量大于一，其租约会带 `warning: "multi_session_unimplemented"`，并且没有验证过 `UseCount=2` 的共存。网关会拒绝没有租约、租约属于其他账号、或同一账号存在多个活动租约的用户。',
            },
          },
        ],
      },
      {
        id: 'journal',
        title: { en: 'The state journal is opt-in', zh: '状态日志需显式开启' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: 'State persistence defaults to a JSON snapshot (`AJS_STATE_FILE`, written every 30 seconds and on shutdown). The append-only state journal is opt-in through `AJS_JOURNAL=1`; journal recovery and append tests are offline, and the snapshot remains the default. Do not assume journal durability in a default deployment.',
              zh: '状态持久化默认使用 JSON 快照（`AJS_STATE_FILE`，每 30 秒以及关闭时写入）。追加式状态日志需要通过 `AJS_JOURNAL=1` 显式开启；日志恢复与追加测试都是离线的，默认仍然是快照。不要在默认部署中假设日志式持久化。',
            },
          },
        ],
      },
      {
        id: 'observability',
        title: { en: 'What the health numbers measure', zh: '健康数据到底测了什么' },
        blocks: [
          {
            kind: 'p',
            text: {
              en: '`GET /v1/health/nodes` measures loopback gate reachability on the daemon host, not vendor reachability. `egressIp` on a node or lease is the vendor’s claim. The through-gate egress probe is opt-in (`AJS_EGRESS_PROBE=1`) and spends a real vendor flow. The live evidence in this archive is one account, one node and one egress address.',
              zh: '`GET /v1/health/nodes` 测量的是守护进程主机上的回环网关可达性，而不是厂商侧可达性。节点或租约上的 `egressIp` 是厂商声明。穿网关的出口探测需要显式开启（`AJS_EGRESS_PROBE=1`），并且会消耗一次真实的厂商流量。本归档的线上证据是一个账号、一个节点和一个出口地址。',
            },
          },
        ],
      },
      {
        id: 'retention',
        title: { en: 'Retention and rate limits', zh: '保留期限与速率限制' },
        blocks: [
          {
            kind: 'list',
            items: [
              {
                en: 'The request log keeps at most 10,000 rows; usage windows are capped at 366 days. `truncated: true` means the row cap clipped the window.',
                zh: '请求日志最多保留 10,000 行；用量窗口最长为 366 天。`truncated: true` 表示行数上限截断了窗口。',
              },
              {
                en: 'A scoped key defaults to 60 requests per minute; `rateLimitPerMin: 0` is unlimited. Exceeding the bucket answers `429` with `Retry-After`.',
                zh: '受限密钥默认每分钟 60 次；`rateLimitPerMin: 0` 表示不限制。超出桶容量会返回 `429` 并带 `Retry-After`。',
              },
              {
                en: 'No real payment drill has run and no route charges a card. There is no pricing information on this page by design.',
                zh: '没有执行过真实支付演练，也没有任何路由会扣款。本页按设计不提供价格信息。',
              },
            ],
          },
        ],
      },
    ],
  },
]

/** Lookup for routes that carry a slug in the URL. */
export const docsPageBySlug: Record<string, DocsPage> = Object.fromEntries(
  docsPages.map((page) => [page.slug, page]),
)

/** `/docs` is the overview; every other page lives under its slug. */
export function docsHref(slug: string): string {
  return slug === 'overview' ? '/docs' : `/docs/${slug}`
}
