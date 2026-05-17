# Architecture

A reference for the technical shape of Sinau — what each package is for,
how data flows, and why some decisions are the way they are. If you're
looking for a product overview, start with [README.md](README.md). For
running it in production, see [DEPLOYMENT.md](DEPLOYMENT.md).

## At a glance

```
┌────────────────────────────────────────────────────────────────┐
│ Browser  ↔  Nginx  ↔  sinau (Go HTTP server, single process)   │
│                          │                                     │
│                          ├── SQLite (WAL, single writer)       │
│                          ├── html/template + htmx              │
│                          └── Reminder worker (goroutine)       │
│                                  ↓                             │
│                              Notifiers (log, email, whatsapp,  │
│                                         telegram)              │
└────────────────────────────────────────────────────────────────┘
```

One Go binary. SQLite for storage. No JS build, no npm, no separate
database process, no container required.

## Package layout

| Path                | Responsibility                                                  |
|---------------------|-----------------------------------------------------------------|
| `cmd/sinau`         | Entry point. Reads env, opens store, builds web server, starts reminder worker, handles SIGTERM. |
| `internal/auth`     | argon2id password hashing, secure random tokens, input validation helpers. |
| `internal/domain`   | Pure structs and constants shared between layers. Role/mode validation. No I/O. |
| `internal/store`    | SQLite migrations + every SQL query in the app. The only package that imports `database/sql`. |
| `internal/web`      | HTTP routes, middleware (CSRF, rate limit, auth, security headers), handlers, template rendering. |
| `internal/reminder` | `Notifier` interface, channel implementations (log, email, whatsapp stub, telegram stub), the `Worker` loop. |
| `templates/app.html`| Single file holding every server-rendered view as named templates. |
| `static/`           | CSS (hand-rolled, design tokens), vendored htmx, SVG favicon. |

## Data model

```
users ─┬─< sessions
       ├─< memberships >─ rooms ─< invites
       ├─< reports ─< comments
       ├─< tasks (assigned_to, assigned_by)
       ├─< submissions (assignment_id, student_id)
       ├─< notification_prefs
       └─< points_ledger
       
rooms ─< assignments ─< submissions
```

Key tables:

- **users** — account, password hash, `can_create_rooms` flag.
- **sessions** — `id_hash` is sha256(token); the raw token lives only in the cookie.
- **rooms** — `mode` (`mentorship` | `classroom`), `leaderboard_visible`.
- **memberships** — `(room_id, user_id)` PK, `role` (`mentor` | `mentee`).
- **invites** — single-use codes (`code_hash`) bound to a room + role.
- **reports** / **comments** — mentorship room artefacts.
- **tasks** — mentorship room artefacts. `reviewed_at` locks status changes once a mentor awards points.
- **assignments** / **submissions** — classroom room artefacts. `UNIQUE(assignment_id, student_id)` so resubmits update in place. `reviewed_at = ''` on a submission means it's awaiting review.
- **points_ledger** — append-only point awards. `UNIQUE(source, source_id)` prevents double-award races.
- **notification_prefs** — opt-in per user. Channel + contact fields (email lives on `users`, WhatsApp number and Telegram chat ID live here).

## Role model (the layered roles)

Three orthogonal concepts:

1. **Account capability** (`users.can_create_rooms`): can this account create new rooms? Bootstrap sets this on the first user; new users joining via invite never get it implicitly.
2. **Room membership** (`memberships.role`): inside a specific room, this user is a `mentor` or a `mentee`. Authoritative for room-level authorization.
3. **Room mode** (`rooms.mode`): this room runs the `mentorship` workflow or the `classroom` workflow.

The same person can be:

- A mentor in a Mentorship room → labelled "Mentor" in the UI.
- A teacher in a Classroom room → labelled "Teacher" in the UI.
- A mentee in another Mentorship room → labelled "Mentee".
- A student in another Classroom room → labelled "Student".

The data layer stores only `mentor`/`mentee`. The display layer translates per mode via `Room.RoleLabel(role)` / `Room.MyRoleLabel()` / `Room.ModeLabel()` (`internal/domain/domain.go`).

Inviting a mentor to a room grants room-level mentor powers but **not** account-level room creation. This is verified by `TestRoomMentorInviteDoesNotGrantCreateRoomsCapability`.

## Authentication

- **Passwords**: argon2id, `m=64 MiB, t=2, p=4`, 16-byte salt, base64 encoding (`internal/auth/auth.go`).
- **Sessions**: 32-byte URL-safe random token in `sinau_session` cookie (`HttpOnly`, `Secure` opt-in, `SameSite=Lax`); only `sha256(token)` is stored in the DB. 14-day expiry, replaced (not amended) on login.
- **CSRF**: per-session token also stored in `sessions` row, embedded as hidden input on every form. Compared with `subtle.ConstantTimeCompare`.
- **Login timing**: on missing email, the server still runs argon2 against a precomputed dummy hash so timing doesn't distinguish "no such user" from "wrong password".
- **Rate limit**: per-IP token bucket on `/login`, `/setup`, `/join` (~0.2/s sustained, 8 burst). `X-Real-IP` / `X-Forwarded-For` honoured when present, so it works behind nginx.

## Authorization

Every room-scoped handler calls `store.RoomAccess(roomID, userID)` first; the returned `role` is the only trusted source for downstream checks. Mode-specific handlers also check `room.Mode`:

- `createTask` — requires `role == mentor`.
- `createAssignment`, `reviewSubmission` — require `role == mentor` **and** `room.Mode == classroom`.
- `submitAssignment` — requires `role == mentee` **and** `room.Mode == classroom`.
- `updateRoomSettings`, `createInvite` — require `role == mentor`.

Two idempotency guards in the store layer protect against accidental re-edits:

- `UpdateTaskStatus` won't change the status of a reviewed task (`WHERE reviewed_at = ''`).
- `ReviewSubmission` won't overwrite a previously reviewed submission (`WHERE reviewed_at = ''`). Resubmission by the student clears `reviewed_at` so the review cycle can repeat.

## Migrations

Versioned in `schema_migrations`. Six versions today, plus one idempotent shim:

| Version | What it adds |
|---------|--------------|
| 1 | Initial schema: users, sessions, rooms, memberships, invites, reports, comments, tasks. |
| 2 | `tasks.due_date`, `tasks.last_reminded_at`. |
| 3 | `notification_prefs`, `rooms.leaderboard_visible`, `tasks.points_awarded/reviewed_at/reviewed_by`, `points_ledger`. |
| 4 | Rebuilds `notification_prefs` to drop the hard-coded channel CHECK and add `whatsapp_number` / `telegram_chat_id`. Toggles `PRAGMA foreign_keys` around the rebuild. |
| 5 | `rooms.mode`, `assignments`, `submissions`. |
| 6 | `users.can_create_rooms`, backfilled from existing memberships. |

Plus `ensureGamificationSchema()` between v3 and v4 — an idempotent shim that patches DBs upgraded from a divergent pre-rebase v3.

Three runner styles coexist:

- `applyMigration(version, []string)` — generic, fresh-install path, all statements in one tx.
- `applyMigration4` — custom because `PRAGMA foreign_keys` can't be toggled inside a tx.
- `applyMigration5` / `applyMigration6` — custom because they check column existence inside the tx (idempotent against the divergent upgrade path).

This works but is **flagged tech debt**: editing migration 1 in place to add `can_create_rooms` made the history non-truthful. A future cleanup should fold `ensureGamificationSchema` into a numbered migration and consolidate the three runners under one `applyMigrationFunc(int, func(*sql.Tx) error)` shape.

## Notifications

```
                                ┌─ off (skip)
                                │
  worker tick → due tasks → ────┼─ log (default fallback)
                                │
                                ├─ email (SMTP via net/smtp)
                                │
                                ├─ whatsapp (stub: HTTP gateway)
                                │
                                └─ telegram (stub: Bot API)
```

- Users opt in at `/settings`. Default state for everyone is `off`.
- `Notifier` interface: `NotifyTaskDue(ctx, Recipient, TaskReminder) error`.
- `Worker` holds a `map[channel]Notifier`. At dispatch time it reads each user's `notification_prefs` and routes to the right notifier. Unknown channels are logged and skipped.
- `EmailNotifier` is real (SMTP, STARTTLS, falls back to log when `SINAU_SMTP_HOST/FROM` is unset).
- `WhatsAppNotifier` and `TelegramNotifier` are **interface-ready stubs**: full config struct, `Configured()` check, fallback wiring, and TODO blocks pointing at the integration paths (`go-whatsapp-web-multidevice` REST daemon and Telegram Bot API). To finish wiring a channel, fill in the `NotifyTaskDue` body — no changes to migrations, store, worker, or web layer needed.
- Dedup: `tasks.last_reminded_at` set after dispatch — one notification per task per day across all recipients.
- Master switch: `SINAU_NOTIFICATIONS_ENABLED=false` hides the `/settings` UI, the topbar link, the `/help` section, and skips starting the worker.

## Reminder worker

In-process goroutine started by `cmd/sinau/main.go` when reminders are enabled. Cancelled by the same `signal.NotifyContext` that shuts down the HTTP server.

```go
ticker := time.NewTicker(every)             // SINAU_REMINDER_INTERVAL, default 1h
for {
    select {
    case <-ctx.Done():       return
    case <-ticker.C:         runOnce(ctx)   // scan tasks within SINAU_REMINDER_WINDOW
    }
}
```

`runOnce` queries due tasks (`DueTaskReminders`), dispatches each through the channel registry, and stamps `last_reminded_at`. Failure to mark dedup is logged but doesn't block the next iteration.

## Frontend

- Server-rendered HTML via Go `html/template`. Single file (`templates/app.html`) with named templates (`landing`, `login`, `join`, `setup`, `mentor_home`, `mentee_home`, `room`, `report`, `settings`, `help`, `invite_created`).
- Progressive enhancement via [htmx](https://htmx.org) — only used for the invite form swap (`hx-post`, `hx-target`).
- Hand-rolled CSS with design tokens (`--bg`, `--accent`, `--line`, etc.) plus mobile breakpoints at 860px and 640px.
- No JS build. No npm. htmx is vendored as a single file.
- Layered role display is centralised in three `domain.Room` methods (`RoleLabel`, `MyRoleLabel`, `ModeLabel`) so templates never render raw `mentor`/`mentee`/`classroom` strings.

## Security headers

Set by the `securityHeaders` middleware on every response:

- `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'`
- `X-Frame-Options: DENY`
- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: same-origin`

Plus invite-only product positioning: every page emits `<meta name="robots" content="noindex,nofollow">` so search engines won't index even the landing page.

## Build & test

```sh
go build ./...                    # validates every package compiles
go vet ./...                      # static checks
go test ./...                     # store + auth + web suites
go build -o bin/sinau ./cmd/sinau # produce the binary (CGO required for SQLite)
```

Test coverage focuses on the store (which encodes authorization rules) and on web-layer boundaries (classroom flow + auth boundaries + CSRF + notification gating).

## Non-goals & trade-offs

- **No horizontal scale.** `SetMaxOpenConns(1)` for SQLite means one writer at a time. Suitable for tens of concurrent users, not hundreds. If you outgrow this, swap the store package to Postgres before adding any other complexity.
- **No file uploads.** Sinau stores only links to external resources (Docs, Drive, Colab, repos). Keeps storage simple and avoids hosting user content.
- **No SSO.** Argon2 passwords + invite codes only. Adding OAuth would need a new field on `users` and a flow change at `/join`.
- **Per-instance, not multi-tenant.** One instance = one organization. There's no "owner_id" on rooms because the whole DB belongs to one operator.

## Known tech debt (carried, not blocking)

- Migration 1 was edited in-place to add `can_create_rooms`; migration 6 patches old DBs. Works, but the file no longer reads as a faithful history. Future cleanup: fold `ensureGamificationSchema` into a numbered migration and consolidate runners.
- Submission `score` is a `TEXT` field with no constraint. Inconsistent with the 1–5 task rubric. Worth unifying.
- `Assignment.MySubmissionStatus/URL/Feedback/Score` are viewer-specific fields on what should be a clean domain type.
- No pagination on `Reports` (`LIMIT 80`), `Submissions`, `Members`, `Assignments`. Acceptable for small rooms.
- Classroom-event notifications (assignment due, submission received) are not wired into the reminder system yet — only task due reminders are.
