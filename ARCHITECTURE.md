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
| `templates/app.html`| Single file holding every server-rendered view as named templates (`landing`, `login`, `join`, `setup`, `mentor_home`, `mentee_home`, `room`, `report`, `report_edit`, `task_edit`, `assignment_edit`, `profile`, `settings`, `help`, `onboarding`, `search`, `search_results`, `coaching`, `growth`, `grades`, `invite_created`, `link_row`, `lang_picker`). |
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

- **users** — account, password hash, `can_create_rooms`, `language`, `engagement_notif_enabled` (per-user opt-out for non-deadline notifications), `onboarded_at` (set on first dashboard visit — empty for fresh accounts so `/onboarding` can intercept the home redirect).
- **sessions** — `id_hash` is sha256(token); the raw token lives only in the cookie. `created_at` exposed in the /profile sessions section.
- **rooms** — `mode` (`mentorship` | `classroom`), `leaderboard_visible`.
- **memberships** — `(room_id, user_id)` PK, `role` (`mentor` | `mentee`).
- **invites** — single-use codes (`code_hash`) bound to a room + role.
- **reports** / **comments** — mentorship room artefacts. Both carry `edited_at` (stamped on every update) and `deleted_at` (soft-delete; non-empty rows are filtered from every read).
- **tasks** — mentorship room artefacts. `reviewed_at` locks status changes, edits, AND deletion once a mentor awards points. Same `edited_at` / `deleted_at` columns as reports.
- **assignments** / **submissions** — classroom room artefacts. `UNIQUE(assignment_id, student_id)` so resubmits update in place. `reviewed_at = ''` on a submission means it's awaiting review. Assignments carry `edited_at` / `deleted_at`; submissions don't (resubmit is the edit path, and they have no soft-delete since the UNIQUE constraint would block re-submission).
- **points_ledger** — append-only point awards. `UNIQUE(source, source_id)` prevents double-award races.
- **notification_prefs** — opt-in per user for deadline reminders. Channel + contact fields (email lives on `users`, WhatsApp number and Telegram chat ID live here). Engagement notifications gate on `users.engagement_notif_enabled` *and* this row.

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

Edit / soft-delete handlers follow a shared rule: the author of a piece of content can always edit or delete it; the room's mentor can edit or delete anyone's content in their room.

- `editReport`, `deleteReport` — author OR room mentor.
- `editComment`, `deleteComment` — author OR room mentor.
- `editTask`, `deleteTask` — room mentor only (mentees never edit task content; status updates go through `updateTask`).
- `editAssignment`, `deleteAssignment` — room mentor only, classroom rooms only.

Idempotency / lock guards in the store layer:

- `UpdateTaskStatus` won't change the status of a reviewed task (`WHERE reviewed_at = ''`).
- `UpdateTask` / `DeleteTask` won't edit or delete a reviewed task — same `reviewed_at = ''` guard, so points already awarded can't be silently undone by deleting the task.
- `ReviewSubmission` won't overwrite a previously reviewed submission (`WHERE reviewed_at = ''`). Resubmission by the student clears `reviewed_at` so the review cycle can repeat.
- Every read query filters `deleted_at = ''` so soft-deleted rows disappear from every view in one place; the row stays in the DB for audit.

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
  worker tick   ──┐ due tasks                  ┌─ off (skip)
                  ├─→ dispatch ────────────────┤
  worker tick   ──┘ due assignments            ├─ log (default fallback)
                                               │
  comment posted ─┐ NotifyEngagement           ├─ email (SMTP via net/smtp)
  submission made ┼─→ Engagement.Dispatch ─────┤
  feedback posted ┘ (synchronous, in goroutine)├─ whatsapp (stub)
                                               │
                                               └─ telegram (stub)
```

Two dispatch paths, one notifier registry:

- **Deadline reminders** run from the `Worker` goroutine on a ticker. Dedup via `tasks.last_reminded_at` / `assignments.last_reminded_at` — one notification per task or assignment per day across all recipients.
- **Engagement events** (`reminder.Engagement`) fire synchronously from web handlers after the underlying write commits: a comment on a report → report author; a submission → every mentor in the room; a teacher's feedback → the student. Dispatch is launched in a goroutine so the HTTP request never blocks on SMTP/HTTP fanout. Self-author events are dropped — never notify yourself.
- `Notifier` interface has three methods: `NotifyTaskDue`, `NotifyAssignmentDue`, `NotifyEngagement(ctx, Recipient, EngagementEvent)`. Adding a new channel means implementing all three, plus a constant in `internal/domain`.
- The notifier registry is built once in `cmd/sinau/main.go` and shared between the worker and the engagement dispatcher.
- `EmailNotifier` is real (SMTP, STARTTLS, falls back to log when `SINAU_SMTP_HOST/FROM` is unset).
- `WhatsAppNotifier` and `TelegramNotifier` are **interface-ready stubs**: full config struct, `Configured()` check, fallback wiring, and TODO blocks pointing at the integration paths (`go-whatsapp-web-multidevice` REST daemon and Telegram Bot API). To finish wiring a channel, fill in the three `Notify*` bodies — no changes to migrations, store, worker, or web layer needed.
- Notification content is localised per recipient: subject/body resolves through `i18n.T` using `users.language`.
- Two opt-out levels: `notification_prefs.enabled` controls the whole channel (and is the master gate for deadline reminders); `users.engagement_notif_enabled` lets a user keep deadline reminders while silencing engagement pings. Both must be on for an engagement notification to fire.
- Master switch: `SINAU_NOTIFICATIONS_ENABLED=false` hides the `/settings` UI, the topbar link, the `/help` section, skips starting the worker, AND skips building the engagement dispatcher.

## Search (FTS5)

Five FTS5 virtual tables — one per searchable resource — backed by
triggers that keep the index in lock-step with the source table:

```
reports        ──AI/AU/AD──> reports_fts
comments       ──AI/AU/AD──> comments_fts
tasks          ──AI/AU/AD──> tasks_fts
assignments    ──AI/AU/AD──> assignments_fts
submissions    ──AI/AU/AD──> submissions_fts
```

Design notes:

- Each FTS table stores the source row's TEXT id as an UNINDEXED
  `source_id` column so search hits can be joined back to the real row.
  (FTS5's `content_rowid` requires an INTEGER rowid, which Sinau's
  hex-string IDs aren't.)
- Soft-delete is a column update (`deleted_at`), not a DELETE — the
  trigger re-syncs the row, and the search query joins against the
  source table to filter `deleted_at = ''`. Simpler than maintaining a
  "this is logically deleted" trigger path.
- The `/search` query is one UNION ALL across the five tables, filtered
  per-source by the viewer's visibility rules (mentors see everything in
  their rooms; mentees see only their own reports / tasks / submissions;
  comments and assignments visible to all room members). Ranked by
  `bm25()` ascending across the union.
- Snippets use ASCII STX/ETX (`\x02`/`\x03`) as match markers. The
  Go layer HTML-escapes the snippet text first, then swaps the markers
  for `<mark>` / `</mark>`. User content can never inject HTML tags.
- Single-word queries are turned into prefix matches (`hand → hand*`)
  so most search-box UX expectations work. Multi-word queries fall
  through to FTS5's implicit AND.
- Indexing depends on the `sqlite_fts5` build tag (see Build & test).

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

- Server-rendered HTML via Go `html/template`. Single file (`templates/app.html`) with named templates (`landing`, `login`, `join`, `setup`, `mentor_home`, `mentee_home`, `room`, `report`, `report_edit`, `task_edit`, `assignment_edit`, `profile`, `settings`, `help`, `invite_created`, plus the `link_row` and `lang_picker` partials).
- Progressive enhancement via [htmx](https://htmx.org) — used for the invite form swap and the "add another link" multi-link inputs (`hx-get`, `hx-target`, `hx-swap=beforeend`).
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

The mattn/go-sqlite3 driver compiles SQLite's FTS5 module only with
the `sqlite_fts5` build tag, and `/search` depends on it. The repo's
`Makefile` sets the tag for every target so you don't have to remember.

```sh
make build   # bin/sinau, release ldflags
make test    # go test -tags sqlite_fts5 ./...
make vet
make run     # local dev server
```

If you call the Go toolchain directly, pass `-tags sqlite_fts5`
explicitly — without it the schema migration fails at boot with
`no such module: fts5`. CGO is required for SQLite.

Test coverage focuses on the store (which encodes authorization rules and the FTS triggers) and on web-layer boundaries (classroom flow + auth boundaries + CSRF + notification gating + edit/delete permissions).

## Non-goals & trade-offs

- **No horizontal scale.** `SetMaxOpenConns(1)` for SQLite means one writer at a time. Suitable for tens of concurrent users, not hundreds. If you outgrow this, swap the store package to Postgres before adding any other complexity.
- **No file uploads.** Sinau stores only links to external resources (Docs, Drive, Colab, repos). Keeps storage simple and avoids hosting user content.
- **No SSO.** Argon2 passwords + invite codes only. Adding OAuth would need a new field on `users` and a flow change at `/join`.
- **Per-instance, not multi-tenant.** One instance = one organization. There's no "owner_id" on rooms because the whole DB belongs to one operator.

## Known tech debt (carried, not blocking)

- `Assignment.MySubmissionStatus/URL/Feedback/Score` are viewer-specific fields on what should be a clean domain type. Refactor when the next feature touches submission shape; pure code-organisation, no user impact.
- No pagination on `Reports` (`LIMIT 80`), `Submissions`, `Members`, `Assignments`. Acceptable for small rooms; revisit if one room ever exceeds ~30 mentees with months of weekly history.
- Soft-deleted rows are never reaped. `deleted_at` rows stay forever — fine until a heavy churn user accumulates noise. A background sweeper that hard-deletes rows past N days is a cheap follow-up.
- Engagement notifications fire in fire-and-forget goroutines; a failure (SMTP down, network blip) is logged and lost. No retry queue, no surface in the UI. Acceptable for v1; if these become load-bearing, route through a persistent outbox.
