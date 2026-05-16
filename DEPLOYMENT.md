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
