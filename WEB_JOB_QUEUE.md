# NZBreX Web Server Job Queue

## Overview
With `-web`, NZBreX runs a small web server: upload NZB files, queue them, start
and stop them, and watch progress and provider statistics.

Jobs run **one at a time**, oldest first. Every job runs with the settings
NZBreX was started with (see "Per-job settings" below).

## Features
- **Queue:** upload NZB files (`.nzb`, `.nzb.gz`, max 128 MiB); each becomes a queued job.
- **Control:** start one job or all queued jobs, stop a running job, cancel a
  waiting one, delete queued or finished jobs.
- **Progress:** live article counts per job, provider statistics.
- **Login:** HTTP basic auth against its own passwd file (`-webpasswd`).

### Per-job settings (not supported yet)
The jobs page shows a per-job and a default settings dialog (Check Only, Check
First, CRC32, yEnc test mode, yEnc CPU threads, cache directory). They are
**read-only**: every job runs with the startup flags (`-checkonly`,
`-checkfirst`, `-crc32`, `-yenctest`, `-yenccpu`, `-cd`, ...). The dialogs and
each job's `Config` show those values.

The config endpoints return `501 Not Implemented` (with the startup settings in
the body), and `/upload` ignores config fields in the form and says so in a
`note` field of its response. Applying settings per job is planned; it needs the
engine to stop reading the process-wide configuration first.

## Job states

```
queued ──start──▶ waiting ──(runner)──▶ running ──▶ completed
   │                 │                     │    └─▶ failed (error, or "stopped")
   │                 └──stop──▶ failed ("canceled before it started")
   └──delete
```

- `queued`: uploaded, not started.
- `waiting`: started, waiting for the job before it to finish.
- `running`: its session is running. Only one job runs at a time.
- `completed` / `failed`: finished. `Error` says why a job failed; a stopped job
  fails with `stopped`.

Queued, completed and failed jobs can be deleted; waiting and running ones must
be stopped first (`409 Conflict` otherwise). At most 1000 queued/waiting jobs
are accepted (`503` after that); only the newest 50 finished jobs are kept.

An uploaded NZB is deleted when its job finishes or is deleted. Uploads go to a
private temporary directory (mode 0700), removed on shutdown.

## API

### Pages
- `GET /`: upload page
- `GET /jobs`: job queue (refreshes every 3 s)
- `GET /stats`: statistics (refreshes every 5 s)
- `GET /static/...`: CSS and JS (served locally, no CDN)

### Routes
| Method | Path | |
|---|---|---|
| `POST` | `/upload` | upload an NZB (form field `file`), returns the job ID |
| `GET` | `/api/jobs` | all jobs: `{"jobs": [...]}` (running, waiting, queued, finished) |
| `POST` | `/api/jobs/:id/start` | start a queued job (it becomes `waiting`) |
| `POST` | `/api/jobs/start-all` | start every queued job; returns `count` |
| `POST` | `/api/jobs/:id/stop` | stop a running job, cancel a waiting one |
| `DELETE` | `/api/jobs/:id` | delete a queued or finished job |
| `PATCH` | `/api/jobs/:id/config` | `501` (per-job settings not supported) |
| `GET`/`PUT` | `/api/jobs/default-config` | `501` (same) |
| `GET` | `/api/stats` | statistics (JSON) |

Errors: `404` unknown job, `409` wrong state, `503` queue full, `413` upload too
large, `400` not an NZB or an NZB that doesn't parse.

### Job JSON
```go
type QueuedJob struct {
    ID                string    // "job-1", "job-2", ...
    NZBPath           string    // the uploaded file (deleted when the job finishes)
    NZBName           string    // original file name
    Config            JobConfig // the settings it runs with: always the startup flags
    Status            string    // queued, waiting, running, completed, failed
    AddedAt           time.Time
    StartedAt         time.Time // when it began running
    FinishedAt        time.Time
    Error             string    // why it failed
    Progress          float64   // 0..1: articles_processed / total_articles
    TotalArticles     uint64    `json:"total_articles"`
    ArticlesProcessed uint64    `json:"articles_processed"` // ok on all providers, dead, or nowhere to upload
    ArticlesDone      uint64    `json:"articles_done"`      // ok on all providers, or uploaded
    ArticlesCached    uint64    `json:"articles_cached"`
    ArticlesDead      uint64    `json:"articles_dead"`
    TotalSize         int64     `json:"total_size"` // bytes
    SessionID         uint64    `json:"session_id"` // the job's session, once it ran
}

type JobConfig struct {
    CheckOnly, CheckFirst, YencCRC bool
    YencTest, YencCpu              int
    Cachedir                       string
}
```

The counters update while the job runs and keep their final values afterwards.

`/api/stats` lists the providers of the running job's session, with its
connection and article counts. While no job runs, it lists the providers loaded
at startup.

## Usage

### Start the web server
```bash
# local only, no login (allowed only on a loopback address; a warning is logged)
./NZBreX -web -webaddr "127.0.0.1:11911" -cd /cache -provider provider.json

# with login: create a web user first (its own file, not the proxy's .proxypasswd)
./NZBreX -webpasswd /etc/nzbrex/webpasswd -webadduser 'webadmin01|a-long-password'
./NZBreX -web -webaddr "127.0.0.1:11911" -webpasswd /etc/nzbrex/webpasswd -cd /cache -provider provider.json
```

Without `-webpasswd` the server refuses to start on anything but a loopback
address. To reach it from elsewhere, set `-webpasswd`, prefer `-webssl` (TLS 1.2+,
`-webssl-crt`, `-webssl-key`), and list the host names it is reached by in
`-webhosts`. Exposing it directly on the internet is not recommended: it drives
your provider accounts.

User names and passwords need at least 10 characters; `-webadduser 'auto|auto'`
generates both and prints them. Users added with `-webadduser` (or edits to the
file) are picked up while the server runs. Web users don't expire unless their
ExpireAt field is set by hand.

`-web` can't be combined with `-nzbdir` or the proxy (`-proxytcp`/`-proxytls`);
NZBreX exits with an error. With `-log`, each job writes its own log file
(`<logdir>/<upload number>-<name>.log`). The log is process-wide, so web server lines
logged while a job runs land in that job's log.

On SIGINT/SIGTERM the server stops accepting requests, stops the running job,
cancels waiting ones and exits (a second signal exits at once).

### API (cURL)
Every request that changes something (POST, PUT, PATCH, DELETE) needs the header
`X-NZBreX-Request: 1`; browsers can't send it cross-site, which blocks CSRF.
With a login, add `-u user:password`.
```bash
# Upload (max 128 MiB). Config fields in the form are ignored (see above).
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" \
  -F "file=@myfile.nzb" http://127.0.0.1:11911/upload

# List all jobs
curl -u webadmin01:a-long-password http://127.0.0.1:11911/api/jobs

# Start one job, or all queued jobs (they run one after another)
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X POST http://127.0.0.1:11911/api/jobs/job-1/start
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X POST http://127.0.0.1:11911/api/jobs/start-all

# Stop a running job / cancel a waiting one
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X POST http://127.0.0.1:11911/api/jobs/job-1/stop

# Delete a queued or finished job
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X DELETE http://127.0.0.1:11911/api/jobs/job-1
```

## Web pages

### Upload (`/`)
Drag-and-drop or file picker for `.nzb` / `.nzb.gz`, upload progress, links to
the queue and statistics.

### Job queue (`/jobs`)
A table of all jobs: name, article counts and size, status badge, timeline
(added / started / finished), settings. Per job:
- ▶ Start (queued)
- ■ Stop (waiting or running)
- 🗑 Delete (queued or finished)
- ⚙ Settings: shown disabled (not supported yet)

"Start All Queued Jobs" starts every queued job; "Default Job Config" shows the
startup settings read-only.

### Statistics (`/stats`)
Provider connections, article counts and speeds, bandwidth, and the running
job's progress.

## Thread safety
- One mutex guards all job fields. The API returns copies, never shared pointers.
- One runner goroutine runs jobs in queue order, because sessions still share
  process-wide settings and limiters.
- Each job runs in its own session; its progress counters are atomics the page
  reads without locking the session.

## Files
- `webserver.go`: server, routes, handlers, statistics
- `webjobs.go`: job queue and runner
- `webauth.go`: login, Host check, CSRF guard
- `passwd.go`: passwd file format (shared with the proxy)
- `web/templates/index.html`, `jobs.html`, `stats.html`: pages
- `web/static/`: `style.css`, Bootstrap 5.3.5 (`bootstrap.min.css`, `bootstrap.bundle.min.js`)
