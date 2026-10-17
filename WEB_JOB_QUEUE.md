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
# HTTP (default)
./NZBreX -web -webaddr "127.0.0.1:11911" -cd /cache -provider provider.json

# HTTPS
./NZBreX -web -webssl \
  -webssl-crt "/etc/letsencrypt/fullchain.pem" \
  -webssl-key "/etc/letsencrypt/privkey.pem" \
  -webaddr ":443" -cd /cache -provider provider.json
```

### Upload File with Configuration (cURL)
```bash
# Upload with default config
curl -F "file=@myfile.nzb" http://127.0.0.1:11911/upload

# Upload with custom config
curl -F "file=@myfile.nzb" \
  -F "checkonly=true" \
  -F "crc32=true" \
  -F "cachedir=/custom/cache" \
  http://127.0.0.1:11911/upload
```

### Manage Jobs (API)
```bash
# List all jobs
curl http://127.0.0.1:11911/api/jobs

# Start job
curl -X POST http://127.0.0.1:11911/api/jobs/job-1/start

# Update job config
curl -X PATCH http://127.0.0.1:11911/api/jobs/job-1/config \
  -H "Content-Type: application/json" \
  -d '{"CheckOnly":true,"YencCRC":true}'

# Delete job
curl -X DELETE http://127.0.0.1:11911/api/jobs/job-1
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
