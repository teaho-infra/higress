# ai-quota-walmart 1.0.0-alpha

Independent Higress Go WASM extension. Omitting `dimensions` selects the unchanged upstream ai-quota handlers; configured dimensions enable the new ledger. `plugin.wasm` is a real Go 1.26 WASI reactor, built with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm`.

## Configuration

```yaml
dimensions: [user, app]
admin_consumer: quota-admin
admin_path: /quota
redis_key_prefix: wmt_quota:
redis:
  service_name: redis.default.svc.cluster.local
  service_port: 6379
  timeout: 2000
enable_path_suffixes: [/v1/chat/completions, /v1/messages]
tiers:
  L1: {quota: 500000000, models: []}
  L2: {quota: 1500000000, models: []}
```

Also accepts the document's two dimension objects: user `name/user`, `enabled/true`, `subject_source/request_header`, `subject_key/x-codebuddy-user-id`; app `name/app`, `enabled/true`, `subject_source/consumer`. Both dimensions are required. Per-dimension Redis prefix fields are not used: the single configured namespace stores atomic multi-account ledgers. The default period is 14 days anchored 2026-01-05T00:00:00+08:00; money budgets can change the period through the admin API described below.

Run after KeyAuth. `x-mse-consumer` must be produced by KeyAuth; `x-codebuddy-user-id` must be overwritten by a trusted identity proxy. Public untrusted user headers are not authentication. The administrator is exactly the authenticated configured `quota-admin` consumer. Never expose its key to browsers.

App defaults to L1. A user with an assigned tier greater than or equal to the app pays; otherwise the app pays. Both accounts accumulate usage/requests, only payer accumulates `spent`. `quota` and `balance` are remaining account balance, `limit` is entitlement. Inherited user snapshots expose `effective_dimension`, `effective_subject`, `effective_tier`, `effective_limit`, `effective_balance` and `app`. An empty model list or `*` allows all models.

## Management

All endpoints are under `/v1/chat/completions/quota`. GET `/meta`, `/accounts?dimension=user|app&offset=0&limit=100`, `/audit?offset=0&limit=100`, `/?dimension=user&subject=alice` (`/usage` alias). Maximum page is 200. Registry enumeration uses sorted-set ranges, never global Redis scans. Audit newest-first retains 10,000 records.

POST accepts JSON or URL-encoded form:

```json
{"dimension":"user","subject":"alice","app":"demo-app","tier":"L2","ticket_id":"approval-123","period":"2026-11-09"}
```

- `/assign`: persistent base assignment and immediate tier adjustment; preserves spending and addons. `app` is optional association for a user.
- `/delta`: signed integer `value`, current-period entitlement adjustment; cannot produce negative limit.
- `/refresh`: idempotent current-period initialization; never returns spent quota.
- `/tiers`: `tiers` object in the same shape as configuration; changes future initialization only. Existing ledger entitlements remain unchanged. Later explicit tier changes use the new template, adjusting from the stored original base quota.

Amounts and cumulative counters are bounded to 9,999,999,999,999 tokens, above 32-bit range and exact through Redis Lua cjson serialization; larger inputs are rejected.

Public management routes enforce method/action allowlists and per-endpoint input fields; internal settlement/check actions and all server-owned timestamp/operator fields are rejected. Snapshot and audit serialization plus resulting-limit validation occur before any administrative write.

All mutations require current `period` and `ticket_id`. The same ticket and canonical payload returns the original result. Reuse with a different action/account/period/payload returns 409. Tickets persist with the namespace. Responses use `{ok:true,...}` or `{ok:false,error:"ai-quota-walmart.CODE",message:"..."}`. No quota is 403/noquota; Redis failure is 503/redis_unavailable. Window rate limiting remains a separate plugin returning 429.

## Atomicity and boundaries

One Redis Lua operation initializes/checks both dimensions, settles both usage counters, or applies/audits/idempotently records an administrative change. Persistent assignments are separate from period ledgers. Each accepted completion carries its original period and payer, so late completion is charged there. A new period drops temporary quota additions and starts usage at zero. Redis TIME validates the period boundary within the atomic operation. Use a standalone Redis deployment: dynamically accessed keys are not Redis Cluster hash-slot compatible.

Charging is based on upstream usage after response, including streaming usage handled by upstream tokenusage parser. This is **not a pre-reservation**: concurrent requests can exceed remaining quota before settlement. Missing usage is not charged. Settlement failures after response cannot revoke already delivered tokens; requests fail closed when Redis is unavailable during admission. There is no durable settlement retry queue. Automatic downgrade after two low-use periods is not implemented; retain period ledgers and implement a separately reviewed scheduled policy. Ledger/registry/ticket storage has no automatic retention; operators must plan archival. Audit retention is bounded separately.

## Verification

`ledger_test.go`: anchored boundaries, integer validation, reserved field/action rejection. `legacy_test.go`: copied upstream ai-quota regression suite, official source unchanged. `integration_test.py`: actual Redis EVAL against a disposable local Redis, unique prefix and cleanup, 22 cases covering dual dimensions, equal-tier selection, 3-billion usage, refresh, ticket conflict, repeated settlement, rollover, late response, template scope and bounded enumeration.

```sh
docker run -d --rm --name walmart-quota-plugin-test -p 127.0.0.1:16389:6379 redis:7-alpine
python3 integration_test.py
go test ./...
GOTOOLCHAIN=go1.26.0 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm
docker stop walmart-quota-plugin-test
```

Local verification logs are ignored: `tdd-failing.log`, `test-results.log`, `redis-integration-results.log`, `build-results.log`. Initial red test invocation was blocked by a corrupt preexisting shared uuid module cache; a fresh isolated `GOMODCACHE=/tmp/walmart-quota-gomod` restored dependency integrity. The upstream module and existing cache were not changed.

## Opt-in personal money budgets

Set `budget_policy: personal_department_money` with a dedicated `redis_key_prefix`
(e.g. `wmt_portal_money:`). Do not reuse a token ledger prefix. Omitted policy
preserves the legacy modes. Default budgets are CNY 500/1500.
Amounts and prices use integer micro-yuan; one yuan is 1,000,000 micro-yuan.
`prices` maps model names to `input_price_micros` / `output_price_micros`, per
million tokens. Missing prices reject requests; explicit zero is free.

Administrators assign department templates with `dimension: department`, then
assign users with `tier: inherit, department: <id>` or an explicit personal tier.
Personal tiers always win. Each user has an independent budget. Apps only
aggregate tokens and cost. Assignment replaces the current temporary tier while
preserving consumed cost and additive adjustments. Department/template edits
apply at next initialization/period; query/list do not initialize accounts.

Admission freezes the user, period and prices; settlement combines input/output
cost exactly with Go big.Int and rounds up once. Concurrent in-flight requests
can exceed the remaining balance (there is no reservation). Responses without
recognized usage remain uncharged; a settlement storage failure is logged but
has no durable retry queue. The caller's department header is never consulted.
GET/POST `prices` reads/replaces the complete map using the usual period/ticket
contract. User mutation actions and department assignment retain audit and
idempotency records. Do not expose the admin key to clients.

Money mode `/meta` returns `period_days` and `next_period_days`. POST `/cycle`
with `{ "days": 7, "period": "<meta.period>", "ticket_id": "<unique>" }`
sets the next cycle to 7 days (1–365 whole days); it does not change the
current ledger. POST `/reset` with the current `period` and a unique
`ticket_id` immediately opens a new cycle. Optional `start_date` and
`end_date` (both `YYYY-MM-DD`) select its inclusive calendar dates in
Asia/Shanghai; the selected range must include today and span 1–365 days.
Without dates, reset uses the pending length starting immediately. After a
selected range ends, following cycles use the configured next-cycle length.
Previous period ledgers remain available for late settlement and audit, while
new requests start with fresh user balances. Both actions require the admin
consumer and are recorded in audit. Cycle configuration is stored under the
money namespace in Redis, so its persistence follows Redis persistence.

A namespace policy marker prevents accidentally reusing an existing token ledger.
Unmarked namespaces containing ledger/configuration keys are rejected; deploy
into a fresh prefix. Overflowed settlements latch the original user/app for
that period and reject further admissions pending administrator reconciliation;
reads and audit remain available. This also applies to costs above the supported
amount range. Prices share the supported 0..9,999,999,999,999 integer range.
