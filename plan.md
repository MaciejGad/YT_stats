# Implementation plan — YarnPower YouTube statistics

**English** | [Polski](plan_pl.md)

The application's behaviour, limits and acceptance criteria are described in [requirements.md](requirements.md). Each step ends with a green `go test ./...` and passing the manual scenarios of that step.

## Project layout

```
cmd/yt-stats/        main: server, hash-password and revoke-sessions commands
internal/config/     loading and validating the configuration
internal/youtube/    API client, pagination, fallback key
internal/stats/      cache, single-flight, stale state
internal/auth/       Argon2id, sessions, session file, CSRF, limiter
internal/web/        HTTP handlers, headers, loading files from web_dir
web/                 index.html, login.html, script.js, style.css, logo.png, favicon.png
deploy/              yt-stats.service, example Nginx configuration
config.example.json  example configuration with empty values
```

## 1. Set up the Go project

- Create a Go module in `~/Code/yt_stats` and run `git init` (`.gitignore` already excludes `old_implementation/`, the binary and the local configuration).
- Build the server on `net/http`, the views on `html/template` and the data format on `encoding/json`. For Argon2id add `golang.org/x/crypto` in a version compatible with the Go in use. Import `time/tzdata`. Commit `go.mod` and `go.sum`.
- JSON configuration (`config.json`, permissions `0600`) with the fields `listen`, `channel_id`, `api_key`, `api_key_fallback` (optional), `username`, `password_hash`, `session_file`, `web_dir`. Validation reports all missing or invalid fields at once, by name and without their values. The configuration path is given with the `-config` flag.
- The `yt-stats hash-password` command reads the password twice without echo and prints an Argon2id hash in PHC format (`$argon2id$v=19$m=19456,t=2,p=1$...`).
- Confirm the channel ID with `channels.list?part=id&forHandle=@YarnPowerPL` (expected `UC5711W83ebIxkC8bHjOKI7g`).
- Prepare loading files from `web_dir`: at startup read and parse all files; while running, compare modification times on every request and reload only the changed ones. An invalid template keeps the previous version and is logged.

**Unit tests**
- `config`: a valid file; each required field missing separately (the message contains the field name); several missing at once; an unknown field; an invalid `listen` format; `channel_id` without the `UC` prefix; `password_hash` in a format other than PHC Argon2id; a missing `api_key_fallback` is allowed; the error message does not contain the key value.
- `hash-password`: the hash verifies with the same password and not with another; two calls give different salts; a mismatched repeated password gives an error.
- Loading `web_dir`: a missing file at startup gives an error naming the file; a template syntax error at startup gives an error; a changed file (with a changed modification time) shows up in the next render; a change to an invalid template keeps the previous version; concurrent rendering during a swap passes under `-race`.

**Manual scenarios**
- Run `go run ./cmd/yt-stats -config config.example.json`: startup is aborted and the message lists all empty fields.
- Run `go run ./cmd/yt-stats hash-password`: the password is not shown on screen, the output starts with `$argon2id$`.
- Call `channels.list` with `forHandle` (e.g. `curl` with the key in an environment variable) and compare the ID with the configuration.

## 2. Implement the YouTube client and the cache

- Use `channels.list` (`part=statistics,contentDetails`) to get the statistics and the ID of the uploads playlist. A hidden subscriber count (`hiddenSubscriberCount`) gives `null`.
- Fetch the pages of `playlistItems.list` (`maxResults=50`) following `nextPageToken`, at most 200 pages. Fetch details in batches of 50 IDs with `videos.list`, `part=snippet,statistics`, and a `fields` parameter narrowing the response. Restore the playlist order, remove duplicates and skip videos the API did not return.
- Use the fixed URL `https://www.googleapis.com/youtube/v3/`, a 10 s limit per request, 30 s for the whole refresh and 5 MiB per response. Sanitise errors before logging (remove the `key` parameter from the URL, log the HTTP status and `reason`).
- Handle the fallback key: on `403` with reason `quotaExceeded` or `dailyLimitExceeded`, retry the same request with the fallback key, remember the switch until the next midnight `America/Los_Angeles` and log it. Other errors do not switch keys.
- Add a cache in `internal/stats` with one shared fetch for concurrent requests. Replace the previous result only after the whole fetch succeeds. Use 60 s freshness, a 24 h maximum age for stale results and a 30 s pause after an error. Store `update_time` as ISO 8601 in the `Europe/Warsaw` zone.
- Inject the clock and the API URL through interfaces so tests can replace them.

**Unit tests** (local `httptest` server, controlled clock)
- Channel: statistics and playlist; a hidden subscriber count gives `null`; a missing channel (`items: []`) gives an error.
- Pagination: 1 page, 3 pages, an empty channel, exceeding the 200-page limit gives an error.
- Batching: 0, 1, 50, 51 and 120 videos give 0, 1, 1, 2 and 3 `videos.list` calls respectively, each with at most 50 IDs.
- The order matches the playlist even when `videos.list` returns a different order; a duplicate in the playlist appears once; a video missing from `videos.list` is skipped; a missing `viewCount` gives `null`.
- Errors: a single request timing out, the whole refresh exceeding 30 s, a response larger than 5 MiB, invalid JSON, `500`, `403 forbidden`; the error log does not contain the key.
- Fallback key: `quotaExceeded` on the primary key switches and retries the same request; the next refresh uses the fallback straight away; after midnight Pacific time the primary key is back (also tested across a daylight saving change); `keyInvalid` does not switch; without a configured fallback key the error is returned as usual; quota exhausted on both keys gives an error.
- Cache: a second request within 60 s does not call the API; after 61 s it does; 10 concurrent requests trigger one fetch (under `-race`); an error with a 23 h old result returns `stale: true`; an error with a 25 h old result gives `503`; no new API calls within 30 s after an error; a new attempt after 30 s; a partially successful fetch does not replace the previous result.
- `update_time`: ISO 8601 with a `+01:00` offset in winter and `+02:00` in summer.

**Manual scenarios**
- A small program or a test with the `integration` tag run with a real key: the number of videos matches the channel, titles and counters match YouTube.
- Run it with an invalid primary key and a valid fallback: the key does not switch (it is not a quota error), the log contains the reason and not the key.

## 3. Add login and persistent sessions

- Build the form and Argon2id password verification with the parameters from the requirements. For an unknown username verify the password against a fixed dummy hash. Compare the username in constant time.
- Add a limiter: 5 failed attempts per IP in 15 minutes, 50 in total per hour, `429` with `Retry-After`, at most 10,000 addresses. Limit concurrent verifications to 2 with a wait of up to 3 s, then `503`. Limit the form to 4 KiB. Take the client address from `X-Real-IP` only for loopback connections.
- Protect both forms with a CSRF token. For login, bind the token to a short pre-login session (in memory, 30 minutes, at most 1,000) and issue a new session identifier after success. The logout token is bound to the real session. Compare tokens in constant time.
- Generate identifiers with `crypto/rand`. Store the SHA-256 hash and the expiry (at most 100 sessions; when exceeded, remove the oldest). Synchronise modifications and replace the file atomically: a temporary file in the same directory, `fsync`, rename. Set permissions to `0600`. Update the in-memory state only after a successful write. Hold `flock` on a lock file for the whole run.
- Add a `yt-stats revoke-sessions` command that takes the same lock and empties the session file.
- Add a shared session check before rendering the panel and returning data. Set and delete the cookie with the same path. If saving a logout fails, show an error and allow retrying.
- Measure Argon2id verification time on Mikrus (expected below 300 ms).

**Unit tests** (controlled clock, temporary directory)
- Password: a correct login; a wrong password; a wrong username; both failures give an identical response; with a wrong username Argon2 still runs (call counter in a fake).
- Session without "Remember me": cookie without `Expires`/`Max-Age`, valid at 11 h 59 min, invalid after 12 h.
- Session with "Remember me": `Max-Age` 90 days, valid at 89 days 23 h, invalid after 90 days; using the panel does not move the expiry.
- Cookie: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/yt/`, no `Domain`; the identifier is 43 base64url characters; the file holds only the hash, not the identifier.
- Restart: a new store instance reads the file and accepts the session; expired entries disappear on the next write.
- Session file: corrupt JSON; a file larger than 1 MiB; no read permission; no write permission on login (login returns an error, no session is created in memory); a write error on logout (error, the session stays valid, retrying works); permissions after writing are `0600`; the 101st session removes the oldest.
- Lock: a second store instance on the same file and `revoke-sessions` while a store is running both fail; `revoke-sessions` without a running service empties the file.
- CSRF: no token, a wrong token, a token from another pre-login session, an expired pre-login session — all give `403`; after login the session identifier differs from the one before login.
- Limiter: 5 failures pass, the 6th gives `429` with `Retry-After`; after 15 minutes it works again; a success resets the counter; the 51st failure from different IPs within an hour gives `429`; `X-Real-IP` is ignored for non-loopback connections; the table never exceeds 10,000 addresses.
- Concurrency: 3 simultaneous verifications (a fake Argon2 with a delay) — 2 start at once, the 3rd waits; after waiting 3 s, `503`.
- A form larger than 4 KiB gives `413`; a username over 64 B or a password over 256 B fails login without calling Argon2.

**Manual scenarios**
- Log in with Chrome at `http://localhost:8091/yt/` without "Remember me"; in DevTools → Application → Cookies check the attributes and that there is no expiry date.
- Log in with "Remember me", restart the process, reload the page — the panel opens without logging in.
- Log out, press Back and reload — the login form, not the panel.
- Enter a wrong password 6 times — after the 5th attempt a message about the limit.
- Submit the login form with `curl` without a CSRF token — `403`.
- Stop the process, run `revoke-sessions`, start the process — the previously logged-in browser lands on the login page. Run `revoke-sessions` while the process is running — a lock error.
- Check `ls -l` of the session file (`-rw-------`) and its contents (only hashes and dates).

## 4. Build the panel and HTTP handlers

- Redesign `web/index.html`, `web/login.html`, `web/style.css`, `web/script.js`, `web/logo.png` and `web/favicon.png` following the "Look and responsiveness" section of the requirements. From the old version we keep only the data scope; error handling and the unused chart code are rewritten or dropped. No `setInterval`, no external resources, no inline scripts or styles (CSP).
- The script fetches `/yt/stat.json` on load and when "Odśwież" is clicked. It handles loading, an empty list, stale data (`stale`), `503` and other errors with a retry, network errors and `401` (redirect to login). It inserts titles with `textContent` and builds links only from `videoId` (`https://www.youtube.com/watch?v=` + `encodeURIComponent`), with `rel="noopener"`.
- Numbers are formatted with `Intl.NumberFormat('pl-PL')` and time with `Intl.DateTimeFormat('pl-PL', {timeZone: 'Europe/Warsaw'})`; `null` is shown as "Niedostępne". Sorting (playlist order / views descending) works without a new request; "Niedostępne" goes last.
- Implement the routes from the requirements table with `HEAD`, `405` and `404` handling, a security headers middleware and `Cache-Control: no-store` for the login page, the panel and `stat.json`. Serve static files from `web_dir` with `Cache-Control: no-cache` and `ETag`: the browser checks for changes on every use (304 when unchanged), so editing a file on the server shows up after a normal reload.
- Set the HTTP server timeouts from the requirements.

**Unit tests** (`httptest`, fake YouTube client)
- JSON contract: the fields `stats.subscriberCount`, `stats.viewsCount`, `videos[].videoId`, `videos[].title`, `videos[].viewCount`, `update_time`, `stale`; counters are digit strings or `null`; `update_time` parses as RFC 3339; `Content-Type: application/json; charset=utf-8`.
- Without a session: `GET /yt/` gives `303` to `/yt/login`; `GET /yt/stat.json` gives `401` with JSON; the fake YouTube was not called.
- `GET /yt/login` with a valid session gives `303` to `/yt/`.
- Errors: `503` without a stale result; the `error` message contains no internal details.
- Routes: `HEAD` on every `GET` route; `POST /yt/stat.json` gives `405` with `Allow`; `/yt/nieznane`, `/yt/index.php`, `/yt/stat.php`, `/yt/statistics.csv` give `404`; `/yt/../config.json` and `/yt/%2e%2e/` do not escape `web_dir`.
- Headers: CSP, `X-Content-Type-Options`, `Referrer-Policy` on every response, including `404`; `no-store` on protected pages and the login page.
- Rendering: a video title containing `<script>` is never inserted into HTML unescaped (templates do not insert video data, but the test guards this assumption); forms contain a CSRF token.
- Files: a change to `web/script.js` in a temporary directory is served without a restart; the `ETag` changes when the file changes.

**Browser tests of the script** (optional, `node --test` with a fake DOM, or by hand)
- Number and date formatting, sorting with `null` values, building a link from a `videoId` with special characters. Run with `node --test test/js/*.test.js`.

**Manual scenarios** (`http://localhost:8091/yt/`, Chrome and Firefox)
- First visit: a loading state, then the tiles, the list and the fetch time in Polish time; with the system zone changed (e.g. to `America/New_York`) the time is still Polish.
- "Odśwież" twice within a minute: the button is disabled while loading, the fetch time does not change. After a minute the time changes.
- Sorting by views and back to publication order.
- API key changed to an invalid one and a restart: an error message with a retry button. With a result in memory and a broken key (API URL swapped for a fake): a "dane z pamięci podręcznej" (cached data) banner with the original time.
- Delete the cookie in DevTools and click "Odśwież": redirect to login.
- Responsiveness in DevTools device mode: 360, 375, 768, 1280 px — no horizontal scrolling, a table from 768 px, cards below. Light and dark themes (DevTools → Rendering → `prefers-color-scheme`).
- Keyboard: Tab moves through login, "Odśwież", sorting, links and "Wyloguj" with visible focus.
- The browser console shows no errors and no CSP violations.
- Change a colour in `web/style.css` and text in `web/index.html`, reload the page without a restart — the changes are visible. Introduce a syntax error in `index.html` — the page keeps working in the old version and the log contains the error.
- A phone on the same network is not required locally; a real phone is checked in step 6.

## 5. Build and run on Mikrus

- On the Mac run `go test ./...`, `go test -race ./...` and `go vet ./...`.
- On Mikrus check `uname -m`. For `x86_64` build on the Mac with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$(git describe --always --dirty)" -o yt-stats ./cmd/yt-stats`. For `aarch64` set `GOARCH=arm64`. Measure the build time (`time`) for a clean build (`go clean -cache`) and an incremental one.
- The package is built by `deploy/build.sh amd64|arm64` (tests, binary, `web/`, service file, `install.sh`) and installed with `sudo ./install.sh` on the server; details in `deploy/README.md`. The script copies the binary to `/opt/yt-stats/yt-stats` and the `web/` directory to `/opt/yt-stats/web/`, keeping the previous versions as `*.prev`. Create a dedicated `yt-stats` system user, the configuration in `/etc/yt-stats/config.json` (owner `root:yt-stats`, `0640`) and the state directory `/var/lib/yt-stats/` (`StateDirectory=`).
- Configure `deploy/yt-stats.service`: `Restart=on-failure`, `User=yt-stats`, `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `StateDirectory=yt-stats`, `ReadOnlyPaths=/opt/yt-stats`, `MemoryMax=128M`. Run the service on `127.0.0.1:8091`.
- Generate the password hash with `yt-stats hash-password` and put it in the configuration.

**Automated tests**
- `go test ./...`, `go test -race ./...`, `go vet ./...` green before every deployment.
- `systemd-analyze verify deploy/yt-stats.service` on the server.

**Manual scenarios** (on the server)
- `systemctl status yt-stats` — active; `ss -ltnp | grep 8091` — listening only on `127.0.0.1`.
- `curl -i http://127.0.0.1:8091/yt/` — `303` to `/yt/login`; `curl -i http://127.0.0.1:8091/yt/stat.json` — `401`.
- `kill -9` the process — systemd starts it again within a few seconds.
- `journalctl -u yt-stats` — no API keys, password, hash or session identifiers (`grep` for a fragment of the key returns nothing).
- Measure RSS (`ps -o rss= -p $(pidof yt-stats)` or `systemctl show -p MemoryCurrent`) idle, during login and while fetching statistics; record the results and lower `MemoryMax` if needed.
- The time of one Argon2id verification on Mikrus (login timing in the log or a short benchmark).
- Editing a file in `/opt/yt-stats/web/` (as root) — the change is visible without restarting the service.

## 6. Switch Nginx, verify and finish the migration

- The domain is behind Cloudflare (HTTPS ends there; traffic reaches Nginx over HTTP on port 80). `deploy/nginx/cloudflare-real-ip.sh` creates `/etc/nginx/snippets/cloudflare-real-ip.conf` from the official list of Cloudflare addresses (`set_real_ip_from` + `real_ip_header CF-Connecting-IP`), so that `$remote_addr` is the user's address.
- `deploy/nginx/yt-stats.conf` goes to `/etc/nginx/snippets/yt-stats.conf` and is included with a single `include` line in the `server` block for `maciejgad.pl`. It contains `location = /yt { return 301 https://$host/yt/; }` and `location ^~ /yt/` with `proxy_pass http://127.0.0.1:8091;`. No path after the proxy address keeps the `/yt/` prefix, and `^~` takes precedence over the rules for `.php` and static files. `Host`, `X-Real-IP`, `X-Forwarded-For` (overwritten) and `X-Forwarded-Proto: https`, HSTS.
- Before the change, back up the vhost to `~/maciejgad.pl.przed-yt-stats`. Then `nginx -t`, reload, and purge the Cloudflare cache for the old `/yt/` files (e.g. `statistics.csv` was cached for 4 h).
- Once everything works, remove the old cron entry if there is one. The old files live in the `HomePage` repository (`html/yt/`), deployed with `git pull`, so we delete them there (after a private copy of `statistics.csv`), not only on the server. Before disabling PHP-FPM for the whole server, check the other sites that may use PHP.
- If after the switch the panel does not meet the acceptance criteria, restore the vhost backup, run `nginx -t` and reload Nginx. The steps are in `deploy/README.md`, section 9.

**Automated tests**
- `nginx -t` before every reload.
- The `deploy/smoke.sh` script run from the Mac (`curl`, headers without bodies) checks: `/yt` → `301`; `/yt/` without a session → `303`; `/yt/stat.json` without a session → `401`; the old URLs `/yt/index.php`, `/yt/stat.php`, `/yt/functions.php`, `/yt/require_auth.php`, `/yt/graph_data.php`, `/yt/cron.php`, `/yt/cron.sh`, `/yt/statistics.csv` → `404`; presence of CSP, `nosniff`, HSTS; `http://` → redirect to `https://`.

**Manual scenarios** (acceptance criteria)
- First login with "Remember me" on a computer; closing the browser and returning without a password.
- `systemctl restart yt-stats` — the session still works, the first refresh fetches data again.
- Login without "Remember me" on a phone; checking the look on a phone and a tablet in both themes.
- Expiry: a temporarily shortened expiry in the session file (with the service stopped) — after startup the browser lands on the login page.
- Logging out and trying to get back with the Back button.
- 6 failed logins from a phone on mobile data — `429` only for that IP, the computer can still log in (confirms a correct `X-Real-IP`).
- The number of videos in the panel compared with the channel in YouTube Studio (excluding private and deleted ones).
- API failure: a temporarily invalid primary key in the configuration and a restart after an earlier fetch — no data and a message; then restore the key. Switching to the fallback key cannot be triggered on demand; it is confirmed by the step 2 tests and by watching the logs.
- Editing `style.css` on the server is visible after a reload without a restart.
- The old URLs from the list in a browser — none returns the old data.

## Technical sources

- [Nginx — location selection](https://nginx.org/en/docs/http/ngx_http_core_module.html#location)
- [Nginx — how proxy_pass passes the path](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_pass)
- [OWASP — CSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)
- [systemd — service sandboxing options](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
