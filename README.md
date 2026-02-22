# sticky-refinery

Directory-scanning media converter daemon built on [sticky-overseer v2](../sticky-overseer).

It watches glob-pattern paths for files, converts them with a configurable command (typically ffmpeg), and tracks every file's lifecycle in SQLite. The WebSocket API (provided by sticky-overseer) exposes live status and allows manual task submission.

## Quick start

```bash
# Build
make build                           # → dist/sticky-refinery

# Run
./dist/sticky-refinery -config config.yaml

# Health check
curl http://localhost:8080/openapi.json
```

## Docker

```bash
make -C docker build                 # sticky-refinery:$(VERSION) + :latest

# Playground with sticky-bb UI (http://localhost:9090)
docker compose -f docker/compose-bb.yaml up
```

Environment variables override config when set:

| Variable          | Default              | Description            |
|-------------------|----------------------|------------------------|
| `OVERSEER_LISTEN` | `:8080`              | WebSocket bind address |
| `OVERSEER_DB`     | `/data/refinery.db`  | SQLite database path   |
| `OVERSEER_LOG_FILE` | _(stdout)_         | Log file path          |

## Configuration

sticky-refinery uses the sticky-overseer native YAML format. See `config.example.yaml` for a complete working example.

```yaml
name: "sticky-refinery"       # used in OpenAPI spec title
listen: ":8080"
db: /data/sticky-refinery.db

task_pool:
  limit: 4                    # max concurrent workers
  queue:
    enabled: true
    size: 100                 # max queued tasks

actions:
  <action-name>:
    type: converter           # the only built-in action type
    task_pool:                # overrides global pool for this action
      limit: 4
    dedupe_key: ["file"]      # prevents double-queuing the same file path
    config:
      # --- required ---
      paths:                  # doublestar glob patterns
        - "/recordings/**/*.ts"
      target:
        format: "{{.File.Dir}}/{{.File.Basename}}.mp4"
      command: "ffmpeg -y -i {{.Input}} -c:v libx264 {{.Output}}"

      # --- optional ---
      scan_interval: "30s"    # default 30s
      direction: "oldest"     # "oldest" | "newest"  (default "oldest")
      min_age: "5m"           # skip files younger than this (default: no limit)
      max_age: null           # skip files older than this (default: no limit)
      delete_on_success: false
      db_path: "/data/sticky-refinery.db"   # default "sticky-refinery.db"
      target:
        regex: "^(?P<base>.+)\\.ts$"        # optional named-capture groups
        format: "{{.File.Dir}}/{{base}}.mp4"
```

### Template variables

#### `target.format` — output path

| Variable | Example value | Description |
|----------|--------------|-------------|
| `{{.File.Dir}}` | `/recordings/cam1` | Input file directory |
| `{{.File.Name}}` | `stream.ts` | Input filename (base + ext) |
| `{{.File.Basename}}` | `stream` | Input filename without extension |
| `{{.File.Ext}}` | `.ts` | Input file extension (with dot) |
| `{{.base}}` | `stream` | Named capture group `(?P<base>...)` from `target.regex` |
| `{{.<group>}}` | _(varies)_ | Any other named capture group |

`target.regex` is applied to the **filename only** (not full path). Named capture groups become top-level template variables.

#### `command` — ffmpeg (or any) command line

All `target.format` variables, plus:

| Variable | Description |
|----------|-------------|
| `{{.Input}}` | Full input file path |
| `{{.Output}}` | Rendered output path |
| `{{.Extra}}` | JSON-encoded extra metadata (from `pipeline_config` table) |

Quoted strings and backslash escapes are honoured when splitting the rendered command into argv.

### File status lifecycle

```
queued → in_flight → completed
                   ↘ errored → (retry via UpsertQueued)
         paused
```

Files are never re-submitted once `completed`. `errored` and `paused` files are re-queued on the next scan cycle.

## WebSocket API

sticky-overseer exposes a WebSocket at `/ws`. Send JSON messages:

```json
{"type": "list"}
{"type": "start", "action": "ts-to-mp4", "params": {"file": "/recordings/x.ts"}}
{"type": "stop",  "task_id": "<uuid>"}
```

See the live OpenAPI spec at `http://localhost:8080/openapi.json` or use sticky-bb for a UI.

## Build targets

```
make build     compile to dist/sticky-refinery
make install   install to $(PREFIX)/bin  (default /usr/local)
make test      go test -race ./...
make clean     rm -rf dist/
```

Version and commit hash are injected at build time via `-ldflags`.
