# Sinau

Sinau is a small, invite-only space for mentors and teachers to track the
progress of the people they're helping — without spreadsheets, group chats,
or a heavy LMS.

> "Sinau" is Javanese for *to learn*.

## What it's for

Two shapes of relationship, one app:

- **Mentorship rooms** — long-running 1:1 or small-group mentorship.
  Mentees write progress reports and link to their work (Docs, Drive,
  Colab, repos). Mentors comment, assign tasks, and award points after
  reviewing what's done.
- **Classroom rooms** — cohort-style. Teachers post assignments with
  deadlines. Students submit a link + notes. Teachers review, leave
  feedback, and score on a 1–5 rubric.

A single Sinau instance can host both kinds of room at the same time.

## Who it's for

- Independent mentors and coaches who outgrew "Track in a spreadsheet."
- Workshop teachers and bootcamp instructors running small cohorts.
- Study circles, reading groups, code clubs.
- Anyone who wants progress tracking that lives at *their* URL, on *their*
  server, with no SaaS account in the loop.

If you need hundreds of concurrent students, SCORM compliance, or built-in
video calls — Sinau is not that. It's deliberately small.

## Layered roles

The same person can wear different hats in different rooms:

| In a Mentorship room      | In a Classroom room        |
|---------------------------|----------------------------|
| Mentor                    | Teacher                    |
| Mentee                    | Student                    |

The role you have in a room is set when you're invited — you don't pick it
yourself. So someone who mentors juniors in one room can be a student in
another room where they're learning from someone else, with no separate
account.

Account-level "can create new rooms" is a separate capability. Being
invited as a mentor to one room doesn't let you create rooms of your own
elsewhere on the instance.

## How it works

**For a mentor / teacher:**

1. Bootstrap your account on first run.
2. Create a room. Pick the format (Mentorship or Classroom).
3. Generate a single-use invite link. Send it to the mentee / student.
4. Watch reports / submissions come in. Leave comments, assign work, score.
5. Optional: flip on a leaderboard if you want the room to see points.

**For a mentee / student:**

1. Open the invite link your mentor / teacher sent you.
2. Pick a password.
3. Post reports / submit assignments. Pick up tasks. Read feedback.
4. Opt in to deadline reminders at **/settings** if you want them.
5. Update your name, email, password, or language at **/profile**.

## What's in the box

- **Progress reports + comments** (Mentorship), with edit and soft-delete
  by the author or the room's mentor
- **Assignments + submissions** with 1–5 rubric and feedback (Classroom);
  teachers can edit / remove published assignments
- **Tasks** with deadlines and points; editable until the mentor has
  reviewed them
- **Per-room leaderboards** — optional, off by default, mentors choose
  whether to make them visible to the room
- **Deadline reminders** — opt-in per user; channels include email, with
  WhatsApp and Telegram wired as preview integrations behind a clean
  notifier interface
- **Engagement notifications** — when someone comments on your report, a
  student submits, or a teacher posts feedback, the right person gets
  pinged on their chosen channel. Per-user opt-out separate from
  deadline reminders.
- **Account self-service** at `/profile` — change name, email, language,
  password (revokes other sessions), and see / sign out other active
  sessions.
- **Per-user notification settings** at `/settings`
- **In-app help** at `/help` for both mentors/teachers and mentees/students

## What it deliberately is not

- Not multi-tenant. One Sinau instance = one organisation. No "billing
  customer" model.
- No file hosting. Sinau stores **links** to your work, not the files
  themselves.
- No public sign-up. Joining a room requires an invite link from a mentor.
- No native mobile app. The web UI is responsive and works on phones.
- No analytics / no third-party scripts / no tracking.

## Self-host in a minute

Sinau is a single Go binary with a SQLite file behind it. To poke at it
locally:

```sh
go run ./cmd/sinau
```

Then open `http://127.0.0.1:8080`. The first time you load it, you'll be
asked to create the bootstrap account.

For production deployment (nginx + TLS + systemd) see
[DEPLOYMENT.md](DEPLOYMENT.md). For the technical shape of the codebase —
package layout, data model, security model, migrations — see
[ARCHITECTURE.md](ARCHITECTURE.md).
