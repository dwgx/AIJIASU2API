import assert from "node:assert/strict";
import test from "node:test";
import {
  ApiError,
  Client,
  NetworkError,
  OperationFailure,
  OperationTimeoutError,
  isApiErrorCode,
  isApiErrorReason,
  isScopeRequired,
  isScopedKeyRestricted,
} from "../dist/index.js";

const token = "ts-test-token-not-a-secret";

function json(body, status = 200, headers = {}) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

function stubClient(handler, options = {}) {
  const calls = [];
  const fetch = async (url, init) => {
    const call = {
      url,
      init,
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
    };
    calls.push(call);
    return handler(call, calls.length);
  };
  const client = new Client({
    baseUrl: "http://127.0.0.1:18080/",
    token,
    fetch,
    retry: {
      maxAttempts: 3,
      baseDelayMs: 1,
      maxDelayMs: 5,
      retryAfterCapMs: 1000,
      jitter: 0,
    },
    ...options,
  });
  return { client, calls };
}

const account = {
  id: "acc-1",
  label: "primary",
  username: "u1",
  platform: "linux",
  state: "active",
  concurrency: 1,
  useCount: 1,
  effectiveSlots: 1,
  regions: ["cn"],
  nodeCount: 2,
  liveNodeCount: 1,
  usageBytes: 42,
  trafficBudgetBytes: null,
  expiresAt: "",
  lastLoginAt: "",
  createdAt: "2026-10-01T00:00:00Z",
};

test("listAccounts sends auth, query and decodes", async () => {
  const { client, calls } = stubClient(() => json([account]));
  const accounts = await client.listAccounts({ state: "active", limit: 5, offset: 10 });
  assert.equal(accounts.length, 1);
  assert.equal(accounts[0].id, "acc-1");
  assert.equal(accounts[0].trafficBudgetBytes, null);
  const url = new URL(calls[0].url);
  assert.equal(url.pathname, "/v1/accounts");
  assert.equal(url.searchParams.get("state"), "active");
  assert.equal(url.searchParams.get("limit"), "5");
  assert.equal(url.searchParams.get("offset"), "10");
  assert.equal(calls[0].init.headers.Authorization, `Bearer ${token}`);
  assert.equal(calls[0].init.headers["X-AJS-CSRF"], undefined);
  assert.equal(String(client).includes(token), false);
  assert.match(calls[0].init.headers["User-Agent"], /^ajs-sdk-ts\//);
});

test("typed ApiError carries code/reason/details and no token", async () => {
  const { client } = stubClient(() =>
    json(
      {
        error: {
          code: "CONFLICT",
          message: "duplicate label",
          reason: "duplicate_label",
          details: { field: "label" },
        },
      },
      409,
    ),
  );
  await assert.rejects(
    client.createAccount({
      label: "dup",
      username: "u",
      password: "p",
      platform: "linux",
      concurrency: 1,
    }),
    (err) => {
      assert.ok(err instanceof ApiError);
      assert.equal(err.status, 409);
      assert.equal(err.code, "CONFLICT");
      assert.equal(err.reason, "duplicate_label");
      assert.equal(err.details.field, "label");
      assert.equal(err.retryable, false);
      assert.equal(err.message.includes(token), false);
      assert.equal(isApiErrorCode(err, "CONFLICT"), true);
      return true;
    },
  );
});

test("retries 5xx with backoff then succeeds", async () => {
  const { client, calls } = stubClient((_call, n) =>
    n < 3 ? json({ error: { code: "INTERNAL", message: "boom" } }, 500) : json([]),
  );
  await client.listAccounts();
  assert.equal(calls.length, 3);
});

test("honors Retry-After on 429", async () => {
  const { client, calls } = stubClient((_call, n) =>
    n === 1
      ? json({ error: { code: "RATE_LIMITED", message: "slow down" } }, 429, {
          "Retry-After": "0",
        })
      : json([]),
  );
  const started = Date.now();
  await client.listAccounts();
  assert.equal(calls.length, 2);
  assert.ok(Date.now() - started < 1000);
});

test("does not retry 4xx", async () => {
  const { client, calls } = stubClient(() =>
    json({ error: { code: "INVALID_ARGUMENT", message: "bad" } }, 400),
  );
  await assert.rejects(client.listAccounts(), (err) => isApiErrorCode(err, "INVALID_ARGUMENT"));
  assert.equal(calls.length, 1);
});

test("POST without an idempotency key is not retried", async () => {
  const { client, calls } = stubClient(() =>
    json({ error: { code: "INTERNAL", message: "boom" } }, 500),
  );
  await assert.rejects(
    client.createAccount({
      label: "a",
      username: "u",
      password: "p",
      platform: "linux",
      concurrency: 1,
    }),
  );
  assert.equal(calls.length, 1);
});

test("createLease generates a matching key and retries safely", async () => {
  const { client, calls } = stubClient((call, n) => {
    if (n === 1) return json({ error: { code: "INTERNAL", message: "boom" } }, 500);
    assert.equal(call.init.headers["Idempotency-Key"], call.body.idempotencyKey);
    assert.equal(call.init.headers["X-AJS-CSRF"], "1");
    assert.match(call.body.idempotencyKey, /^sdk-/);
    return json(
      {
        lease: {
          id: "lease-1",
          userId: "usr-1",
          userName: "u",
          accountId: "acc-1",
          accountLabel: "primary",
          platform: "linux",
          nodeId: "acc-1:srv-1",
          nodeLabel: "Tokyo",
          region: "jp",
          egressIp: "203.0.113.9",
          protocol: "socks5",
          gatePort: 19001,
          ttlSeconds: 3600,
          expiresAt: "2026-10-26T01:00:00Z",
          requests: 0,
          bytes: 0,
          sticky: true,
          status: "active",
          idempotencyKey: call.body.idempotencyKey,
          createdAt: "2026-10-26T00:00:00Z",
        },
        replayed: false,
      },
      201,
    );
  });
  const result = await client.createLease({ userId: "usr-1", platform: "linux" });
  assert.equal(calls.length, 2);
  assert.equal(result.lease.id, "lease-1");
  assert.equal(result.lease.gatePort, 19001);
});

test("getLease decodes the connection without a secret", async () => {
  const { client, calls } = stubClient(() =>
    json({
      lease: {
        id: "lease-1",
        userId: "usr-1",
        userName: "u",
        accountId: "acc-1",
        accountLabel: "primary",
        platform: "linux",
        nodeId: "acc-1:srv-1",
        nodeLabel: "Tokyo",
        region: "jp",
        egressIp: "203.0.113.9",
        protocol: "socks5",
        gatePort: 19001,
        ttlSeconds: 3600,
        expiresAt: "2026-10-26T01:00:00Z",
        requests: 3,
        bytes: 1024,
        sticky: true,
        status: "active",
        idempotencyKey: "k",
        createdAt: "2026-10-26T00:00:00Z",
      },
      connection: {
        host: "127.0.0.1",
        port: 19001,
        protocol: "socks5",
        username: "usr-1",
        credentialKind: "ajs_sk",
      },
    }),
  );
  const detail = await client.getLease("lease-1");
  assert.equal(detail.lease.id, "lease-1");
  assert.deepEqual(detail.connection, {
    host: "127.0.0.1",
    port: 19001,
    protocol: "socks5",
    username: "usr-1",
    credentialKind: "ajs_sk",
  });
  assert.equal(new URL(calls[0].url).pathname, "/v1/leases/lease-1");
  assert.equal(calls[0].init.method, "GET");
  assert.equal(JSON.stringify(detail).includes(token), false);
});

test("listNodes encodes filters and decodes nodes", async () => {
  const { client, calls } = stubClient(() =>
    json([
      {
        id: "acc-1:srv-1",
        accountId: "acc-1",
        accountLabel: "primary",
        label: "Tokyo",
        region: "jp",
        country: "JP",
        city: "Tokyo",
        state: "LIVE",
        latencyMs: 42,
        successRate: 0.98,
        inUse: 1,
        capacity: 4,
        egressIp: "203.0.113.9",
        protocol: "socks5",
        gatePort: 19001,
        lastProbeAt: "2026-10-26T00:00:00Z",
        serverId: "srv-1",
      },
      {
        id: "acc-2:srv-2",
        accountId: "acc-2",
        accountLabel: "backup",
        label: "Osaka",
        region: "jp",
        country: "JP",
        city: "Osaka",
        state: "STALE",
        latencyMs: 0,
        successRate: 0,
        inUse: 0,
        capacity: 2,
        egressIp: "",
        protocol: "http",
        gatePort: 0,
        lastProbeAt: "",
      },
    ]),
  );
  const nodes = await client.listNodes({
    accountId: "acc-1",
    state: "LIVE",
    region: "jp",
    limit: 20,
    offset: 40,
  });
  assert.equal(nodes.length, 2);
  assert.equal(nodes[0].serverId, "srv-1");
  assert.equal(nodes[0].successRate, 0.98);
  assert.equal(nodes[1].state, "STALE");
  assert.equal(nodes[1].serverId, undefined);
  const url = new URL(calls[0].url);
  assert.equal(url.pathname, "/v1/nodes");
  assert.equal(url.searchParams.get("account_id"), "acc-1");
  assert.equal(url.searchParams.get("state"), "LIVE");
  assert.equal(url.searchParams.get("region"), "jp");
  assert.equal(url.searchParams.get("limit"), "20");
  assert.equal(url.searchParams.get("offset"), "40");
});

test("listRequests keeps an explicit limit=0 and decodes rows", async () => {
  const row = {
    id: "req-1",
    ts: "2026-10-26T00:00:00Z",
    userId: "usr-1",
    userName: "u",
    accountLabel: "primary",
    nodeLabel: "Tokyo",
    region: "jp",
    method: "CONNECT",
    target: "example.com:443",
    status: 200,
    bytes: 2048,
    latencyMs: 12,
    source: "gate",
  };
  const { client, calls } = stubClient(() => json([row]));
  const requests = await client.listRequests({ userId: "usr-1", limit: 0, offset: 5 });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].source, "gate");
  assert.equal(requests[0].bytes, 2048);
  const url = new URL(calls[0].url);
  assert.equal(url.pathname, "/v1/requests");
  assert.equal(url.searchParams.get("user_id"), "usr-1");
  assert.equal(url.searchParams.get("limit"), "0");
  assert.equal(url.searchParams.get("offset"), "5");

  const second = stubClient(() => json([]));
  await second.client.listRequests();
  assert.equal(new URL(second.calls[0].url).searchParams.has("limit"), false);
});

test("scoped-key 403 reasons map to helpers, not messages", async () => {
  const denial = (reason, message = "denied") =>
    json({ error: { code: "FORBIDDEN", message, reason } }, 403);
  const scope = stubClient(() => denial("scope_required"));
  await assert.rejects(scope.client.listNodes(), (err) => {
    assert.equal(isScopeRequired(err), true);
    assert.equal(isScopedKeyRestricted(err), false);
    assert.equal(isApiErrorReason(err, "scope_required"), true);
    assert.equal(err instanceof ApiError, true);
    assert.equal(err.message.includes(token), false);
    return true;
  });
  const restricted = stubClient(() => denial("scoped_key_restricted"));
  await assert.rejects(restricted.client.listRequests(), (err) => {
    assert.equal(isScopedKeyRestricted(err), true);
    assert.equal(isScopeRequired(err), false);
    return true;
  });
  const other = stubClient(() => denial("origin_forbidden"));
  await assert.rejects(other.client.listNodes(), (err) => {
    assert.equal(isScopeRequired(err), false);
    assert.equal(isScopedKeyRestricted(err), false);
    return true;
  });
  assert.equal(isScopeRequired(new Error("boom")), false);
  assert.equal(isScopedKeyRestricted(new Error("boom")), false);
});

test("patchAccount sends an explicit null budget", async () => {
  const { client, calls } = stubClient(() => json(account));
  await client.patchAccount("acc-1", { label: "renamed", trafficBudgetBytes: null });
  assert.equal(calls[0].init.method, "PATCH");
  assert.equal(new URL(calls[0].url).pathname, "/v1/accounts/acc-1");
  assert.deepEqual(calls[0].body, { label: "renamed", trafficBudgetBytes: null });
});

test("releaseLease always confirms", async () => {
  const { client, calls } = stubClient(() => new Response(null, { status: 204 }));
  await client.releaseLease("lease-1");
  assert.equal(calls[0].init.method, "DELETE");
  assert.deepEqual(calls[0].body, { confirm: true });
});

test("getUsage encodes the window and decodes nullable budgets", async () => {
  const { client, calls } = stubClient(() =>
    json({
      from: "2026-10-01T00:00:00Z",
      to: "2026-10-26T23:59:59Z",
      requests: 10,
      bytes: 2048,
      errors: 1,
      accounts: [
        {
          account: "primary",
          requests: 10,
          bytes: 2048,
          errors: 1,
          avgLatencyMs: 12,
          buckets: [],
          trafficBudgetBytes: null,
          usedBytes: 100,
          remainingBytes: null,
        },
      ],
      users: [],
      days: [],
      topTargets: [],
      truncated: false,
      rowsRetained: 10000,
      retentionDays: 3,
      quotaEnforced: false,
    }),
  );
  const summary = await client.getUsage({
    from: "2026-10-01",
    to: "2026-10-26",
    account: "primary",
    userId: "usr-1",
  });
  assert.equal(summary.requests, 10);
  assert.equal(summary.accounts[0].trafficBudgetBytes, null);
  const url = new URL(calls[0].url);
  assert.equal(url.searchParams.get("userId"), "usr-1");
  assert.equal(url.searchParams.get("from"), "2026-10-01");
});

test("listEvents formats wait and surfaces CURSOR_EXPIRED", async () => {
  const { client, calls } = stubClient(() =>
    json(
      {
        error: {
          code: "CURSOR_EXPIRED",
          message: "cursor is no longer retained",
          details: { oldestCursor: "evt-000042" },
        },
      },
      410,
    ),
  );
  await assert.rejects(client.listEvents({ since: "evt-1", limit: 50, wait: "25s" }), (err) => {
    assert.ok(err instanceof ApiError);
    assert.equal(err.code, "CURSOR_EXPIRED");
    assert.equal(err.details.oldestCursor, "evt-000042");
    return true;
  });
  const url = new URL(calls[0].url);
  assert.equal(url.searchParams.get("wait"), "25s");
  assert.equal(url.searchParams.get("since"), "evt-1");
});

test("constructor validates its inputs", () => {
  assert.throws(() => new Client({ baseUrl: "", token }));
  assert.throws(() => new Client({ baseUrl: "ftp://example.com", token }));
  assert.throws(() => new Client({ baseUrl: "http://example.com", token: "  " }));
  assert.throws(() => new Client({ baseUrl: "http://user:pass@example.com", token }));
  assert.throws(() => new Client({ baseUrl: "http://example.com?x=1", token }));
});

test("transport failure is a typed NetworkError without the token", async () => {
  const { client } = stubClient(() => {
    throw new Error("connect ECONNREFUSED");
  });
  await assert.rejects(client.listAccounts(), (err) => {
    assert.ok(err instanceof NetworkError);
    assert.equal(err.message.includes(token), false);
    return true;
  });
});

test("selectNode polls a 202 operation to its result", async () => {
  const operation = (state, extra = {}) => ({
    id: "op-1",
    type: "account.switch",
    state,
    accountId: "acc-1",
    target: { serverId: "srv-7" },
    requestedBy: "admin",
    createdAt: "",
    startedAt: "",
    finishedAt: "",
    steps: [],
    result: null,
    error: null,
    ...extra,
  });
  let polls = 0;
  const { client, calls } = stubClient((call) => {
    if (call.init.method === "POST") {
      assert.deepEqual(call.body, { target: { serverId: "srv-7" } });
      return json(operation("queued"), 202, {
        Location: "/v1/operations/op-1",
        "Retry-After": "0",
      });
    }
    polls += 1;
    if (polls === 1) return json(operation("running"));
    return json(
      operation("succeeded", {
        result: {
          changed: true,
          accountId: "acc-1",
          nodeId: "acc-1:srv-7",
          serverId: "srv-7",
          gatePort: 19001,
          egressIp: "203.0.113.7",
          sessionHealthy: true,
        },
      }),
    );
  });
  const result = await client.selectNode(
    "acc-1",
    { target: { serverId: "srv-7" } },
    { intervalMs: 1 },
  );
  assert.equal(result.changed, true);
  assert.equal(result.nodeId, "acc-1:srv-7");
  assert.equal(result.gatePort, 19001);
  assert.equal(polls, 2);
  assert.equal(calls.filter((c) => c.init.method === "GET").length, 2);
});

test("selectNode returns changed:false for an already-current 200", async () => {
  const { client } = stubClient(() =>
    json({
      changed: false,
      accountId: "acc-1",
      serverId: "srv-7",
      sessionHealthy: true,
    }),
  );
  const result = await client.selectNode("acc-1", { target: { serverId: "srv-7" } });
  assert.equal(result.changed, false);
});

test("selectNode maps a failed operation to OperationFailure", async () => {
  const base = {
    id: "op-2",
    type: "account.connect",
    accountId: "acc-1",
    target: { serverId: "srv-7" },
    requestedBy: "admin",
    createdAt: "",
    startedAt: "",
    finishedAt: "",
    steps: [],
  };
  const { client } = stubClient((call) => {
    if (call.init.method === "POST") {
      return json({ ...base, state: "queued", result: null, error: null }, 202, {
        Location: "/v1/operations/op-2",
      });
    }
    return json({
      ...base,
      state: "failed",
      result: null,
      error: { code: "LOGIN_REQUIRED", message: "login first", details: { accountId: "acc-1" } },
    });
  });
  await assert.rejects(
    client.selectNode("acc-1", { target: { serverId: "srv-7" } }, { intervalMs: 1 }),
    (err) => {
      assert.ok(err instanceof OperationFailure);
      assert.equal(err.operationId, "op-2");
      assert.equal(err.code, "LOGIN_REQUIRED");
      assert.equal(err.details.accountId, "acc-1");
      return true;
    },
  );
});

test("selectNode recovers the operationId from a 504", async () => {
  const { client } = stubClient((call) => {
    if (call.init.method === "POST") {
      return json(
        {
          error: {
            code: "OPERATION_TIMEOUT",
            message: "operation did not finish within timeoutSeconds",
            details: { operationId: "op-9" },
          },
        },
        504,
      );
    }
    return json({
      id: "op-9",
      type: "account.switch",
      state: "succeeded",
      accountId: "acc-1",
      target: { serverId: "srv-7" },
      requestedBy: "admin",
      createdAt: "",
      startedAt: "",
      finishedAt: "",
      steps: [],
      result: { changed: true, accountId: "acc-1", serverId: "srv-7", sessionHealthy: true },
      error: null,
    });
  });
  const result = await client.selectNode(
    "acc-1",
    { target: { serverId: "srv-7" }, wait: true },
    { intervalMs: 1 },
  );
  assert.equal(result.changed, true);
});

test("waitOperation times out with a typed error", async () => {
  const { client } = stubClient(() =>
    json({
      id: "op-3",
      type: "account.switch",
      state: "running",
      accountId: "acc-1",
      target: {},
      requestedBy: "admin",
      createdAt: "",
      startedAt: "",
      finishedAt: "",
      steps: [],
      result: null,
      error: null,
    }),
  );
  await assert.rejects(
    client.waitOperation("op-3", { intervalMs: 1, timeoutMs: 40 }),
    (err) => {
      assert.ok(err instanceof OperationTimeoutError);
      assert.equal(err.operationId, "op-3");
      return true;
    },
  );
});

test("streamEvents parses comments, multi-line data and CRLF", async () => {
  const encoder = new TextEncoder();
  const frames = [
    ": keepalive\n\n",
    'id: evt-1\nevent: lease.created\ndata: {"id":"evt-1",\n',
    'data: "type":"lease.created","ts":"2026-10-26T00:00:00Z",\n',
    'data: "data":{"leaseId":"lease-1"}}\n\n',
    'id: evt-2\r\nevent: lease.revoked\r\n',
    'data: {"id":"evt-2","type":"lease.revoked","ts":"","data":{}}\r\n\r\n',
  ];
  const { client, calls } = stubClient(() => {
    const body = new ReadableStream({
      start(controller) {
        for (const frame of frames) controller.enqueue(encoder.encode(frame));
        controller.close();
      },
    });
    return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
  });
  const events = [];
  for await (const event of client.streamEvents({ since: "evt-0", lastEventId: "evt-0" })) {
    events.push(event);
  }
  assert.equal(events.length, 2);
  assert.equal(events[0].id, "evt-1");
  assert.equal(events[0].type, "lease.created");
  assert.equal(events[0].data.leaseId, "lease-1");
  assert.equal(events[1].id, "evt-2");
  assert.equal(calls[0].init.headers.Accept, "text/event-stream");
  assert.equal(calls[0].init.headers["Last-Event-ID"], "evt-0");
});

test("streamEvents throws a typed error without leaking the token", async () => {
  const { client } = stubClient(() =>
    json({ error: { code: "UNAUTHORIZED", message: "bad token" } }, 401),
  );
  await assert.rejects(
    (async () => {
      for await (const _event of client.streamEvents()) {
        // no events
      }
    })(),
    (err) => {
      assert.ok(err instanceof ApiError);
      assert.equal(err.code, "UNAUTHORIZED");
      assert.equal(err.message.includes(token), false);
      return true;
    },
  );
});
