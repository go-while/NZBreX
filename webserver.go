package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

//go:embed web/templates/*
var templatesFS embed.FS

var (
	webServerEnabled bool
	webServerAddr    string
	webServerSSL     bool
	webServerCert    string
	webServerKey     string
	webProcessor     *PROCESSOR
	webWg            *sync.WaitGroup
	jobQueue         *JobQueue
)

// JobQueue manages queued NZB processing jobs
type JobQueue struct {
	mu            sync.RWMutex
	jobs          []*QueuedJob
	running       map[string]*QueuedJob // jobID -> job
	completed     []*QueuedJob          // completed/failed jobs
	nextID        int
	processingMu  sync.Mutex // Ensures only one job processes at a time (prevents global cfg conflicts)
	defaultConfig JobConfig  // Default configuration for new jobs
}

// QueuedJob represents a job in the queue
type QueuedJob struct {
	ID                string
	NZBPath           string
	NZBName           string
	Config            JobConfig
	Status            string // "queued", "running", "completed", "failed"
	AddedAt           time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	Error             string
	Progress          float64
	TotalArticles     uint64 `json:"total_articles"`
	ArticlesProcessed uint64 `json:"articles_processed"` // done count
	ArticlesDone      uint64 `json:"articles_done"`      // done count (same as ArticlesProcessed)
	ArticlesCached    uint64 `json:"articles_cached"`    // cached count
	ArticlesDead      uint64 `json:"articles_dead"`      // dead count
	TotalSize         int64  `json:"total_size"`         // Total size in bytes
	SessionID         uint64 `json:"session_id"`         // Link to SESSION
}

// JobConfig holds configuration options for a job
type JobConfig struct {
	CheckOnly  bool
	CheckFirst bool
	YencCRC    bool
	YencTest   int
	YencCpu    int
	Cachedir   string
}

// SessionStats holds real-time statistics extracted from a SESSION
type SessionStats struct {
	TotalArticles   uint64
	ArticlesDone    uint64
	ArticlesCached  uint64
	ArticlesDead    uint64
	ArticlesChecked uint64
}

// NewJobQueue creates a new job queue
func NewJobQueue() *JobQueue {
	globalmux.Lock()
	defer globalmux.Unlock()

	// Initialize with default config from current global config
	defaultConfig := JobConfig{
		CheckOnly:  false,
		CheckFirst: false,
		YencCRC:    false,
		YencTest:   4,
		YencCpu:    0,
		Cachedir:   "",
	}

	// If cfg is already initialized, use its values as defaults
	if cfg != nil && cfg.opt != nil {
		defaultConfig.CheckOnly = cfg.opt.CheckOnly
		defaultConfig.CheckFirst = cfg.opt.CheckFirst
		defaultConfig.YencCRC = cfg.opt.YencCRC
		defaultConfig.YencTest = cfg.opt.YencTest
		defaultConfig.YencCpu = cfg.opt.YencCpu
		defaultConfig.Cachedir = cfg.opt.Cachedir
	}

	return &JobQueue{
		jobs:          make([]*QueuedJob, 0),
		running:       make(map[string]*QueuedJob),
		completed:     make([]*QueuedJob, 0),
		nextID:        1,
		defaultConfig: defaultConfig,
	}
}

// AddJob adds a new job to the queue
func (jq *JobQueue) AddJob(nzbPath, nzbName string, config JobConfig) string {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	jobID := fmt.Sprintf("job-%d", jq.nextID)
	jq.nextID++

	job := &QueuedJob{
		ID:      jobID,
		NZBPath: nzbPath,
		NZBName: nzbName,
		Config:  config,
		Status:  "queued",
		AddedAt: time.Now(),
	}

	// Try to parse NZB to get size and article count
	nzbfile, err := loadNzbFile(nzbPath)
	if err == nil && nzbfile != nil {
		var totalArticles uint64

		for _, file := range nzbfile.Files {
			totalArticles += uint64(file.TotalSegments)
		}

		job.TotalSize = nzbfile.Bytes
		job.TotalArticles = totalArticles
	}

	jq.jobs = append(jq.jobs, job)
	return jobID
}

// GetJob retrieves a job by ID
func (jq *JobQueue) GetJob(jobID string) *QueuedJob {
	jq.mu.RLock()
	defer jq.mu.RUnlock()

	// Check running jobs first
	if job, ok := jq.running[jobID]; ok {
		return job
	}

	// Check queued jobs
	for _, job := range jq.jobs {
		if job.ID == jobID {
			return job
		}
	}

	// Check completed jobs
	for _, job := range jq.completed {
		if job.ID == jobID {
			return job
		}
	}

	return nil
}

// ListJobs returns all jobs (queued, running, and completed)
func (jq *JobQueue) ListJobs() []*QueuedJob {
	jq.mu.RLock()
	defer jq.mu.RUnlock()

	allJobs := make([]*QueuedJob, 0, len(jq.jobs)+len(jq.running)+len(jq.completed))

	// Add running jobs with real-time stats
	for _, job := range jq.running {
		// Extract session stats for running jobs
		if webProcessor != nil {
			webProcessor.mux.RLock()
			for _, session := range webProcessor.sessMap {
				if session != nil && session.nzbPath == job.NZBPath {
					sessionStats := extractSessionStats(session)
					if sessionStats != nil {
						// Update job with real-time statistics
						job.ArticlesDone = sessionStats.ArticlesDone
						job.ArticlesProcessed = sessionStats.ArticlesDone
						job.ArticlesCached = sessionStats.ArticlesCached
						job.ArticlesDead = sessionStats.ArticlesDead
						job.TotalArticles = sessionStats.TotalArticles
						if sessionStats.TotalArticles > 0 {
							job.Progress = float64(sessionStats.ArticlesDone) / float64(sessionStats.TotalArticles)
						}
					}
					break
				}
			}
			webProcessor.mux.RUnlock()
		}
		allJobs = append(allJobs, job)
	}

	// Add queued jobs
	allJobs = append(allJobs, jq.jobs...)

	// Add completed jobs
	allJobs = append(allJobs, jq.completed...)

	return allJobs
}

// GetDefaultConfig returns a copy of the default job configuration
func (jq *JobQueue) GetDefaultConfig() JobConfig {
	jq.mu.RLock()
	defer jq.mu.RUnlock()
	return jq.defaultConfig
}

// SetDefaultConfig updates the default job configuration
func (jq *JobQueue) SetDefaultConfig(config JobConfig) {
	jq.mu.Lock()
	defer jq.mu.Unlock()
	jq.defaultConfig = config
}

// StartAllQueued starts all queued jobs (they will process serially)
func (jq *JobQueue) StartAllQueued() (int, error) {
	jq.mu.Lock()

	// Get all queued job IDs
	queuedIDs := make([]string, 0, len(jq.jobs))
	for _, job := range jq.jobs {
		if job.Status == "queued" {
			queuedIDs = append(queuedIDs, job.ID)
		}
	}
	jq.mu.Unlock()

	// Start each job
	started := 0
	for _, jobID := range queuedIDs {
		if err := jq.StartJob(jobID); err != nil {
			dlog(always, "Warning: Failed to start job %s: %v", jobID, err)
		} else {
			started++
		}
	}

	return started, nil
}

// StartJob starts a queued job
func (jq *JobQueue) StartJob(jobID string) error {
	jq.mu.Lock()

	// Find the job in queue
	var jobIndex = -1
	var job *QueuedJob
	for i, j := range jq.jobs {
		if j.ID == jobID {
			jobIndex = i
			job = j
			break
		}
	}

	if jobIndex == -1 {
		jq.mu.Unlock()
		return fmt.Errorf("job %s not found in queue", jobID)
	}

	// Remove from queue
	jq.jobs = append(jq.jobs[:jobIndex], jq.jobs[jobIndex+1:]...)

	// Mark as running
	job.Status = "running"
	job.StartedAt = time.Now()
	jq.running[jobID] = job
	jq.mu.Unlock()

	// Start processing in background
	go func() {
		err := jq.processJob(job)

		jq.mu.Lock()
		if err != nil {
			job.Status = "failed"
			job.Error = err.Error()
			dlog(always, "ERROR: Job %s failed: %v", jobID, err)
		} else {
			job.Status = "completed"
			dlog(always, "Job %s completed successfully", jobID)
		}
		job.FinishedAt = time.Now()
		delete(jq.running, jobID)

		// Move to completed list
		jq.completed = append(jq.completed, job)

		// Keep only last 50 completed jobs to prevent memory leak
		if len(jq.completed) > 50 {
			jq.completed = jq.completed[len(jq.completed)-50:]
		}

		jq.mu.Unlock()
	}()

	return nil
}

// processJob processes a single job
func (jq *JobQueue) processJob(job *QueuedJob) error {
	if webProcessor == nil {
		return fmt.Errorf("web processor not initialized")
	}

	// CRITICAL: Lock to prevent concurrent job execution
	// This is necessary because cfg.opt is a GLOBAL variable shared across all jobs.
	// Without this lock, concurrent jobs would overwrite each other's configuration.
	// TODO: Refactor to pass per-job config to LaunchSession instead of using global cfg
	jq.processingMu.Lock()
	defer jq.processingMu.Unlock()

	// Apply job-specific configuration to global cfg
	/* disabled: data race!
	oldConfig := cfg.opt
	defer func() {
		cfg.opt = oldConfig // Restore original config
	}()

	// Override with job config
	cfg.opt.CheckOnly = job.Config.CheckOnly
	cfg.opt.CheckFirst = job.Config.CheckFirst
	cfg.opt.YencCRC = job.Config.YencCRC
	cfg.opt.YencTest = job.Config.YencTest
	cfg.opt.YencCpu = job.Config.YencCpu
	if job.Config.Cachedir != "" {
		cfg.opt.Cachedir = job.Config.Cachedir
	}
	*/
	wg := &sync.WaitGroup{}
	wg.Add(1)

	dlog(cfg.opt.Verbose, "Starting job %s with NZB: %s (serialized execution due to global cfg)", job.ID, job.NZBPath)

	// Launch session with NZB file (pass nil for session, file path for NZB)
	err := webProcessor.LaunchSession(nil, job.NZBPath, wg)
	if err != nil {
		return fmt.Errorf("failed to launch session: %w", err)
	}

	wg.Wait()
	return nil
}

// DeleteJob removes a job from the queue (only if not running)
func (jq *JobQueue) DeleteJob(jobID string) error {
	jq.mu.Lock()
	defer jq.mu.Unlock()

	// Can't delete running jobs
	if _, ok := jq.running[jobID]; ok {
		return fmt.Errorf("cannot delete running job %s", jobID)
	}

	// Find and remove from queued jobs
	for i, job := range jq.jobs {
		if job.ID == jobID {
			jq.jobs = append(jq.jobs[:i], jq.jobs[i+1:]...)
			return nil
		}
	}

	// Find and remove from completed jobs
	for i, job := range jq.completed {
		if job.ID == jobID {
			jq.completed = append(jq.completed[:i], jq.completed[i+1:]...)
			return nil
		}
	}

	return fmt.Errorf("job %s not found", jobID)
}

// WebStats holds statistics for the web interface
type WebStats struct {
	Uptime        string `json:"uptime"`
	TotalSegments uint64 `json:"total_segments"`
	CheckedSeg    uint64 `json:"checked_segments"`
	DownloadedSeg uint64 `json:"downloaded_segments"`
	UploadedSeg   uint64 `json:"uploaded_segments"`
	CachedSeg     uint64 `json:"cached_segments"`
	DeadSeg       uint64 `json:"dead_segments"`
	MemUsed       int    `json:"mem_used"`
	MemMax        int    `json:"mem_max"`

	// Bandwidth tracking
	TotalRXbytes uint64 `json:"total_download_bytes"`
	TotalTXbytes uint64 `json:"total_upload_bytes"`

	// Queue stats
	DLQueue      uint64 `json:"dl_queue"`
	UPQueue      uint64 `json:"up_queue"`
	TotalDLQueue uint64 `json:"total_dl_queue"`
	TotalUPQueue uint64 `json:"total_up_queue"`

	// Routine counts
	CheckRoutines uint64 `json:"check_routines"`
	DownsRoutines uint64 `json:"downs_routines"`
	ReupsRoutines uint64 `json:"reups_routines"`

	// Connection stats
	OpenConns     uint64 `json:"open_connections"`
	TotalNewConns uint64 `json:"total_new_connections"`
	TotalDisConns uint64 `json:"total_disconnections"`

	Providers  []ProviderStats `json:"providers"`
	ActiveJobs []JobInfo       `json:"jobs"`
	RecentLogs []string        `json:"recent_logs"`

	// Computed fields for frontend
	TotalJobs       int `json:"total_jobs"`
	ActiveProviders int `json:"active_providers"`
}

// ProviderStats holds per-provider statistics
type ProviderStats struct {
	Name          string  `json:"name"`
	Group         string  `json:"group"`
	DownloadSpeed float64 `json:"download_speed"` // bytes/sec
	UploadSpeed   float64 `json:"upload_speed"`   // bytes/sec
	TotalDownload uint64  `json:"download_bytes"` // total bytes
	TotalUpload   uint64  `json:"upload_bytes"`   // total bytes
	OpenConns     int     `json:"open_connections"`
	IdleConns     int     `json:"idle_connections"`
	Checks        uint64  `json:"checks"`
	Downloads     uint64  `json:"downloads"`
	Uploads       uint64  `json:"uploads"`

	// Article stats
	Checked    uint64 `json:"checked"`
	Available  uint64 `json:"available"`
	Missing    uint64 `json:"missing"`
	Downloaded uint64 `json:"downloaded"`
	Uploaded   uint64 `json:"uploaded"`

	// Total bandwidth
	TotalRXbyte uint64 `json:"total_rx_bytes"`
	TotalTXbyte uint64 `json:"total_tx_bytes"`
}

// JobInfo holds information about active processing jobs
type JobInfo struct {
	ID            string    `json:"id"`
	NZBName       string    `json:"name"`
	NZBFile       string    `json:"nzb_file"`
	StartTime     time.Time `json:"start_time"`
	StartedAt     time.Time `json:"started_at"`
	Status        string    `json:"status"`
	TotalSegments uint64    `json:"total_articles"`
	Progress      float64   `json:"progress"`

	// Additional stats for display
	ArticlesProcessed uint64 `json:"articles_processed"`
	UploadBytes       uint64 `json:"upload_bytes"`
	DownloadBytes     uint64 `json:"download_bytes"`
}

// StartWebServer initializes and starts the Gin web server
func StartWebServer(addr string, processor *PROCESSOR, wg *sync.WaitGroup) error {
	webServerEnabled = true
	webServerAddr = addr
	webProcessor = processor
	webWg = wg

	// Initialize job queue
	jobQueue = NewJobQueue()

	// Set Gin to release mode for production
	if !cfg.opt.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.Default()

	// Parse embedded templates
	tmpl, err := template.ParseFS(templatesFS, "web/templates/*.html")
	if err != nil {
		return fmt.Errorf("failed to parse templates: %w", err)
	}
	router.SetHTMLTemplate(tmpl)

	// Serve static CSS file from embedded filesystem
	router.GET("/static/style.css", func(c *gin.Context) {
		data, err := templatesFS.ReadFile("web/templates/style.css")
		if err != nil {
			c.String(http.StatusNotFound, "CSS file not found")
			return
		}
		c.Data(http.StatusOK, "text/css", data)
	})

	// Routes
	router.GET("/", handleIndex)
	router.GET("/stats", handleStats)
	router.POST("/upload", handleUpload)
	router.GET("/api/stats", handleAPIStats)

	// Job queue routes
	router.GET("/jobs", handleJobsList)
	router.GET("/api/jobs", handleAPIJobsList)
	router.POST("/api/jobs/:id/start", handleJobStart)
	router.POST("/api/jobs/start-all", handleJobStartAll)
	router.DELETE("/api/jobs/:id", handleJobDelete)
	router.PATCH("/api/jobs/:id/config", handleJobUpdateConfig)
	router.GET("/api/jobs/default-config", handleGetDefaultConfig)
	router.PUT("/api/jobs/default-config", handleSetDefaultConfig)

	// Determine protocol for logging
	protocol := "http"
	if webServerSSL {
		protocol = "https"
	}
	dlog(always, "Starting web server on %s://%s", protocol, addr)

	// Run server in a goroutine
	go func() {
		var err error
		if webServerSSL {
			// Start HTTPS server with TLS
			err = router.RunTLS(addr, webServerCert, webServerKey)
		} else {
			// Start HTTP server
			err = router.Run(addr)
		}
		if err != nil {
			dlog(always, "ERROR: Web server failed to start: %v", err)
		}
	}()

	return nil
}

// handleIndex serves the main page
func handleIndex(c *gin.Context) {
	c.HTML(http.StatusOK, "index.html", gin.H{
		"title":   "NZBreX - NZB Refresh",
		"version": appVersion,
	})
}

// handleStats serves the statistics page
func handleStats(c *gin.Context) {
	stats := collectStats()
	c.HTML(http.StatusOK, "stats.html", gin.H{
		"title": "NZBreX Statistics",
		"stats": stats,
	})
}

// handleUpload processes NZB file uploads and adds them to the job queue
func handleUpload(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// Validate file extension
	ext := filepath.Ext(file.Filename)
	if ext != ".nzb" && ext != ".gz" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only .nzb and .nzb.gz files are allowed"})
		return
	}

	// Create temporary directory for uploads if it doesn't exist
	uploadDir := filepath.Join(os.TempDir(), "nzbrex-uploads")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload directory"})
		return
	}

	// Save uploaded file
	dst := filepath.Join(uploadDir, file.Filename)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
		return
	}

	dlog(always, "Web: Uploaded NZB file: %s (size: %d bytes)", file.Filename, file.Size)

	// Start with default config from queue, then override with form data if provided
	config := jobQueue.GetDefaultConfig()

	// Override with form data if provided
	if checkonly := c.PostForm("checkonly"); checkonly != "" {
		config.CheckOnly = checkonly == "true"
	}
	if checkfirst := c.PostForm("checkfirst"); checkfirst != "" {
		config.CheckFirst = checkfirst == "true"
	}
	if crc32 := c.PostForm("crc32"); crc32 != "" {
		config.YencCRC = crc32 == "true"
	}
	if cachedir := c.PostForm("cachedir"); cachedir != "" {
		config.Cachedir = cachedir
	}

	// Parse optional int fields
	if yencTest := c.PostForm("yenctest"); yencTest != "" {
		if val, err := fmt.Sscanf(yencTest, "%d", &config.YencTest); err == nil && val == 1 {
			if config.YencTest < 1 || config.YencTest > 4 {
				config.YencTest = 4
			}
		}
	}
	if yencCpu := c.PostForm("yenccpu"); yencCpu != "" {
		fmt.Sscanf(yencCpu, "%d", &config.YencCpu)
	}

	// Add job to queue
	jobID := jobQueue.AddJob(dst, file.Filename, config)

	c.JSON(http.StatusOK, gin.H{
		"message":  "File uploaded and added to queue",
		"filename": file.Filename,
		"size":     file.Size,
		"job_id":   jobID,
		"status":   "queued",
	})
}

// handleJobsList displays the jobs queue page
func handleJobsList(c *gin.Context) {
	jobs := jobQueue.ListJobs()
	c.HTML(http.StatusOK, "jobs.html", gin.H{
		"title": "Job Queue",
		"jobs":  jobs,
	})
}

// handleAPIJobsList returns all jobs as JSON
func handleAPIJobsList(c *gin.Context) {
	jobs := jobQueue.ListJobs()
	c.JSON(http.StatusOK, gin.H{
		"jobs": jobs,
	})
}

// handleJobStart starts a queued job
func handleJobStart(c *gin.Context) {
	jobID := c.Param("id")

	if err := jobQueue.StartJob(jobID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Job started successfully",
		"job_id":  jobID,
	})
}

// handleJobDelete deletes a queued job
func handleJobDelete(c *gin.Context) {
	jobID := c.Param("id")

	if err := jobQueue.DeleteJob(jobID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Job deleted successfully",
		"job_id":  jobID,
	})
}

// handleJobUpdateConfig updates a job's configuration
func handleJobUpdateConfig(c *gin.Context) {
	jobID := c.Param("id")

	job := jobQueue.GetJob(jobID)
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Job not found"})
		return
	}

	if job.Status != "queued" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot update config of non-queued job"})
		return
	}

	// Update configuration from JSON body
	var newConfig JobConfig
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid configuration"})
		return
	}

	jobQueue.mu.Lock()
	job.Config = newConfig
	jobQueue.mu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"message": "Job configuration updated",
		"job_id":  jobID,
		"config":  newConfig,
	})
}

// handleJobStartAll starts all queued jobs
func handleJobStartAll(c *gin.Context) {
	started, err := jobQueue.StartAllQueued()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("Started %d job(s)", started),
		"count":   started,
	})
}

// handleGetDefaultConfig returns the default job configuration
func handleGetDefaultConfig(c *gin.Context) {
	config := jobQueue.GetDefaultConfig()
	c.JSON(http.StatusOK, gin.H{
		"config": config,
	})
}

// handleSetDefaultConfig updates the default job configuration
func handleSetDefaultConfig(c *gin.Context) {
	var newConfig JobConfig
	if err := c.ShouldBindJSON(&newConfig); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid configuration"})
		return
	}

	jobQueue.SetDefaultConfig(newConfig)

	c.JSON(http.StatusOK, gin.H{
		"message": "Default configuration updated",
		"config":  newConfig,
	})
}

// handleAPIStats returns statistics as JSON for AJAX requests
func handleAPIStats(c *gin.Context) {
	stats := collectStats()
	c.JSON(http.StatusOK, stats)
}

// extractSessionStats extracts real-time statistics from a SESSION
// This function counts the various states of segments in the session
func extractSessionStats(s *SESSION) *SessionStats {
	if s == nil || s.segmentList == nil {
		return nil
	}
	s.mux.RLock()
	defer s.mux.RUnlock()

	stats := &SessionStats{
		TotalArticles: uint64(len(s.segmentList)),
	}
	providersCnt := len(s.providerList)

	// Count articles by state
	for _, item := range s.segmentList {
		item.mux.RLock()

		// Count cached articles
		if item.flagCache {
			stats.ArticlesCached++
		}

		// Count checked articles
		if item.checkedOn > 0 {
			stats.ArticlesChecked++
		}

		// Count done articles (available on all providers or uploaded)
		if len(item.missingOn) == 0 && len(item.availableOn) == providersCnt {
			stats.ArticlesDone++
		}

		// Count dead articles (not available anywhere)
		if len(item.availableOn) == 0 && len(item.missingOn) == providersCnt {
			stats.ArticlesDead++
		}

		item.mux.RUnlock()
	}

	return stats
}

// collectStats gathers current statistics for display
func collectStats() WebStats {
	stats := WebStats{
		Uptime:     formatDuration(time.Since(booted)),
		RecentLogs: []string{},
		Providers:  []ProviderStats{},
		ActiveJobs: []JobInfo{},
	}

	// Get memory usage
	if memlim != nil {
		stats.MemUsed, stats.MemMax = memlim.Usage()
	}

	// Get global counters if available
	if GCounter != nil {
		// Download/Upload bandwidth tracking
		stats.TotalRXbytes = GCounter.GetValue("TOTAL_RXbytes")
		stats.TotalTXbytes = GCounter.GetValue("TOTAL_TXbytes")

		// Queue counters
		stats.DLQueue = GCounter.GetValue("dlQueueCnt")
		stats.UPQueue = GCounter.GetValue("upQueueCnt")
		stats.TotalDLQueue = GCounter.GetValue("TOTAL_dlQueueCnt")
		stats.TotalUPQueue = GCounter.GetValue("TOTAL_upQueueCnt")

		// Routine counters
		stats.CheckRoutines = GCounter.GetValue("GoCheckRoutines")
		stats.DownsRoutines = GCounter.GetValue("GoDownsRoutines")
		stats.ReupsRoutines = GCounter.GetValue("GoReupsRoutines")

		// Connection counters
		stats.TotalNewConns = GCounter.GetValue("TOTAL_NewConns")
		stats.TotalDisConns = GCounter.GetValue("TOTAL_DisConns")
		stats.OpenConns = stats.TotalNewConns - stats.TotalDisConns
	}

	// Collect provider statistics from running/completed jobs
	globalmux.RLock()
	defer globalmux.RUnlock()

	if webProcessor != nil {
		webProcessor.mux.RLock()
		// Iterate through all active sessions
		for _, session := range webProcessor.sessMap {
			if session != nil && session.active && session.providerList != nil {
				for _, prov := range session.providerList {
					openConns, idleConns := prov.ConnPool.GetStats()

					// Get bandwidth from provider counter
					provRX := prov.ConnPool.Counter.GetValue("TOTAL_RXbytes")
					provTX := prov.ConnPool.Counter.GetValue("TOTAL_TXbytes")

					// Get speed values stored by Speedmeter function
					prov.mux.RLock()
					downloadSpeed := prov.speed.downloadSpeed
					uploadSpeed := prov.speed.uploadSpeed
					prov.mux.RUnlock()

					provStats := ProviderStats{
						Name:          prov.Name,
						Group:         prov.Group,
						OpenConns:     openConns,
						IdleConns:     idleConns,
						Checks:        prov.articles.checked,
						Downloads:     prov.articles.downloaded,
						Uploads:       prov.articles.refreshed,
						Checked:       prov.articles.checked,
						Available:     prov.articles.available,
						Missing:       prov.articles.missing,
						Downloaded:    prov.articles.downloaded,
						Uploaded:      prov.articles.refreshed,
						TotalRXbyte:   provRX,
						TotalTXbyte:   provTX,
						TotalDownload: provRX,        // For JSON compatibility
						TotalUpload:   provTX,        // For JSON compatibility
						DownloadSpeed: downloadSpeed, // bytes/sec (from Speedmeter)
						UploadSpeed:   uploadSpeed,   // bytes/sec (from Speedmeter)
					}
					stats.Providers = append(stats.Providers, provStats)
				}
			}
		}
		webProcessor.mux.RUnlock()
	}

	// Collect active job information
	if jobQueue != nil {
		jobQueue.mu.RLock()
		for _, job := range jobQueue.running {
			// Try to find the session for this job and get real-time stats
			var sessionStats *SessionStats
			if webProcessor != nil {
				webProcessor.mux.RLock()
				for _, session := range webProcessor.sessMap {
					if session != nil && session.nzbPath == job.NZBPath {
						sessionStats = extractSessionStats(session)
						break
					}
				}
				webProcessor.mux.RUnlock()
			}

			jobInfo := JobInfo{
				ID:        job.ID,
				NZBName:   job.NZBName,
				Status:    job.Status,
				StartedAt: job.StartedAt,
				StartTime: job.StartedAt, // For JSON compatibility
				Progress:  job.Progress,
			}

			// Update job with session stats if available
			if sessionStats != nil {
				jobInfo.TotalSegments = sessionStats.TotalArticles
				jobInfo.ArticlesProcessed = sessionStats.ArticlesDone
				// Update the job object itself with real-time progress
				job.ArticlesDone = sessionStats.ArticlesDone
				job.ArticlesProcessed = sessionStats.ArticlesDone
				job.ArticlesCached = sessionStats.ArticlesCached
				job.ArticlesDead = sessionStats.ArticlesDead
				job.TotalArticles = sessionStats.TotalArticles
				if sessionStats.TotalArticles > 0 {
					job.Progress = float64(sessionStats.ArticlesDone) / float64(sessionStats.TotalArticles)
				}
			}

			stats.ActiveJobs = append(stats.ActiveJobs, jobInfo)
		}
		jobQueue.mu.RUnlock()
	}

	// Calculate computed fields
	stats.TotalJobs = len(stats.ActiveJobs)
	stats.ActiveProviders = 0
	for _, prov := range stats.Providers {
		if prov.Checks > 0 || prov.Downloads > 0 || prov.Uploads > 0 || prov.OpenConns > 0 {
			stats.ActiveProviders++
		}
	}

	return stats
}

// formatDuration formats a duration into a human-readable string
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// WriteWebLog adds a log entry to the web interface (for display in UI)
func WriteWebLog(msg string) {
	// This could be expanded to maintain a ring buffer of recent logs
	// for display in the web interface
	if webServerEnabled {
		// Store log in a circular buffer or similar
		log.Printf("[WEB] %s", msg)
	}
}
