# Sinau

Small mentor/learner progress tracker.

## Run

Development:

```sh
go run ./cmd/sinau
```

Open `http://127.0.0.1:8080`.

Build and run the binary:

```sh
mkdir -p bin
go build -trimpath -ldflags="-s -w" -o bin/sinau ./cmd/sinau
./bin/sinau
```

Environment:

- `SINAU_ADDR`, default `127.0.0.1:8080`
- `SINAU_DB`, default `data/sinau.db`
- `SINAU_TEMPLATES`, default `templates`
- `SINAU_STATIC`, default `static`
- `SINAU_SECURE_COOKIE`, set `true` behind HTTPS
- `SINAU_REMINDERS`, set `false` to disable deadline reminder worker
- `SINAU_REMINDER_INTERVAL`, default `1h`
- `SINAU_REMINDER_WINDOW`, default `24h`

Deployment notes are in [DEPLOYMENT.md](DEPLOYMENT.md).

## Code Layout

- `cmd/sinau`: binary entry point and configuration wiring
- `internal/auth`: password hashing, tokens, validation helpers
- `internal/domain`: shared domain structs and role constants
- `internal/store`: SQLite migrations, queries, and persistence rules
- `internal/web`: HTTP routes, middleware, handlers, and rendering
- `templates`: server-rendered HTML
- `static`: CSS and vendored htmx

## Model

- First run creates the first mentor and first room.
- Mentors create invite codes for learners or other mentors.
- Learners submit progress reports and links to docs, PDFs, Drive, Colab, or repos.
- Mentors comment on reports and assign tasks.
