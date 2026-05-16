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
- `SINAU_SMTP_HOST`, e.g. `smtp.example.com:587`. Empty disables email
  delivery (reminders for users who chose `email` quietly fall back to log).
- `SINAU_SMTP_USER`, `SINAU_SMTP_PASS`, `SINAU_SMTP_FROM` — SMTP auth /
  envelope.
- `SINAU_SMTP_STARTTLS`, default `true`. Set `false` for plaintext local
  relays.
- `SINAU_WHATSAPP_API_URL`, `SINAU_WHATSAPP_API_KEY` — gateway URL + key
  for the WhatsApp stub. Empty keeps the channel selectable but routes to
  log (no delivery). Designed for an external daemon like
  `aldinokemal/go-whatsapp-web-multidevice`.
- `SINAU_TELEGRAM_BOT_TOKEN`, `SINAU_TELEGRAM_API_BASE` — Bot API
  credentials for the Telegram stub. Empty token keeps the channel
  selectable but routes to log.

Users opt in to reminders themselves at `/settings`. Available channels:
`off`, `email`, `whatsapp` (preview), `telegram` (preview), `log`. WhatsApp
and Telegram have the full interface and DI plumbing in place
(`internal/reminder/whatsapp.go`, `internal/reminder/telegram.go`,
contact-info fields on `notification_prefs`, `Recipient.WhatsApp` /
`Recipient.Telegram`); only the actual HTTP call to the delivery backend
is left as a TODO. To finish wiring a channel, fill in `NotifyTaskDue` in
the stub file — no changes to the worker, store, web layer, or migration
are required.

Deployment notes are in [DEPLOYMENT.md](DEPLOYMENT.md).

## Code Layout

- `cmd/sinau`: binary entry point, env-var config, reminder notifier selection
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
