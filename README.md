# Sinau

Small mentor/learner progress tracker.

## Run

```sh
go run ./cmd/sinau
```

Open `http://127.0.0.1:8080`.

Environment:

- `SINAU_ADDR`, default `127.0.0.1:8080`
- `SINAU_DB`, default `data/sinau.db`
- `SINAU_SECURE_COOKIE`, set `true` behind HTTPS

## Model

- First run creates the first mentor and first room.
- Mentors create invite codes for learners or other mentors.
- Learners submit progress reports and links to docs, PDFs, Drive, Colab, or repos.
- Mentors comment on reports and assign tasks.
