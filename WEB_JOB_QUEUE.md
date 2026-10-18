# NZBreX Web Server Job Queue Implementation

## Overview
The web server now includes a job queue system that allows users to upload NZB files, configure processing options, and manage job execution.

## Features

### Job Queue System
- **Queue Management**: Upload NZB files and add them to a processing queue
- **Job Configuration**: Set per-job options for processing
- **Job Control**: Start, stop, and delete jobs
- **Status Tracking**: Monitor job progress and status

### Configurable Options (Per Job)
- `-checkonly`: Check article availability only (no download/upload)
- `-checkfirst`: Check all articles before starting downloads
- `-crc32`: Enable CRC32 verification for articles
- `-yenctest`: Select yenc decoding mode (1-4)
- `-yenccpu`: Set CPU threads for yenc processing
- `-cd`: Cache directory path

## API Endpoints

### Web Pages
- `GET /` - Upload page with file drop zone
- `GET /jobs` - Job queue management page
- `GET /stats` - Statistics dashboard

### API Routes
- `POST /upload` - Upload NZB file and add to queue
- `GET /api/jobs` - List all jobs (queued + running)
- `POST /api/jobs/:id/start` - Start a queued job
- `DELETE /api/jobs/:id` - Delete a queued job
- `PATCH /api/jobs/:id/config` - Update job configuration
- `GET /api/stats` - Get statistics (JSON)

## Data Structures

### JobQueue
Manages all queued and running jobs with thread-safe operations.

### QueuedJob
```go
type QueuedJob struct {
    ID         string        // Unique job identifier
    NZBPath    string        // Path to uploaded NZB file
    NZBName    string        // Original filename
    Config     JobConfig     // Job-specific configuration
    Status     string        // "queued", "running", "completed", "failed"
    AddedAt    time.Time     // When job was added
    StartedAt  time.Time     // When job started processing
    FinishedAt time.Time     // When job finished
    Error      string        // Error message if failed
    Progress   float64       // Progress percentage (0-100)
}
```

### JobConfig
```go
type JobConfig struct {
    CheckOnly  bool   // Only check availability
    CheckFirst bool   // Check all before downloading
    YencCRC    bool   // Enable CRC32 verification
    YencTest   int    // Yenc mode (1-4)
    YencCpu    int    // CPU threads (0=auto)
    Cachedir   string // Cache directory
}
```

## Workflow

### 1. Upload NZB File
User uploads an NZB file via web interface:
- File is validated (.nzb or .nzb.gz)
- File is saved to temporary directory
- Job is created with default or specified configuration
- Job is added to queue with status="queued"
- Job ID is returned to user

### 2. Configure Job (Optional)
User can modify job configuration before starting:
- Update any config options via API
- Configuration is validated
- Only queued jobs can be configured

### 3. Start Job
User starts a queued job:
- Job moves from queue to running state
- Processor launches session with job-specific config
- Job runs in background goroutine
- Status updates to "running"

### 4. Monitor Progress
User can view job status:
- Auto-refreshing job list (3 second intervals)
- Status badges (queued/running/completed/failed)
- Configuration summary displayed
- Error messages shown if failed

### 5. Job Completion
When job finishes:
- Status updates to "completed" or "failed"
- Job is removed from running map
- Timestamp recorded
- Original config restored

## Usage Examples

### Start Web Server
```bash
# local only, no login (allowed only on a loopback address; a warning is logged)
./NZBreX -web -webaddr "127.0.0.1:11911" -cd /cache -provider provider.json

# with login: create a web user first (its own file, not the proxy's .proxypasswd)
./NZBreX -webpasswd /etc/nzbrex/webpasswd -webadduser 'webadmin01|a-long-password'
./NZBreX -web -webaddr "127.0.0.1:11911" -webpasswd /etc/nzbrex/webpasswd -cd /cache -provider provider.json
```

Without `-webpasswd` the server refuses to start on anything but a loopback
address. To reach it from elsewhere, set `-webpasswd`, prefer `-webssl` (TLS 1.2+),
and list the host names it is reached by in `-webhosts`. Exposing it directly on
the internet is not recommended: it drives your provider accounts.

### API (cURL)
Every request that changes something (POST, PUT, PATCH, DELETE) needs the header
`X-NZBreX-Request: 1`; browsers can't send it cross-site, which blocks CSRF.
With a login, add `-u user:password`.
```bash
# Upload (max 128 MiB). Per-job settings are not supported yet: jobs run with
# the startup settings, and config fields in the form are ignored.
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" \
  -F "file=@myfile.nzb" http://127.0.0.1:11911/upload

# List all jobs
curl -u webadmin01:a-long-password http://127.0.0.1:11911/api/jobs

# Start one job, or all queued jobs (they run one after another)
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X POST http://127.0.0.1:11911/api/jobs/job-1/start
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X POST http://127.0.0.1:11911/api/jobs/start-all

# Delete a queued or finished job
curl -u webadmin01:a-long-password -H "X-NZBreX-Request: 1" -X DELETE http://127.0.0.1:11911/api/jobs/job-1
```

## Web Interface Features

### Upload Page (/)
- Drag-and-drop file upload
- File validation (.nzb, .nzb.gz)
- Upload progress indicator
- Links to job queue and statistics

### Job Queue Page (/jobs)
- Table view of all jobs
- Status badges with color coding
- Configuration summary per job
- Action buttons:
  - ▶ Start (queued jobs only)
  - ⚙ Config (queued jobs only)
  - 🗑 Delete (queued jobs only)
- Auto-refresh every 3 seconds
- Modal dialog for configuration

### Statistics Page (/stats)
- Real-time provider statistics
- Bandwidth visualization
- Job progress tracking
- Auto-refresh every 5 seconds

## Thread Safety
- All job queue operations use mutex locks
- Safe concurrent access to job data
- Original config is restored after job completion
- Job-specific sessions prevent conflicts

## Error Handling
- File upload validation
- Job not found errors
- Cannot delete running jobs
- Cannot configure non-queued jobs
- Configuration validation
- Session launch errors captured

## Files Modified/Created

### Modified
- `webserver.go` - Added job queue system and handlers
- `Flags.go` - Added SSL/TLS flags for web server
- `web/templates/index.html` - Added job queue link
- `web/templates/stats.html` - Added job queue link

### Created
- `web/templates/jobs.html` - Job queue management page

## Benefits
1. **Better Control**: Queue jobs and start them when ready
2. **Per-Job Configuration**: Different settings for different NZBs
3. **Monitoring**: Track job status and progress
4. **Safety**: Can't accidentally overload system with simultaneous jobs
5. **Flexibility**: Mix check-only and full processing jobs
6. **Reliability**: Failed jobs don't block the queue
