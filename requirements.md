# Requirements — YarnPower YouTube statistics

**English** | [Polski](requirements_pl.md)

## Goal and scope

A private panel at `https://maciejgad.pl/yt/` shows current statistics for the [@YarnPowerPL](https://www.youtube.com/@YarnPowerPL) channel. The channel is set in the configuration, so someone else can deploy the same panel for their own channel. It replaces the features of the existing `index.php`, `stat.php`, `functions.php` and `script.js`. The scope covers login, a channel summary and a table of videos. CSV history, charts, cron and periodic background refreshes are out of scope.

The `old_implementation/` directory holds a copy of the old version for reference only. It is excluded from the repository by `.gitignore`.

## Using the panel

1. Opening `/yt/` shows the panel when the session is valid, or redirects to the login form.
2. The form accepts the username and password of the single account from the configuration. A successful login leads to `/yt/`. A failed one shows the same message for a wrong username and a wrong password.
3. Once opened, the panel sends one request for statistics. The user can repeat it with the "Odśwież" (Refresh) button or reload the page. The panel never refreshes data on its own.
4. The panel shows the channel's total views, the subscriber count, the title and view count of every video, and the time of the last successful fetch in the `Europe/Warsaw` time zone. The title links to YouTube. When the API hides a counter, the panel shows "Niedostępne" (Unavailable).
5. While fetching, the panel shows a loading state. When YouTube fails, it shows the last available result marked as coming from the cache, with its original fetch time. Without a stored result it shows a message and lets the user retry.
6. "Odśwież" within 60 seconds of the last fetch returns cached data. This is intended; the displayed fetch time shows how old the data is.

## Look and responsiveness

- The interface is redesigned and clearly more modern than the current page. All text is in Polish and the document has `lang="pl"`.
- The page works without horizontal scrolling from 360 px wide. We check it at 375 px (phone), 768 px (tablet) and 1280 px (desktop), and on a real phone.
- Order from the top: a header with the channel's avatar and name and the "Wyloguj" (Log out) button, the list of videos, and below it a smaller summary panel.
- The summary panel shows subscribers, channel views, the number of videos in the list, the fetch time and the "Odśwież" button. It sticks to the bottom of the screen (it does not scroll with the list) and takes its own place under the list at the end of the page.
- The colours refer to yarn: navy, the yellow of a ball of yarn and a warm, woolly background. The channel's avatar is shown as a circle and looks good in both themes.
- From 768 px the video list is a table with "Tytuł" (Title) and "Wyświetlenia" (Views) columns; numbers are right-aligned with tabular digits. Below 768 px each video is a card row with the title and the view count under it.
- The default order is the order of the uploads playlist (newest first). The user can switch to sorting by view count, descending. Sorting happens in the browser and sends no new request.
- Numbers are formatted in the browser using `pl-PL` (e.g. `12 345`). The JSON keeps them as digit strings.
- Colours follow `prefers-color-scheme` (light and dark themes). Text contrast meets WCAG AA, keyboard focus is visible, touch targets are at least 44 × 44 px.
- The loading state disables the "Odśwież" button. Status messages (loading, cached data, error) are announced through `aria-live`.
- The login form has the same style and is comfortable to use on a phone.
- The page loads no resources from external servers (CDN, fonts, scripts).

## Login and session

- The "Zapamiętaj mnie na 90 dni" (Remember me for 90 days) option stores a persistent cookie and a session with the same expiry, counted from login. Using the panel does not extend the session. The session survives an application restart.
- Without the option the browser gets a session cookie, and the server-side session is limited to 12 hours from login. The browser may restore such a cookie after a restart, depending on its session-restore settings.
- The cookie holds a random 32-byte identifier encoded as base64url. Go stores only the SHA-256 hash of the identifier with its expiry, in a private file. The cookie has `Secure`, `HttpOnly`, `SameSite=Lax` and `Path=/yt/`, and no `Domain` attribute.
- "Wyloguj" invalidates the current session on the server and deletes the cookie. Expired entries are removed whenever the session file is modified.
- The `yt-stats revoke-sessions` command removes all sessions (e.g. after a password change). It is run while the service is stopped. The service holds a lock (`flock`) on the session file, so the command fails and changes nothing when the service is running.
- Access to the panel and the data requires a session. The login and logout forms are protected against CSRF.
- For an unknown username the application verifies the password against a fixed dummy Argon2id hash, so the response time does not reveal whether the username is correct.
- The Argon2id password hash and the YouTube API keys live in the configuration, outside the public directory. Responses and logs never reveal the password, the API keys or session identifiers. Login pages and protected responses have `Cache-Control: no-store`.
- The client address used for limits comes from `X-Real-IP` only when the connection comes from `127.0.0.1` or `::1`. Otherwise the application uses the connection address. Nginx fills `X-Real-IP` with the address from `CF-Connecting-IP` (see "Environment").

## Limits

| Area | Limit |
| --- | --- |
| Failed logins from one IP | 5 within 15 minutes, then `429` with `Retry-After`. A successful login resets the counter. |
| Failed logins in total | 50 per hour, then `429` for everyone until the window ends. |
| Concurrent password verifications | 2; further ones wait up to 3 s, then `503`. |
| IP addresses remembered by the limiter | 10,000; the oldest are removed. |
| Form size | 4 KiB; username up to 64 bytes, password up to 256 bytes. |
| Argon2id parameters | m = 19 MiB, t = 2, p = 1, 16 B salt, 32 B key (OWASP minimum). |
| Pre-login sessions (CSRF) | in memory, valid for 30 minutes, at most 1,000. |
| Active sessions in the file | at most 100; when exceeded, the oldest is removed. A session file larger than 1 MiB is rejected on read. |
| Single YouTube request | 10 s, response up to 5 MiB. |
| Whole statistics refresh | 30 s, at most 200 playlist pages (10,000 videos). |
| HTTP server | `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `WriteTimeout` 40 s, `IdleTimeout` 60 s, headers up to 16 KiB. |
| Service memory | `MemoryMax=128M` in systemd; the limit can be lowered after measuring on Mikrus. Expected idle RSS below 30 MB. |

## Fetching data

- The channel ID comes from the configuration (`channel_id`), so anyone can deploy the panel for their own channel. For @YarnPowerPL it is `UC5711W83ebIxkC8bHjOKI7g` (the old version prepended `UC` in `functions.php`). Before deployment we confirm it with a `channels.list?forHandle=@handle` query.
- The channel's name, handle (`customUrl`) and avatar come from the same `channels.list` request (`part=snippet`), so they cost no extra quota. The header and the login page show them; until the first successful fetch they show a generic title and the bundled `logo.png`.
- The avatar is downloaded by the server (only over HTTPS from Google image hosts `ggpht.com`, `googleusercontent.com`, `ytimg.com`, up to 1 MiB, JPEG/PNG/WebP/GIF recognised by content, without the API key) and served from `/yt/channel-logo`, so the page still loads nothing from external servers. It is downloaded again only when its address changes. A failed download does not fail the refresh.
- Right after startup the application fetches the data once, so the login page knows the channel's name and avatar. After that, as before, YouTube is queried only after an authorised request.
- Go queries YouTube Data API v3 after an authorised request if the last successful read is older than 60 seconds. Concurrent refreshes share one fetch.
- The application reads the channel statistics and the ID of the uploads playlist. It handles every page of the playlist (the old version fetched only the first 50 videos) and fetches video statistics in batches of 50. Private and deleted videos are skipped when the API does not return their details.
- After an error the application returns the last successful result up to 24 hours old, with status `200` and `stale: true`. For 30 seconds after an error further requests use the same state instead of retrying YouTube immediately. No result within that window means a `503` response.
- The statistics cache lives in the memory of a single process. A restart may clear the cache, and the first request fetches the data again. The session file is persistent and independent of the cache.

### Fallback API key

- The configuration has a primary key and an optional fallback key.
- When YouTube answers a request made with the primary key with `403` and reason `quotaExceeded` or `dailyLimitExceeded`, the application retries the same request with the fallback key and keeps using it until the next midnight Pacific time (`America/Los_Angeles`), when YouTube resets the quota. After that it returns to the primary key.
- Other errors (invalid key, 5xx, timeout, access denied) do not switch keys.
- When the fallback key also runs out of quota, the refresh fails and the usual failure handling applies (stale data or `503`).
- Logs say which key was used (`primary` / `fallback`) and when the switch happened, never the key values.

## HTTP interface

| Method and path | Behaviour |
| --- | --- |
| `GET /yt/` | The panel, or `303` to `/yt/login`. |
| `GET /yt/login` | The login form; with a valid session `303` to `/yt/`. |
| `POST /yt/login` | Verifies the password, creates a session and responds `303` to `/yt/` on success. |
| `POST /yt/logout` | Invalidates the session and responds `303` to `/yt/login`. |
| `GET /yt/stat.json` | Statistics, or `401` without a valid session. |
| `GET /yt/script.js`, `GET /yt/style.css`, `GET /yt/logo.png`, `GET /yt/favicon.png` | Static files, available without a session. |
| `GET /yt/channel-logo` | The channel's avatar, available without a session (the login page shows it); `404` until it is downloaded. Versioned like static files. |

- Every `GET` route also handles `HEAD`. Any other method on a known path returns `405` with an `Allow` header. An unknown path returns `404`.
- The JSON keeps the existing fields `stats.subscriberCount`, `stats.viewsCount`, `videos[].videoId`, `videos[].title`, `videos[].viewCount` and `update_time`. Counters stay digit strings. A missing counter is `null`. The `stale` field says whether the result comes from the fallback read. The `channel` field holds `title`, `handle` (may be missing), `url` and `logoVersion` (missing without an avatar).
- `update_time` is ISO 8601 with the `Europe/Warsaw` offset, e.g. `2026-11-23T22:45:10+01:00`. The browser shows it in the `Europe/Warsaw` zone regardless of the device's zone.
- On error the endpoint returns `{"error": "<short message>"}` with no internal details. After a `401` the script redirects to the login form.
- All responses have the headers `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`. Nginx sets HSTS.

### Interface files

- The application is a single Go binary. The HTML templates (`index.html`, `login.html`) and the files `script.js`, `style.css`, `logo.png` and `favicon.png` sit next to it in the directory set in the configuration (`web_dir`) and are not compiled in.
- Changes to these files take effect without recompiling or restarting: the application reloads a template when the file's modification time changes. If the new version of a template has a syntax error, the application logs the error and keeps using the last valid version.
- At startup a missing or invalid file stops the application with a message naming the file.
- Templates refer to static files through `{{asset "name"}}`, which produces a versioned URL such as `/yt/script.js?v=1a2b…` (a content hash). Changing the file changes the URL, so neither the browser nor Cloudflare serves a stale copy. The URL with the current version has `Cache-Control: public, max-age=31536000, immutable`; a URL with no version or an outdated one has `no-cache`.

## Environment

The application runs as a single lightweight Go process under `systemd`. The domain is behind Cloudflare: HTTPS ends at Cloudflare, which connects to Nginx over HTTP. Nginx forwards `/yt/` traffic to `127.0.0.1:8091`, keeping the prefix, and sets `X-Real-IP` to the real client address from `CF-Connecting-IP`, accepted only from Cloudflare addresses (the `real_ip` module). After the switch we purge the Cloudflare cache for the old `/yt/` files. The Linux binary is built on a Mac. The binary embeds the time zone database (`time/tzdata`), so it does not depend on `/usr/share/zoneinfo` on the server. The application needs no database. Idle memory use, memory use during login and fetching, and build time are measured before acceptance.

Locally the application is tested over plain HTTP at `http://localhost:8091/yt/` in Chrome or Firefox. These browsers treat `localhost` as a secure context and accept a cookie with the `Secure` attribute. Safari does not, so we do not use it locally.

## Acceptance criteria

- First login, returning without a password with the persistent cookie, a service restart, expiry of both kinds of session, logout and `revoke-sessions`.
- No access to the panel or `stat.json` without a session. Requests to the old data files do not bypass login.
- A form with an invalid CSRF token is rejected; the attempt limit (`429`) and the concurrent verification limit work.
- The full set of security headers and `Cache-Control: no-store` on protected pages.
- A complete video list (the count matches the channel minus private and deleted videos) and API failure handling, including the switch to the fallback key.
- The look at 375, 768 and 1280 px and on a real phone, in light and dark themes, without horizontal scrolling.
- A change to a file in `web_dir` is visible after reloading the page, without restarting the service.
- Logs without secrets; RSS and build time measured.

## Technical sources

- [YouTube Data API v3 — retrieving a list of videos](https://developers.google.com/youtube/data/guides/implementation/videos)
- [YouTube Data API v3 — retrieving details of multiple videos](https://developers.google.com/youtube/v3/docs/videos/list)
- [YouTube Data API v3 — errors and quotas](https://developers.google.com/youtube/v3/docs/errors)
- [OWASP — session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- [OWASP — password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
