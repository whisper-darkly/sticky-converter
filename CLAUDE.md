# sticky-refinery — developer notes

## Repo at a glance

```
cmd/sticky-refinery/main.go   12 lines — just overseer.RunCLI(version, commit)
converter/handler.go          converter action type: factory + handler + service loop
internal/db/db.go             SQLite open + WAL pragmas
internal/executor/executor.go template rendering for target paths and commands
internal/scanner/scanner.go   glob file discovery with age filtering
internal/store/store.go       DAL for target_files + pipeline_config tables
```

**go.mod replace**: `sticky-overseer/v2 => ../sticky-overseer` for local dev.
Remove for release builds; tag overseer first.

## Architecture

sticky-refinery is a thin wrapper around sticky-overseer.  All concurrency, queuing,
retry, WebSocket serving, and OpenAPI generation are handled by overseer.

The binary registers one `overseer.ActionFactory` (`"converter"`) via `init()`.
`overseer.RunCLI` loads the YAML config, instantiates a handler per action entry, then
drives the event loop.

```
overseer.RunCLI()
  └─ converterFactory.Create()   one per action in config
       └─ converterHandler{actionName, cfg, store}
            ├─ Describe()         → ActionInfo{name, type, params}
            ├─ Validate(params)   → error
            ├─ Start(...)         → *Worker  (spawns ffmpeg, wraps callbacks)
            └─ RunService(ctx, submit)   background scan loop
```

## sticky-overseer interface contracts

### `overseer.ActionFactory`

```go
Type() string
Create(config map[string]any, actionName string,
       mergedRetry RetryPolicy, poolCfg PoolConfig,
       dedupeKey []string) (ActionHandler, error)
```

- `config` is the raw `config:` map from YAML, marshaled to JSON and back
- Called once per action at startup; return a non-nil error to abort startup
- Register via `overseer.RegisterFactory(&myFactory{})` in an `init()` function

### `overseer.ActionHandler`

```go
Describe() ActionInfo
Validate(params map[string]string) error
Start(taskID string, params map[string]string, cb WorkerCallbacks) (*Worker, error)
```

- `Describe` is called on every `GET /openapi.json`; keep it cheap
- `Validate` is called before `Start`; return a descriptive error on bad input
- `Start` must return quickly — the worker runs in a goroutine managed by overseer

### `overseer.ServiceHandler` (optional)

```go
RunService(ctx context.Context, submit TaskSubmitter)
```

- Implement this interface in addition to `ActionHandler` to run a background loop
- `submit.Submit(actionName, dedupeID, params)` enqueues a task as if submitted via WebSocket
- Blocks until `ctx` is cancelled

### `overseer.WorkerCallbacks`

```go
OnOutput(w *Worker, line string, isStderr bool)
LogEvent(w *Worker, event string, kvs ...KV)
OnExited(w *Worker, exitCode int, intentional bool, t time.Time)
```

Wrap with `overseer.NewWorkerCallbacks(...)` to override only the callbacks you need.

### `overseer.WorkerConfig`

```go
WorkerConfig{
    TaskID:        string,
    Command:       string,    // argv[0]
    Args:          []string,  // argv[1:]
    IncludeStdout: bool,
    IncludeStderr: bool,
}
```

## converter action config — required/optional/defaults

| Field | Required | Default | Notes |
|-------|----------|---------|-------|
| `paths` | yes | — | ≥1 doublestar glob pattern |
| `target.format` | yes | — | Go template for output path |
| `command` | yes | — | Go template; split respecting quotes |
| `target.regex` | no | `""` | Named groups → template vars |
| `scan_interval` | no | `30s` | Parsed by `time.ParseDuration` |
| `direction` | no | `"oldest"` | `"oldest"` or `"newest"` |
| `min_age` | no | 0 (no limit) | Skip younger files |
| `max_age` | no | 0 (no limit) | Skip older files |
| `delete_on_success` | no | `false` | Removes input on exit code 0 |
| `db_path` | no | `"sticky-refinery.db"` | Relative or absolute |

Startup validation errors (missing `paths`, `target.format`, `command`) abort the
process; config is never partially loaded.

## executor template contracts

### `RenderTargetPath(inputPath, regexStr, formatTmpl) (string, error)`

Template data (accessed as `{{.Key}}`):

| Key | Type | Description |
|-----|------|-------------|
| `File` | `FileVars` | See below |
| `<group>` | `string` | Named capture from `regexStr` |

`FileVars` fields: `.Dir`, `.Name`, `.Basename`, `.Ext`

- `regexStr` is matched against `filepath.Base(inputPath)` only
- Unmatched regex → capture groups absent (no template error; use `{{if .group}}`)

### `RenderCommand(cmdTmpl, inputPath, outputPath, extraJSON) ([]string, error)`

Template data:

| Key | Type | Description |
|-----|------|-------------|
| `Input` | `string` | Full input path |
| `Output` | `string` | Rendered output path |
| `Extra` | `string` | JSON-encoded `pipeline_config.extra_json` |
| `File` | `FileVars` | Same as above |

The rendered string is split by `parseArgs`: respects `"…"`, `'…'`, and `\"` `\'` `\\`.

## store contracts

**Schema** (SQLite, WAL mode):

```sql
target_files(
    path PRIMARY KEY, pipeline_name, status,
    error_count, error_message,
    queued_at, started_at, completed_at, last_attempted_at  -- RFC3339Nano UTC strings
)
pipeline_config(name PRIMARY KEY, extra_json)
```

**Status values**: `queued` → `in_flight` → `completed` | `errored` | `paused`

**`UpsertQueued` semantics**: INSERT or no-op. Only re-queues if current status is
`errored` or `paused`. Does NOT overwrite `in_flight` or `completed` rows.

**`IsCompleted` / `IsInFlight`**: return `false` on `sql.ErrNoRows` (not found = not processed).

## scanner contracts

```go
ScanAll(patterns []string, direction string, minAge, maxAge time.Duration) ([]string, error)
```

- Uses `doublestar.GlobWalk` — patterns support `**` recursive matching
- `splitPattern` finds the longest non-glob prefix as the filesystem root
- De-duplicates across overlapping patterns (by absolute path)
- Age is computed from `fs.DirEntry.Info().ModTime()` at scan time
- Zero `minAge`/`maxAge` means no limit (not "zero age")
- Sort is stable; ties preserve filesystem walk order

## Build / tags / release workflow

```bash
# Build
make build

# Tag (annotated — required)
git tag -a v0.2.0 -m "v0.2.0"
git push --follow-tags

# If overseer module isn't proxied yet:
GONOSUMDB=github.com/whisper-darkly/* GOPROXY=direct go get ...
```

Remove the `replace` directive in `go.mod` before tagging a release.

## Adding a new action type

1. Create `myaction/handler.go` in a new package
2. Define a struct implementing `overseer.ActionFactory` + `overseer.ActionHandler`
3. Optionally implement `overseer.ServiceHandler` for background work
4. Register in `init()`: `overseer.RegisterFactory(&myFactory{})`
5. Blank-import the package from `cmd/sticky-refinery/main.go`

No other files need changing. overseer wires up everything from the factory.
