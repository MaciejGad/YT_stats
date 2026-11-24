# Deploying yt-stats on Mikrus

**English** | [Polski](README_pl.md)

The service runs as `yt-stats.service` under systemd and listens on `127.0.0.1:8091`; Nginx (step 6 of the plan) forwards `/yt/` to it.

| Path | Contents | Owner, permissions |
| --- | --- | --- |
| `/opt/yt-stats/yt-stats` | binary | `root:root 0755` |
| `/opt/yt-stats/web/` | templates, CSS, JS, logo (editable without a restart) | `root:root 0644` |
| `/etc/yt-stats/config.json` | configuration with the API key and the password hash | `root:yt-stats 0640` |
| `/var/lib/yt-stats/sessions.json` | sessions (hashes only) | `yt-stats 0600` |
| `/etc/systemd/system/yt-stats.service` | service | `root:root 0644` |

## 1. Server architecture

On the server:

```bash
uname -m
```

`x86_64` → `amd64` package, `aarch64` → `arm64` package.

## 2. Building on the Mac

In the project directory (runs the tests, then builds):

```bash
deploy/build.sh amd64
```

Result: `dist/yt-stats-<version>-linux-amd64.tar.gz` with the binary, the `web/` directory, the service file, an example configuration, `install.sh` and these instructions.

## 3. First installation

```bash
scp dist/yt-stats-*-linux-amd64.tar.gz USER@SERVER:/tmp/
ssh USER@SERVER
cd /tmp && tar xzf yt-stats-*-linux-amd64.tar.gz && cd yt-stats-*-linux-amd64
sudo ./install.sh
```

The first run creates the `yt-stats` user, copies the files and creates `/etc/yt-stats/config.json` with empty fields, then exits. Fill in the configuration:

```bash
/opt/yt-stats/yt-stats hash-password
sudo nano /etc/yt-stats/config.json
```

Enter `channel_id` (the `UC…` ID of your channel), `api_key` (optionally `api_key_fallback`), `username`, and the output of `hash-password` as `password_hash`. Then again:

```bash
sudo ./install.sh
```

The script checks the configuration as the service user (`check-config`), starts the service and checks that `/yt/login` responds with `200`.

## 4. Updating

The same as the first installation: build a new package, copy it, unpack it, `sudo ./install.sh`. The configuration and sessions are kept. The previous binary and interface files are kept as `/opt/yt-stats/yt-stats.prev` and `/opt/yt-stats/web.prev`.

Changes to `web/` alone (CSS, HTML, JS, logo) can also be uploaded straight to `/opt/yt-stats/web/` — they take effect after a page reload, without a restart.

## 5. Checking after installation

```bash
systemctl status yt-stats
ss -ltnp | grep 8091
curl -si http://127.0.0.1:8091/yt/ | head -3
curl -si http://127.0.0.1:8091/yt/stat.json | head -1
journalctl -u yt-stats -n 50 --no-pager
```

Expected: the service is `active (running)`, listening only on `127.0.0.1:8091`, `/yt/` → `303` to `/yt/login`, `/yt/stat.json` → `401`.

No secrets in the journal (a fragment of the API key and of the password hash; both results should be `0`):

```bash
sudo journalctl -u yt-stats --no-pager | grep -c 'AIza' ; sudo journalctl -u yt-stats --no-pager | grep -c 'argon2id'
```

Automatic restart after a crash:

```bash
sudo systemctl kill -s KILL yt-stats; sleep 7; systemctl is-active yt-stats
```

## 6. Measurements

Memory (RSS in KiB) when idle, during login and after fetching statistics:

```bash
ps -o rss=,cmd= -C yt-stats
systemctl show yt-stats -p MemoryCurrent -p MemoryPeak
```

Password verification time on the server (the gap between journal entries is too coarse, so we measure just the hashing with the same parameters):

```bash
time (printf 'test\ntest\n' | /opt/yt-stats/yt-stats hash-password >/dev/null)
```

The result covers two things at once: process startup and one Argon2id run (m = 19 MiB, t = 2). Expected below 300 ms.

## 7. Removing all sessions (e.g. after a password change)

The command must run as the service user — run as root it would create a session file the service cannot read.

```bash
sudo systemctl stop yt-stats
sudo runuser -u yt-stats -- /opt/yt-stats/yt-stats revoke-sessions -config /etc/yt-stats/config.json
sudo systemctl start yt-stats
```

## 8. Rolling back

```bash
sudo mv /opt/yt-stats/yt-stats.prev /opt/yt-stats/yt-stats
sudo rm -rf /opt/yt-stats/web && sudo mv /opt/yt-stats/web.prev /opt/yt-stats/web
sudo systemctl restart yt-stats
```

## Troubleshooting

The application's messages are in Polish; they are quoted below as they appear, with a translation.

**The service fails to start with status `226/NAMESPACE` or `218/CAPABILITIES`.** Some containers (e.g. unprivileged LXC) do not allow some of the sandboxing options. Turn them off with a drop-in instead of editing the service file:

```bash
sudo systemctl edit yt-stats
```

and enter:

```ini
[Service]
PrivateDevices=no
ProtectKernelTunables=no
ProtectKernelModules=no
ProtectKernelLogs=no
ProtectControlGroups=no
ProtectClock=no
ProtectHostname=no
RestrictNamespaces=no
```

Then `sudo systemctl restart yt-stats`. If it still does not work, add `PrivateTmp=no` and `ProtectHome=no`.

**`plik sesji ... jest zablokowany` (session file ... is locked).** Another instance is already running (e.g. started by hand). Check `ps -C yt-stats`.

**`plik konfiguracji ma uprawnienia 0644` (configuration file has permissions 0644).** `sudo chmod 640 /etc/yt-stats/config.json && sudo chown root:yt-stats /etc/yt-stats/config.json`.

**`odczyt pliku sesji: permission denied` (reading the session file: permission denied).** The session file was created by root (e.g. `revoke-sessions` without `runuser`). Fix: `sudo chown yt-stats:yt-stats /var/lib/yt-stats/sessions.json*`.

## 9. Nginx and Cloudflare (switching /yt/)

`maciejgad.pl` is behind Cloudflare: HTTPS ends at Cloudflare, and Nginx receives traffic over HTTP on port 80. Therefore:

- the client address comes from the `CF-Connecting-IP` header, accepted only from Cloudflare addresses (`nginx/cloudflare-real-ip.sh`); without it the login attempt limit would count all users as a handful of Cloudflare addresses,
- after the switch the Cloudflare cache must be purged for the old files (e.g. `statistics.csv` was cached for 4 h).

All commands run on the server, in the directory of the unpacked package.

1. Back up the current configuration (for rollback):
   ```bash
   sudo cp -p /etc/nginx/sites-available/maciejgad.pl ~/maciejgad.pl.przed-yt-stats
   ```
2. The Cloudflare address list and the snippet for `/yt/`:
   ```bash
   sudo ./nginx/cloudflare-real-ip.sh
   sudo install -m 0644 nginx/yt-stats.conf /etc/nginx/snippets/yt-stats.conf
   ```
3. Include the snippet in the vhost (one line after `index ...;`) and check:
   ```bash
   sudo sed -i '/^\s*index index.php index.html;/a\	include /etc/nginx/snippets/yt-stats.conf;' /etc/nginx/sites-available/maciejgad.pl
   grep -n 'yt-stats' /etc/nginx/sites-available/maciejgad.pl
   ```
   `grep` must show exactly one line.
4. Test and reload:
   ```bash
   sudo nginx -t && sudo systemctl reload nginx
   ```
5. Cloudflare → maciejgad.pl → Caching → Configuration → Custom Purge → URL, enter (one per line):
   ```
   https://maciejgad.pl/yt/statistics.csv
   https://maciejgad.pl/yt/stat.json
   https://maciejgad.pl/yt/stat.php
   https://maciejgad.pl/yt/index.php
   https://maciejgad.pl/yt/script.js
   https://maciejgad.pl/yt/graph_data.php
   https://maciejgad.pl/yt/cron.sh
   ```
6. On the Mac: `deploy/smoke.sh` — every line `ok`.

**Rollback:**
```bash
sudo cp -p ~/maciejgad.pl.przed-yt-stats /etc/nginx/sites-available/maciejgad.pl
sudo nginx -t && sudo systemctl reload nginx
```
