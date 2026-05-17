# Deployment

Sinau is a single Go binary with SQLite storage. The recommended deployment is:

- Sinau listens only on `127.0.0.1`.
- Nginx handles public HTTP/HTTPS.
- `SINAU_SECURE_COOKIE=true` is enabled behind HTTPS.
- The SQLite database lives in a persistent data directory.

## Run Locally

From the project directory:

```sh
go run ./cmd/sinau
```

Open:

```txt
http://127.0.0.1:8080
```

Use a custom address or database:

```sh
SINAU_ADDR=127.0.0.1:8090 SINAU_DB=data/dev.db go run ./cmd/sinau
```

Build a local binary:

```sh
mkdir -p bin
go build -trimpath -ldflags="-s -w" -o bin/sinau ./cmd/sinau
./bin/sinau
```

## Server Layout

Example production paths:

```txt
/opt/sinau/sinau          # binary
/opt/sinau/templates/     # HTML templates
/opt/sinau/static/        # htmx asset
/var/lib/sinau/sinau.db   # SQLite database
/etc/sinau/sinau.env      # environment file
```

Create directories:

```sh
sudo useradd --system --home /var/lib/sinau --shell /usr/sbin/nologin sinau
sudo mkdir -p /opt/sinau /var/lib/sinau /etc/sinau
sudo chown -R sinau:sinau /var/lib/sinau
```

Build on the server:

```sh
go build -trimpath -ldflags="-s -w" -o sinau ./cmd/sinau
sudo install -m 0755 sinau /opt/sinau/sinau
sudo cp -R templates /opt/sinau/templates
sudo cp -R static /opt/sinau/static
```

Or build elsewhere for Linux:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o sinau ./cmd/sinau
```

Note: the SQLite driver uses CGO. Cross-compiling may require a C toolchain for the target.

## Environment

Create `/etc/sinau/sinau.env`:

```sh
SINAU_ADDR=127.0.0.1:8080
SINAU_DB=/var/lib/sinau/sinau.db
SINAU_TEMPLATES=/opt/sinau/templates
SINAU_STATIC=/opt/sinau/static
SINAU_SECURE_COOKIE=true
SINAU_NOTIFICATIONS_ENABLED=true
SINAU_REMINDERS=true
SINAU_REMINDER_INTERVAL=1h
SINAU_REMINDER_WINDOW=24h
SINAU_SMTP_HOST=smtp.example.com:587
SINAU_SMTP_USER=sinau@example.com
SINAU_SMTP_PASS=app-specific-password
SINAU_SMTP_FROM=sinau@example.com
SINAU_SMTP_STARTTLS=true
```

Lock it down:

```sh
sudo chown root:sinau /etc/sinau/sinau.env
sudo chmod 0640 /etc/sinau/sinau.env
```

## systemd

Create `/etc/systemd/system/sinau.service`:

```ini
[Unit]
Description=Sinau learning progress tracker
After=network.target

[Service]
User=sinau
Group=sinau
WorkingDirectory=/opt/sinau
EnvironmentFile=/etc/sinau/sinau.env
ExecStart=/opt/sinau/sinau
Restart=on-failure
RestartSec=3

NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/sinau

# Resource limits — generous for a small instance, tighten if co-tenanting.
MemoryHigh=192M
MemoryMax=256M
TasksMax=128

[Install]
WantedBy=multi-user.target
```

Start it:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now sinau
sudo systemctl status sinau
```

Check logs:

```sh
journalctl -u sinau -f
```

## Nginx

Install Nginx and create `/etc/nginx/sites-available/sinau`:

```nginx
server {
    listen 80;
    server_name sinau.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl http2;
    server_name sinau.example.com;

    ssl_certificate /etc/letsencrypt/live/sinau.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/sinau.example.com/privkey.pem;

    client_max_body_size 1m;

    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options DENY always;
    add_header Referrer-Policy same-origin always;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;

        proxy_read_timeout 30s;
        proxy_send_timeout 30s;
    }
}
```

Enable it:

```sh
sudo ln -s /etc/nginx/sites-available/sinau /etc/nginx/sites-enabled/sinau
sudo nginx -t
sudo systemctl reload nginx
```

## TLS With Certbot

If using Let's Encrypt:

```sh
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d sinau.example.com
```

After TLS is active, keep:

```sh
SINAU_SECURE_COOKIE=true
```

If running locally over plain HTTP, leave it unset or set it to `false`, otherwise the browser will not send the session cookie.

## Backups

SQLite is safe to back up while the app is running if you use SQLite's online backup command:

```sh
sudo -u sinau sqlite3 /var/lib/sinau/sinau.db ".backup '/var/lib/sinau/backups/sinau-$(date +%F-%H%M).db'"
```

Create the backup directory first:

```sh
sudo mkdir -p /var/lib/sinau/backups
sudo chown sinau:sinau /var/lib/sinau/backups
```

For a small deployment, a daily cron job is enough:

```cron
15 2 * * * sudo -u sinau sqlite3 /var/lib/sinau/sinau.db ".backup '/var/lib/sinau/backups/sinau-$(date +\%F-\%H\%M).db'"
```

Copy backups off the server with `rsync`, S3-compatible storage, or your existing backup system.

## Updating

Build and replace the binary:

```sh
go build -trimpath -ldflags="-s -w" -o sinau ./cmd/sinau
sudo install -m 0755 sinau /opt/sinau/sinau
sudo cp -R templates /opt/sinau/templates
sudo cp -R static /opt/sinau/static
sudo systemctl restart sinau
```

Check:

```sh
sudo systemctl status sinau
curl -I https://sinau.example.com
```

## Security Checklist

- Keep Sinau bound to `127.0.0.1`, not `0.0.0.0`, when behind Nginx.
- Use HTTPS in production.
- Set `SINAU_SECURE_COOKIE=true` in production.
- Keep `/var/lib/sinau` readable only by the `sinau` user.
- Keep registration invite-only.
- Back up `/var/lib/sinau/sinau.db`.
- Do not put the database inside the git repository or release directory.

## Deadline Reminders

Sinau includes a lightweight in-process reminder worker. It scans open
tasks with deadlines inside the reminder window and dispatches one
reminder per task per day to each opted-in recipient.

Recipients with no row in `notification_prefs` default to `off`, so nobody
is pinged without opting in at `/settings`.

Controls:

```sh
# Master switch. false → no reminder worker, no /settings, no Notifications
# section on /help, no Settings link in the topbar, AND no engagement
# notifications (comment / submission / feedback pings).
SINAU_NOTIFICATIONS_ENABLED=true
# Granular switch: false keeps /settings, /profile, and engagement
# notifications working but pauses the deadline-reminder ticker.
SINAU_REMINDERS=true
SINAU_REMINDER_INTERVAL=1h
SINAU_REMINDER_WINDOW=24h
SINAU_SMTP_HOST=smtp.example.com:587
SINAU_SMTP_USER=sinau@example.com
SINAU_SMTP_PASS=app-specific-password
SINAU_SMTP_FROM=sinau@example.com
SINAU_SMTP_STARTTLS=true
# Optional, channel stubs. Leaving these empty keeps the channels selectable
# in /settings but routes them to the server log instead of attempting
# delivery.
SINAU_WHATSAPP_API_URL=http://127.0.0.1:3000
SINAU_WHATSAPP_API_KEY=replace-me
SINAU_TELEGRAM_BOT_TOKEN=replace-me
```

The reminder worker dispatches one notification per due task per recipient
based on each user's preference at `/settings`. Recipients with no row
default to `off`, so nobody is pinged without opting in.

Channels:

- `off` — no notifications.
- `email` — SMTP via the `SINAU_SMTP_*` env vars.
- `whatsapp` — **preview**. Interface and DI plumbing complete; the actual
  HTTP call to a WhatsApp gateway (e.g.
  `aldinokemal/go-whatsapp-web-multidevice`) is a TODO in
  `internal/reminder/whatsapp.go:NotifyTaskDue`. Until filled in, this
  channel falls back to log.
- `telegram` — **preview**. Same shape as WhatsApp; TODO in
  `internal/reminder/telegram.go:NotifyTaskDue` to call the Bot API. Falls
  back to log until wired.
- `log` — server log; useful for development and admin debugging.

To finish wiring a preview channel (or plug a new one entirely):

1. Implement / fill in `reminder.Notifier.NotifyTaskDue` for that
   channel. The stubs already declare config structs, fallback wiring, a
   `Configured()` check, and the right log lines.
2. Register the notifier in `cmd/sinau/main.go:buildNotifiers` (already
   done for `whatsapp` and `telegram` stubs).
3. Add a new constant in `internal/domain` and let it through
   `domain.ValidNotifChannel`.
4. Add the option to the `/settings` form in `templates/app.html`.

No migration is required to add channels — `notification_prefs.channel`
is validated in application code (`domain.ValidNotifChannel`) rather than
a CHECK constraint, intentionally.

Disable reminders:

```sh
SINAU_REMINDERS=false
```
