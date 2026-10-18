package main

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// Job states. A job moves queued → waiting → running → completed/failed.
// waiting: started, but another job still holds processingMu.
const (
	jobQueued    = "queued"
	jobWaiting   = "waiting"
	jobRunning   = "running"
	jobCompleted = "completed"
	jobFailed    = "failed"
)

const (
	maxPendingJobs  = 1000 // queued + waiting; AddJob refuses more
	maxFinishedJobs = 50   // finished jobs kept for display; older ones are dropped
)

var (
	errJobNotFound = errors.New("job not found")
	errJobState    = errors.New("job is in the wrong state")
	errQueueFull   = errors.New("job queue is full")
)

// JobQueue manages the web jobs. Jobs run one at a time (processingMu), because
// sessions still share the global cfg.
//
// Locking: jq.mu guards every job field. Readers get copies (ListJobs, GetJob),
// never pointers. Lock order: jq.mu before PROCESSOR.mux (runJob calls
// newSession under jq.mu); nothing takes jq.mu while holding PROCESSOR.mux.
type JobQueue struct {
	mu           sync.RWMutex
	jobs         []*QueuedJob // every job, in the order it was added
	nextID       int
	processingMu sync.Mutex // only one job runs at a time
}

// QueuedJob is one uploaded NZB. The exported fields are the JSON the pages read.
type QueuedJob struct {
	ID                string
	NZBPath           string
	NZBName           string
	Config            JobConfig // the settings the job runs with: always the startup flags (D2)
	Status            string
	AddedAt           time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	Error             string
	Progress          float64 // 0..1: finished articles / total
	TotalArticles     uint64  `json:"total_articles"`
	ArticlesProcessed uint64  `json:"articles_processed"` // finished: ok on all providers, dead, or nowhere to upload
	ArticlesDone      uint64  `json:"articles_done"`      // ok on all providers, or uploaded
	ArticlesCached    uint64  `json:"articles_cached"`
	ArticlesDead      uint64  `json:"articles_dead"`
	TotalSize         int64   `json:"total_size"` // bytes
	SessionID         uint64  `json:"session_id"`

	session *SESSION // set when the job starts running; the job's own session, never matched by path
}

// JobConfig holds per-job settings. They are not applied yet (D2): every job
// runs with the settings the process was started with.
type JobConfig struct {
	CheckOnly  bool
	CheckFirst bool
	YencCRC    bool
	YencTest   int
	YencCpu    int
	Cachedir   string
}

func NewJobQueue() *JobQueue {
	return &JobQueue{nextID: 1}
}

// startupJobConfig reports the settings every job runs with.
func startupJobConfig() JobConfig {
	return JobConfig{
		CheckOnly:  cfg.opt.CheckOnly,
		CheckFirst: cfg.opt.CheckFirst,
		YencCRC:    cfg.opt.YencCRC,
		YencTest:   cfg.opt.YencTest,
		YencCpu:    cfg.opt.YencCpu,
		Cachedir:   cfg.opt.Cachedir,
	}
}

// AddJob parses the NZB and queues it. A file that doesn't parse is rejected.
// The parse runs before jq.mu is taken (it used to hold the lock for the whole parse).
func (jq *JobQueue) AddJob(nzbPath, nzbName string) (string, error) {
	nzbfile, err := loadNzbFile(nzbPath)
	if err != nil || nzbfile == nil {
		if err == nil {
			err = errors.New("empty nzb")
		}
		return "", fmt.Errorf("invalid NZB file: %w", err)
	}
	var totalArticles uint64
	for _, file := range nzbfile.Files {
		totalArticles += uint64(file.TotalSegments)
	}
	job := &QueuedJob{
		NZBPath:       nzbPath,
		NZBName:       nzbName,
		Config:        startupJobConfig(),
		Status:        jobQueued,
		AddedAt:       time.Now(),
		TotalSize:     nzbfile.Bytes,
		TotalArticles: totalArticles,
	}

	jq.mu.Lock()
	defer jq.mu.Unlock()
	pending := 0
	for _, j := range jq.jobs {
		if j.Status == jobQueued || j.Status == jobWaiting {
			pending++
		}
	}
	if pending >= maxPendingJobs {
		return "", fmt.Errorf("%w: %d jobs are waiting", errQueueFull, pending)
	}
	job.ID = fmt.Sprintf("job-%d", jq.nextID)
	jq.nextID++
	jq.jobs = append(jq.jobs, job)
	return job.ID, nil
}

// find returns the job with this ID. Caller holds jq.mu.
func (jq *JobQueue) find(jobID string) (int, *QueuedJob) {
	for i, j := range jq.jobs {
		if j.ID == jobID {
			return i, j
		}
	}
	return -1, nil
}

// GetJob returns a copy of the job.
func (jq *JobQueue) GetJob(jobID string) (QueuedJob, bool) {
	jq.mu.RLock()
	defer jq.mu.RUnlock()
	if _, j := jq.find(jobID); j != nil {
		return j.snapshot(), true
	}
	return QueuedJob{}, false
}

// ListJobs returns copies of all jobs: running, waiting, queued, then finished.
// Running jobs carry their session's live progress. Nothing is written.
func (jq *JobQueue) ListJobs() []QueuedJob {
	jq.mu.RLock()
	defer jq.mu.RUnlock()
	out := make([]QueuedJob, 0, len(jq.jobs))
	for _, status := range []string{jobRunning, jobWaiting, jobQueued, ""} {
		for _, j := range jq.jobs {
			finished := j.Status == jobCompleted || j.Status == jobFailed
			if (status == "" && finished) || j.Status == status {
				out = append(out, j.snapshot())
			}
		}
	}
	return out
}

// runningJobs returns copies of the running jobs.
func (jq *JobQueue) runningJobs() []QueuedJob {
	jq.mu.RLock()
	defer jq.mu.RUnlock()
	var out []QueuedJob
	for _, j := range jq.jobs {
		if j.Status == jobRunning {
			out = append(out, j.snapshot())
		}
	}
	return out
}

// snapshot copies the job; a running job gets its session's current progress.
// Caller holds jq.mu (read is enough: only the copy is changed).
func (j *QueuedJob) snapshot() QueuedJob {
	c := *j
	c.session = nil
	if j.Status == jobRunning && j.session != nil {
		c.setProgress(&j.session.progress)
	}
	return c
}

// setProgress fills the counters from a session's published progress.
func (j *QueuedJob) setProgress(p *sessionProgress) {
	total := p.total.Load()
	if total == 0 {
		return // NZB not loaded yet: keep the counts from AddJob
	}
	// The divider's "done" never counts uploaded items (an upload sets uploadedTo, not
	// availableOn); they end up in notarget instead. ok = on every provider, or uploaded.
	done, dead, notarget := p.done.Load(), p.dead.Load(), p.notarget.Load()
	finished := min(done+dead+notarget, total)
	j.TotalArticles = total
	j.ArticlesDone = min(done+p.uploaded.Load(), total)
	j.ArticlesProcessed = finished
	j.ArticlesCached = p.cached.Load()
	j.ArticlesDead = dead
	j.Progress = float64(finished) / float64(total)
}

// StartAllQueued starts every queued job. They run one after another.
func (jq *JobQueue) StartAllQueued() int {
	jq.mu.Lock()
	var ids []string
	for _, j := range jq.jobs {
		if j.Status == jobQueued {
			ids = append(ids, j.ID)
		}
	}
	jq.mu.Unlock()
	started := 0
	for _, id := range ids {
		if err := jq.StartJob(id); err != nil {
			dlog(always, "Warning: Failed to start job %s: %v", id, err)
		} else {
			started++
		}
	}
	return started
}

// StartJob moves a queued job to waiting and starts its goroutine. The job
// becomes running once it holds processingMu.
func (jq *JobQueue) StartJob(jobID string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	_, job := jq.find(jobID)
	if job == nil {
		return fmt.Errorf("%w: %s", errJobNotFound, jobID)
	}
	if job.Status != jobQueued {
		return fmt.Errorf("%w: %s is %s, not %s", errJobState, jobID, job.Status, jobQueued)
	}
	job.Status = jobWaiting
	go jq.runJob(job)
	return nil
}

// runJob waits for its turn, runs the job's session and records the result.
func (jq *JobQueue) runJob(job *QueuedJob) {
	jq.processingMu.Lock()
	defer jq.processingMu.Unlock()

	jq.mu.Lock()
	if job.Status != jobWaiting {
		jq.mu.Unlock() // removed or canceled while waiting
		return
	}
	if webProcessor == nil {
		jq.finishLocked(job, errors.New("web processor not initialized"))
		jq.mu.Unlock()
		return
	}
	sessId, s := webProcessor.newSession(job.NZBPath)
	job.session = s
	job.SessionID = sessId
	job.Status = jobRunning
	job.StartedAt = time.Now()
	jq.mu.Unlock()

	dlog(cfg.opt.Verbose, "Starting job %s sessId=%d NZB: %s", job.ID, sessId, job.NZBPath)
	err := jq.launch(s)
	if err != nil {
		// LaunchSession's early error returns skip clearSession: don't leave the session in sessMap
		webProcessor.removeSession(sessId, s)
	}

	jq.mu.Lock()
	jq.finishLocked(job, err)
	jq.mu.Unlock()
}

// launch runs the session. A panic on this goroutine fails the job instead of
// killing the web server. Panics on worker goroutines can't be caught here.
func (jq *JobQueue) launch(s *SESSION) (err error) {
	defer func() {
		if r := recover(); r != nil {
			dlog(always, "ERROR: job session %d panicked: %v\n%s", s.sessId, r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	if err := webProcessor.LaunchSession(s, "", nil); err != nil {
		return fmt.Errorf("failed to launch session: %w", err)
	}
	return nil
}

// finishLocked records the job's result and drops the oldest finished jobs
// beyond maxFinishedJobs. Caller holds jq.mu.
func (jq *JobQueue) finishLocked(job *QueuedJob, err error) {
	if job.session != nil {
		job.setProgress(&job.session.progress) // final counts, also when nobody polled
		job.session = nil                      // let the session be collected
	}
	if err != nil {
		job.Status = jobFailed
		job.Error = err.Error()
		dlog(always, "ERROR: Job %s failed: %v", job.ID, err)
	} else {
		job.Status = jobCompleted
		dlog(always, "Job %s completed successfully", job.ID)
	}
	job.FinishedAt = time.Now()

	finished := 0
	for _, j := range jq.jobs {
		if j.Status == jobCompleted || j.Status == jobFailed {
			finished++
		}
	}
	if drop := finished - maxFinishedJobs; drop > 0 {
		kept := jq.jobs[:0]
		for _, j := range jq.jobs {
			if drop > 0 && (j.Status == jobCompleted || j.Status == jobFailed) {
				drop--
				continue
			}
			kept = append(kept, j)
		}
		clear(jq.jobs[len(kept):])
		jq.jobs = kept
	}
}

// DeleteJob removes a queued or finished job.
func (jq *JobQueue) DeleteJob(jobID string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	i, job := jq.find(jobID)
	if job == nil {
		return fmt.Errorf("%w: %s", errJobNotFound, jobID)
	}
	switch job.Status {
	case jobQueued, jobCompleted, jobFailed:
	default:
		return fmt.Errorf("%w: cannot delete %s job %s", errJobState, job.Status, jobID)
	}
	jq.jobs = append(jq.jobs[:i], jq.jobs[i+1:]...)
	return nil
}
