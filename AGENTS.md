# Repository Guidelines

Local usage tracker for coding agents. It reads the session/telemetry files that
**Claude Code, Codex, OpenCode, Pi, omp and Freebuff** already write, normalizes them into
its own SQLite database, and serves a dashboard from that database.

One Go binary, two shapes:
- default build → native desktop window (Wails v3 webview)
- `-tags server` → headless HTTP server serving the identical embedded frontend

## Architecture & Data Flow

Layered, dependencies point inward. No globals; everything is constructor-injected.

```
main.go (composition root)
  └─ internal/service   Wails binding surface (6 structs, JSON-only signatures)
       └─ internal/sync      Scheduler + Engine: scan loop, rollups, reporting
            ├─ internal/harness   per-agent Adapters, file/DB parsing
            ├─ internal/pricing   cost resolution + catalog sync (models.dev / LiteLLM)
            └─ internal/store     SQLite schema + every read/write path
       └─ internal/config        env defaults + mutable runtime config
internal/version                 build stamp (leaf)
```

End-to-end:

1. **Sources** — agent-written files/DBs: `~/.claude/projects/**/*.jsonl`,
   `~/.codex/sessions` (+ `state_5.sqlite`), `~/.local/share/opencode/opencode*.db`,
   `~/.pi/agent/sessions`, `~/.omp/stats.db` + `~/.omp/agent/sessions`,
   `~/.config/manicode/projects/**/chats/*/log.jsonl`,
   `~/.config/freebuff-desktop/projects/*/desktop-v2.db`; plus extra roots from the
   `scan_roots` table merged by `harness.ResolveRoots`.
2. **Parse** — each `harness.Adapter.Scan` walks files newest-first, prefiltering lines by
   byte marker (e.g. `claudeMarker = {"usage","assistant"}`), JSON-decoding into fixed
   whitelist structs. **Never `map[string]any`** — prompt/response text is never read
   (privacy contract in `internal/harness/harness.go`). Emits `Batch{Events,Sessions,States,Deletes,…}`.
3. **Incremental cursor** — `scan_state` row per file (`file:<path>`) or foreign DB
   (`sql:<harness>:<path>`) holding `mtime,size,inode,offset,watermark,parser_version`.
   Unchanged signature → skip; new inode/shrunk file → restart at 0; else resume at offset.
   Bumped `ParserVersion()` forces a full re-scan (`ClearHarness`).
4. **Cost** — harness-reported `CostUSD` wins (`reported`), else `catalog.Resolve(...)` →
   `estimated` or `unavailable` (NULL cost, **not** 0).
5. **Store** — batched multi-row upsert on `event_key` (keeps the larger total); returns
   `DirtyKeys{Days,Sessions}`; `RefreshRollups` runs after each harness.
6. **Rollups** — `rollup_daily_hm`, `rollup_daily_hp`, `rollup_session` precomputed;
   rebuilt per dirty day, or `RebuildAllRollups` when dirty days > 60 or
   `RollupsIntact` detects drift.
7. **Service → UI** — every exported service method becomes a typed JS binding;
   `frontend/src/api/*.ts` wraps them and normalizes Go `nil` slices to `[]`.
   UI updates are **push, not polling**: `data:changed` bumps `sync.dataVersion` in
   `stores/sync.ts`, which triggers refetch in `composables/useLiveQuery.ts`.

### Sync events (`internal/service/service.go` ↔ `frontend/src/api/sync.ts` `EVENTS`)

`sync:state`, `sync:progress`, `sync:done`, `sync:error`, `data:changed`,
`pricing:synced`, `settings:saved`, `sleep:status`. Keep both sides in sync when adding one.
(`sleep:status` is emitted by the sleep controller, not the sync engine.)

### Store internals worth knowing

- Writer: 1 connection, WAL, `busy_timeout(5000)`. Reader: 4-conn pool, `mode=ro&query_only`.
  Concurrency is handled at the SQLite level — there is no Go mutex around SQL.
- `store.DTOs` (`dto.go`) carry camelCase JSON tags and are the only serialized types;
  entity structs (`Event`, `Session`, `ScanState`) are exported **without** tags.
- `RangeQuery{fromMs,toMs,…}` is the single filter struct: `FromMs` inclusive,
  `ToMs` exclusive, `0` = unbounded.
- `query.go::pickSource` chooses rollup vs `usage_events` and refuses rollups when the
  filter includes outcomes/costSources/sessionIds or a partial-day range.

## Key Directories

| Path | Purpose |
|---|---|
| `main.go`, `window_desktop.go`, `window_server.go` | composition root; build-tagged window openers (`//go:build !server` / `server`) |
| `cmd/scan-report/` | CLI: one full scan into a throwaway DB, prints per-harness table or JSON |
| `internal/harness/` | one file per agent adapter (`claude.go`, `codex.go`, `opencode.go`, `pi.go`, `omp.go`, `freebuff.go`, `freebuffdesktop.go`) + shared `sink.go`, `jsonl.go`, `registry.go`, `xtsqlite.go` |
| `internal/store/` | `schema.go` DDL, `db.go` handles/DSN, `query.go` reads, `rollups.go`, `dto.go` |
| `internal/sync/` | `engine.go`, `scheduler.go`, `report.go` |
| `internal/sleep/` | keep-awake controller + per-OS inhibitors/sleepers/idlers and one-shot sleep/display/screensaver actions (`controller.go`, `monitor.go`, `platform_<os>.go`) |
| `internal/service/` | Wails bindings: `app/service.go`, `dashboard.go`, `events.go`, `meta.go`, `settings.go`, `sync.go` |
| `internal/pricing/` | `catalog.go` (atomic snapshot), `sync.go` (downloads), `normalize.go` |
| `internal/config/` | `config.go`, `paths.go` |
| `frontend/src/` | Vue app: `api/` (thin binding seam), `stores/`, `views/`, `components/`, `composables/`, `lib/`, `i18n/`, `styles/` |
| `frontend/bindings/` | **generated** TS bindings — never hand-edit |
| `build/` | Wails Taskfiles per platform, icons, Dockerfiles, packaging config |
| `docs/screenshots/` | dashboard screenshots referenced by `README.md` |

## Development Commands

Tasks run through Taskfile; `wails3` wraps them, so both `wails3 task <name>` and
plain `task <name>` (Taskfile CLI) work.

```bash
task dev            # desktop watch mode: builds DEV=true, runs vite on 127.0.0.1:9245, launches app
task build          # production desktop binary -> bin/agents-dashboard
task run            # build DEV=true and launch
task build:server   # headless binary -> bin/agents-dashboard-server (-tags server,production)
task run:server     # build DEV=true + run the server
task build:docker   # build build/docker/Dockerfile.server image
task run:docker     # docker run --rm -p 8080:8080
task package        # platform packaging (dmg / nsis|msix / appimage+deb+rpm+aur)

go run ./cmd/scan-report                 # CLI scan report
go run ./cmd/scan-report -json           # machine-readable
go test ./...                            # full Go suite (~1.3s)
go test ./internal/store -run Rollup -v  # single package / test
```

Cross-compilation goes through the `GOOS` var: `task build GOOS=windows`,
`task build GOOS=linux ARCH=arm64`. There is no `dev` dispatch — dev mode is desktop-only.

Frontend alone (rarely needed; the Taskfile drives it in-tree):

```bash
cd frontend && pnpm install && pnpm dev   # vite, port 9245, strictPort
cd frontend && pnpm build                 # vue-tsc && vite build --mode production
```

## Git & Commits

A change is not finished until it is committed. After completing a work unit, stage the files
it touched and commit them in the same turn — never leave finished work sitting in the tree.

- Subject: `<type>: <imperative summary>`, lowercase, no trailing period, ≤ 72 characters.
  Types in use: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `build`.
- Body when the change is not self-evident: bullets naming the files/symbols touched and why,
  wrapped near 80 columns. No tool/assistant attribution lines, no emoji.
- One commit per work unit. Unrelated edits get their own commit; never fold a drive-by
  refactor into a feature commit.
- Stage by path (`git add README.md docs/`). Never `git add -A` / `git commit -a` while the
  tree holds unrelated edits, and never commit `bin/`, `frontend/dist/`, `frontend/node_modules/`,
  `.task/` or `*.db*` (all gitignored — a `-f` add is always wrong here).
- Check `git status` before committing so nothing unintended is staged. Do not amend, rebase
  or force-push shared history unless asked to.

## Code Conventions & Common Patterns

### Go

- Packages are single lowercase words (`config`, `store`, `harness`, `pricing`, `sync`,
  `service`); files are lowercase with no separator (`rollups.go`, `scanstate.go`).
- `internal/sync` is imported as `syncengine` (collides with stdlib `sync`).
- Constructors are `New<Thing>(deps)`; adapters are unexported (`newPiAdapter(home)`) and
  exposed only through the single `harness.Adapter` interface.
- **DI**: constructor injection only. `service.Deps{DB,Cfg,Engine,Scheduler,Catalog,Log,Emit}`
  is one shared struct passed to all six `New*Service` funcs. `Engine`/`Catalog` take
  `*store.DB` per call, not at construction. The emitter is injected late
  (`Engine.SetEmitter`) because `*application.App` does not exist until services register.
- **Service bindings constraint** (`service/service.go` package doc): every exported method
  becomes a JS binding, so parameters/returns must be JSON-serializable — no `time.Time`,
  no `interface{}`, no maps of structs. Return `(Value, error)`; errors as plain Go `error`
  (Wails converts to a rejected promise). Reads use `context.Background()` because the
  binding carries no ctx; use `Deps.loc()` for timezone.
- **Errors**: wrap with `%w` and a lowercase verb phrase naming the operation
  (`fmt.Errorf("insert %d events: %w", len(evs), err)`). One sentinel:
  `sync.ErrAlreadyRunning`. No custom error types. Degrade, don't fail, at trust
  boundaries: locked/unreadable foreign DB → `harness.IsBusy(err)` → return nil, cursor
  kept, retry next sync. Errors the UI must see are **returned**, not logged.
- **Logging**: `log/slog` only, injected via constructor/`Deps`, string key pairs
  (`logger.Warn("cannot load price catalog", "error", err)`). Never `slog.Default()`
  directly except as the nil fallback.
- Constants: exported for cross-package use (`CostSourceEstimated`, `GranDay`, `DimHarness`,
  `EventSyncState`, `SettingTZ`, `SourceModelsDev`); unexported for tuning knobs
  (`batchEvents`, `insertRowsPerStatement`, `fullRebuildThreshold`, `maxHourBuckets`,
  `<x>Marker`).
- Build tags: only `window_desktop.go` / `window_server.go` and
  `harness/inode_{unix,other}.go` matter to app code. `production` is a Wails/Taskfile flag only.

### Frontend (Vue 3 + TS)

- Every SFC: `<script setup lang="ts">` + `<template>` + `<style scoped>`; one component
  per file, PascalCase, imported with `@/…` and an explicit `.vue` extension.
- Props: `defineProps<{…}>()` / `withDefaults(...)`; emits: type-literal tuples
  (`defineEmits<{ "update:modelValue": [string[]] }>()`); `v-model` ⇄ `modelValue`.
- Stores: Pinia **setup-style**, ids lowercase singular (`"filters"`, `"sync"`, `"settings"`,
  `"meta"`). Computed, then actions.
- `@bindings/...` is imported **only** inside `frontend/src/api/*.ts` — that directory is the
  single seam over generated `$Call.ByID` RPC; it also normalizes `null` → `[]`/defaults.
  Views and components never touch bindings directly.
- Live data: `useLiveQuery(fetcher, initial, extraDeps)` — debounced 400 ms, race-guarded by
  `requestId`, refetches on `filters.query` change and on `sync.dataVersion` (extra deps for
  view-local params like pagination). Writes call `api.*` actions directly.
- Charts: only through `components/charts/ChartBox.vue`; colors are CSS vars resolved at draw
  time via `lib/chart.ts`, and the chart is destroyed/rebuilt on theme change.
- Styling: CSS custom properties in `styles/variables.css` (dark in `:root`, light overrides
  under `:root[data-theme="light"]`), switched by one `data-theme` attribute on `<html>`
  (`lib/theme.ts`). Semantic classes (`.card`, `.num`, `.grid-4`, …) live in
  `global.css` / `components.css`; component styles stay layout-only. No utility framework.
- Formatting: all token/USD/ms/date formatting goes through `lib/format.ts`, and every date
  helper takes an explicit timezone (`settings.timezone`) — never rely on the host zone.
- i18n: `vue-i18n` composition mode, flat JSON in `src/i18n/{en,vi}.json` with dotted keys.
  Locale is DB-backed (`settings.locale`), not localStorage. New user-facing strings belong
  in both files; existing hardcoded strings are known debt (`stores/sync.ts` `statusLabel`,
  `lib/format.ts` relative-time words, several component messages).
- `HeaderControls.vue` uses `as never` on `settings.update(...)` to escape the strict types —
  don't copy that pattern; prefer adding the field to the generated `SettingsPatch` model.

## Important Files

| File | Why it matters |
|---|---|
| `main.go` | wiring order: config → logger → store → catalog → engine → scheduler → `service.Deps` → 6 services → event registration → window → `app.Run()` |
| `internal/service/service.go` | `Deps`, event-name consts, binding contract |
| `internal/store/schema.go` | `schemaVersion = 1`, DDL applied via `PRAGMA user_version` — bump for migrations |
| `internal/store/dto.go` | every JSON DTO crossing to the UI |
| `internal/harness/harness.go` | `Adapter` interface + `Event`/`Batch` shapes and the privacy contract |
| `internal/harness/registry.go` | harness registration, `ResolveRoots`, `AllWithHome` (test entry) |
| `internal/sync/engine.go` | per-run flow, rollup repair, event emission |
| `internal/sync/scheduler.go` | idle/burst adaptive loop, `TriggerNow` |
| `internal/config/config.go` | env keys + defaults, `Mutable`, `Snapshot/Apply`, `Warnings` |
| `window_desktop.go` | desktop window + the menu-bar tray (build-tagged `!server`); the tray polls the sleep status and today's all-token totals, and carries the sleep-now/display/screensaver actions |
| `windowclose_{darwin,other}.go` | macOS close-to-menu-bar: cancels `WindowClosing`, hides the window and switches to the accessory activation policy (no Dock icon) via cgo; no-op elsewhere |
| `frontend/src/api/sync.ts` | `EVENTS` map mirroring Go event names |
| `frontend/src/composables/useLiveQuery.ts` | the only data-fetch pattern in views |
| `frontend/vite.config.ts` | port 9245, `@` and `@bindings` aliases, wails plugin |
| `Taskfile.yml` + `build/Taskfile.yml` | the real build definition (root README is stale) |

## Runtime/Tooling Preferences

- **Go 1.26.0** (`go.mod`). SQLite is `modernc.org/sqlite` — pure Go, **no CGO needed**.
  Wails is `github.com/wailsapp/wails/v3 v3.0.0-beta.18`; the `wails3` CLI must be on PATH.
- **pnpm** is the package manager (`frontend/pnpm-lock.yaml`, `PACKAGE_MANAGER` var defaults
  to `pnpm`). No Node/pnpm version is pinned; `frontend/.npmrc` sets `minimum-release-age=10080`.
- Vite 8, Vue 3.5, TypeScript 5.9 (strict), `vue-tsc` runs **before** every `vite build`, so
  type errors fail the build. `tsconfig` keeps `noImplicitAny: false`,
  `noUnusedParameters: false`.
- Generated vs committed: `frontend/bindings/**` is committed but generated by
  `task common:generate:bindings` (`wails3 generate bindings … -ts -i ./...`) — regenerate
  after changing any exported service/store type. `bin/`, `frontend/dist`, `frontend/node_modules`,
  `.task/`, `*.db*`, mobile `gen/`+`overlay.json` are gitignored.
- Version stamping is documented (`internal/version/version.go`, `-ldflags -X …Version`) but no
  task passes it; builds report `dev`.
- **Absent tooling** (don't look for it): no linter/formatter config for Go or TS, no
  `.editorconfig`, no `.gitattributes`, no CI, no pre-commit hooks, no `LICENSE` file.
  `gofmt`/`go vet` conventions apply by default; there is no enforcement.
- Known inconsistencies: `build/windows/Taskfile.yml` MSIX path references a non-existent
  `wails.json`; `build/config.yml` still carries template identity (`My Company` / `0.0.1`).

## Testing & QA

- **Go**: stdlib `testing` only (no testify, despite the stale `go.sum` entry). 11 test files
  across 5 packages; run everything with `go test ./...`.
- Tests live beside their package in the **same** package (internal tests, e.g. `package store`).
  No `testdata/`, no golden files, no shared `testutil` package — helpers are lowercase and
  duplicated per package.
- Fixtures are built at runtime in `t.TempDir()`: write a fake home tree of JSONL files, or
  create a real SQLite fixture with `sql.Open("sqlite", …)` (`ompStatsFixture`,
  Codex/OpenCode thread fixtures). Network is faked with `httptest.NewServer` plus overriding
  the unexported `modelsDevURL` / `liteLLMURL` catalog fields. Assertions are plain
  `if`/`t.Errorf`. No `t.Parallel()`.
- `internal/pricing/live_test.go` is the only opt-in network test: skipped under `-short`
  unless `AGENTS_DASHBOARD_LIVE_PRICING` is set.
- Untested: `internal/service`, `internal/version`, `cmd/scan-report`, root `main.go`.
- **Frontend has no tests, no linter, no formatter** — `vue-tsc` (type-check) is the only
  automated check, and it only runs as part of `pnpm build` / `pnpm build:dev`.
- No CI and no coverage gates. When adding a test, prefer a temp-dir fixture + behavior
  assertion in the package under test, mirroring the existing harness adapter tests.

### Manual QA

Desktop features are best verified with `task dev` (hot reload for both sides) or
`task run`; the headless path with `task run:server` (defaults `localhost:8080`, override via
`AGENTS_DASHBOARD_SERVER_HOST` / `AGENTS_DASHBOARD_SERVER_PORT`).