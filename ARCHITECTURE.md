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

Pre-launch history is squashed into a single migration. `schemaV1` in
`internal/store/store.go` is the entire shape of the database — every
table created in final form, no incremental ALTERs. The runner is one
generic `applyMigration(version, []string)` wrapped in a transaction.

If you need to evolve the schema after release, add `migration 2`
alongside `schemaV1`. Do not edit `schemaV1` itself — that path is
closed.

## Notifications

```
                                              ┌─ off (skip)
                                              │
  worker tick → due tasks       ─┐            ├─ log (default fallback)
                                 ├─→ dispatch ┤
                worker tick → due assignments ┘├─ email (SMTP via net/smtp)
                                              │
                                              ├─ whatsapp (stub: HTTP gateway)
                                              │
                                              └─ telegram (stub: Bot API)
```

- Users opt in at `/settings`. Default state for everyone is `off`.
- `Notifier` interface has two methods: `NotifyTaskDue(ctx, Recipient, TaskReminder)` for mentorship rooms and `NotifyAssignmentDue(ctx, Recipient, AssignmentReminder)` for classroom rooms. Adding a new channel means implementing both, plus a constant in `internal/domain`.
- `Worker` holds a `map[channel]Notifier`. At dispatch time it reads each user's `notification_prefs` and routes to the right notifier. Unknown channels are logged and skipped.
- `EmailNotifier` is real (SMTP, STARTTLS, falls back to log when `SINAU_SMTP_HOST/FROM` is unset).
- `WhatsAppNotifier` and `TelegramNotifier` are **interface-ready stubs**: full config struct, `Configured()` check, fallback wiring, and TODO blocks pointing at the integration paths (`go-whatsapp-web-multidevice` REST daemon and Telegram Bot API). To finish wiring a channel, fill in the two `Notify*Due` bodies — no changes to migrations, store, worker, or web layer needed.
- Dedup: `tasks.last_reminded_at` and `assignments.last_reminded_at` set after dispatch — one notification per task or assignment per day across all recipients.
- Notification content is localised per recipient: subject/body resolves through `i18n.T` using `users.language`.
- Master switch: `SINAU_NOTIFICATIONS_ENABLED=false` hides the `/settings` UI, the topbar link, the `/help` section, and skips starting the worker.

## Reminder worker

In-process goroutine started by `cmd/sinau/main.go` when reminders are enabled. Cancelled by the same `signal.NotifyContext` that shuts down the HTTP server.

```go
ticker := time.NewTicker(every)             // SINAU_REMINDER_INTERVAL, default 1h
for {
    select {
    case <-ctx.Done():       return
    case <-ticker.C:         runOnce(ctx)   // scan both task + assignment due windows
    }
}
```

`runOnce` makes two passes: (1) `DueTaskReminders` then per-task dispatch + `MarkTaskReminded`; (2) `DueAssignmentReminders` (fans each assignment out to all unsubmitted mentees) then `MarkAssignmentReminded` per unique assignment. Failure to mark dedup is logged but doesn't block the next iteration.

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

- `Assignment.MySubmissionStatus/URL/Feedback/Score` are viewer-specific fields on what should be a clean domain type. Refactor when the next feature touches submission shape; pure code-organisation, no user impact.
- No pagination on `Reports` (`LIMIT 80`), `Submissions`, `Members`, `Assignments`. Acceptable for small rooms; revisit if one room ever exceeds ~30 mentees with months of weekly history.
- Submission-received notifications (teacher gets pinged when a mentee submits) are not yet wired. Only deadline reminders fire today, on both sides.
