package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"
)

// Job states. A job moves queued → waiting → running → completed/failed.
// waiting: started, but the jobs before it are still running.
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

// JobQueue manages the web jobs. One runner goroutine runs them one at a time,
// oldest waiting job first, because sessions still share the global cfg.
// (A goroutine per started job racing for a mutex ran them in random order.)
//
// Locking: jq.mu guards every job field. Readers get copies (ListJobs, GetJob),
// never pointers. Lock order: jq.mu before PROCESSOR.mux (runJob calls
// newSession under jq.mu); nothing takes jq.mu while holding PROCESSOR.mux.
type JobQueue struct {
	mu     sync.RWMutex
	jobs   []*QueuedJob // every job, in the order it was added
	nextID int
	wake   chan struct{} // a job became waiting
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

	session       *SESSION // set when the job starts running; the job's own session, never matched by path
	stopRequested bool     // StopJob was called while it ran
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
	jq := &JobQueue{nextID: 1, wake: make(chan struct{}, 1)}
	go jq.runner()
	return jq
}

// runner runs waiting jobs in the order they were added, one at a time.
func (jq *JobQueue) runner() {
	for range jq.wake {
		for {
			job := jq.nextWaiting()
			if job == nil {
				break
			}
			jq.runJob(job)
		}
	}
}

// nextWaiting returns the oldest waiting job, or nil.
func (jq *JobQueue) nextWaiting() *QueuedJob {
	jq.mu.RLock()
	defer jq.mu.RUnlock()
	for _, j := range jq.jobs {
		if j.Status == jobWaiting {
			return j
		}
	}
	return nil
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
	// the segments in the NZB, as the session counts them (TotalSegments is
	// what the subjects claim)
	totalArticles := uint64(nzbfile.Segments)
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
	// done = on every provider, confirmed or uploaded by the session
	done, dead, notarget := p.done.Load(), p.dead.Load(), p.notarget.Load()
	finished := min(done+dead+notarget, total)
	j.TotalArticles = total
	j.ArticlesDone = min(done, total)
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

// StartJob moves a queued job to waiting. The runner starts it when the jobs
// before it are done.
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
	select {
	case jq.wake <- struct{}{}:
	default: // the runner is already awake
	}
	return nil
}

// runJob runs the job's session and records the result. Only the runner calls it.
func (jq *JobQueue) runJob(job *QueuedJob) {
	jq.mu.Lock()
	if job.Status != jobWaiting {
		jq.mu.Unlock() // canceled since nextWaiting
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
	if err == nil && job.stopRequested {
		err = errors.New("stopped")
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
	removeUpload(job.NZBPath) // the session is done with it

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

// StopJob cancels a waiting job, or stops a running one: its session drops the
// work that is left and ends as usual; the job ends as failed "stopped".
func (jq *JobQueue) StopJob(jobID string) error {
	jq.mu.Lock()
	_, job := jq.find(jobID)
	if job == nil {
		jq.mu.Unlock()
		return fmt.Errorf("%w: %s", errJobNotFound, jobID)
	}
	switch job.Status {
	case jobWaiting:
		jq.finishLocked(job, errors.New("canceled before it started")) // the runner skips it
		jq.mu.Unlock()
		return nil
	case jobRunning:
		job.stopRequested = true
		s := job.session
		jq.mu.Unlock()
		if s != nil {
			s.stop()
		}
		return nil
	}
	status := job.Status
	jq.mu.Unlock()
	return fmt.Errorf("%w: %s is %s, only waiting or running jobs can be stopped", errJobState, jobID, status)
}

// stopAll cancels waiting jobs and stops running ones (shutdown).
func (jq *JobQueue) stopAll() {
	jq.mu.RLock()
	var ids []string
	for _, j := range jq.jobs {
		if j.Status == jobWaiting || j.Status == jobRunning {
			ids = append(ids, j.ID)
		}
	}
	jq.mu.RUnlock()
	for _, id := range ids {
		jq.StopJob(id)
	}
}

// waitIdle waits until no job is running, or until timeout. It reports whether none is.
func (jq *JobQueue) waitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if len(jq.runningJobs()) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
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
	removeUpload(job.NZBPath)
	return nil
}

// removeUpload deletes an uploaded NZB. Only files in webUploadDir are touched.
func removeUpload(path string) {
	if webUploadDir == "" || filepath.Dir(path) != webUploadDir {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		dlog(always, "WARN: removing upload %s: %v", logQuote(path), err)
	}
}
