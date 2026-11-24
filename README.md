# yt-stats

**English** | [Polski](README_pl.md)

A private statistics panel for the YouTube channel [@YarnPowerPL](https://www.youtube.com/@YarnPowerPL), available at `https://maciejgad.pl/yt/` after logging in.

It shows the list of videos with their view counts (sorted by newest or most popular) and a channel summary: subscribers, views and the number of videos. The data comes from YouTube Data API v3, with a fallback API key for when the quota runs out. The interface is in Polish.

## Design

- A single Go binary (`cmd/yt-stats`) with no external services and no database.
- The HTML templates, CSS and JavaScript live separately in `web/` — they can be changed without recompiling, and static file URLs are versioned automatically.
- Password login (Argon2id), sessions in a JSON file, CSRF protection and a login attempt limit.
- On the server: a systemd service behind Nginx and Cloudflare.

Details: [requirements.md](requirements.md), [plan.md](plan.md), [deploy/README.md](deploy/README.md).

## Running locally

```bash
cp config.example.json config.json   # fill in the fields
go run ./cmd/yt-stats hash-password  # password hash for config.json
go run ./cmd/yt-stats -config config.json
```

Tests:

```bash
go test ./...
node --test test/js/*.test.js
```

## How this project was made

The code, tests, deployment scripts and documentation were written by [Claude Code](https://claude.com/claude-code) with the **Claude Opus 5.5** model.

The human's role was:

- defining the requirements and making the review decisions ([requirements.md](requirements.md)),
- setting the step-by-step work plan ([plan.md](plan.md)),
- verifying every step — reviewing the changes, running the tests and manual scenarios, and accepting before each commit,
- deploying to the server and switching the traffic over.
