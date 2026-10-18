package main

import (
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	webProcessor     *PROCESSOR   // set by StartWebServer before the server serves
	jobQueue         *JobQueue    // set by StartWebServer before the server serves
	webSrv           *http.Server // the running server, for Shutdown
)

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

// StartWebServer sets up the routes, binds addr and starts serving in the
// background. A bind or certificate error is returned (plan 4.1: it used to be
// logged from a goroutine while the process went on without a web server).
func StartWebServer(addr string, processor *PROCESSOR) error {
	webProcessor = processor
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
	// per-job settings are not implemented yet (D2)
	router.PATCH("/api/jobs/:id/config", handleJobConfigNotImplemented)
	router.GET("/api/jobs/default-config", handleJobConfigNotImplemented)
	router.PUT("/api/jobs/default-config", handleJobConfigNotImplemented)

	var tlsConf *tls.Config
	if webServerSSL {
		cert, err := tls.LoadX509KeyPair(webServerCert, webServerKey)
		if err != nil {
			return fmt.Errorf("web server TLS cert='%s' key='%s': %w", webServerCert, webServerKey, err)
		}
		tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web server listen on '%s': %w", addr, err)
	}
	srv := &http.Server{Handler: router, TLSConfig: tlsConf}
	webSrv = srv

	protocol := "http"
	if tlsConf != nil {
		protocol = "https"
	}
	dlog(always, "Web server listening on %s://%s", protocol, ln.Addr())

	go func() {
		var err error
		if tlsConf != nil {
			err = srv.ServeTLS(ln, "", "") // certificate is in TLSConfig
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			dlog(always, "ERROR: web server stopped: %v", err)
		}
	}()
	return nil
}

// handleIndex serves the main page
func handleIndex(c *gin.Context) {
	c.HTML(http.StatusOK, "index.html", nil)
}

// handleStats serves the statistics page. The page loads its data from /api/stats.
func handleStats(c *gin.Context) {
	c.HTML(http.StatusOK, "stats.html", nil)
}

// jobErrStatus maps a job queue error to an HTTP status.
func jobErrStatus(err error) int {
	switch {
	case errors.Is(err, errJobNotFound):
		return http.StatusNotFound
	case errors.Is(err, errJobState):
		return http.StatusConflict
	case errors.Is(err, errQueueFull):
		return http.StatusServiceUnavailable
	}
	return http.StatusBadRequest
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

	jobID, err := jobQueue.AddJob(dst, file.Filename)
	if err != nil {
		os.Remove(dst)
		c.JSON(jobErrStatus(err), gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"message":  "File uploaded and added to queue",
		"filename": file.Filename,
		"size":     file.Size,
		"job_id":   jobID,
		"status":   jobQueued,
	}
	for _, field := range []string{"checkonly", "checkfirst", "crc32", "cachedir", "yenctest", "yenccpu"} {
		if c.PostForm(field) != "" {
			resp["note"] = "per-job settings are not supported yet and were ignored: the job runs with the startup settings"
			break
		}
	}
	c.JSON(http.StatusOK, resp)
}

// handleJobsList displays the jobs queue page. The page loads its data from /api/jobs.
func handleJobsList(c *gin.Context) {
	c.HTML(http.StatusOK, "jobs.html", nil)
}

// handleAPIJobsList returns all jobs as JSON
func handleAPIJobsList(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"jobs": jobQueue.ListJobs(),
	})
}

// handleJobStart starts a queued job
func handleJobStart(c *gin.Context) {
	jobID := c.Param("id")
	if err := jobQueue.StartJob(jobID); err != nil {
		c.JSON(jobErrStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Job started successfully",
		"job_id":  jobID,
	})
}

// handleJobDelete deletes a queued or finished job
func handleJobDelete(c *gin.Context) {
	jobID := c.Param("id")
	if err := jobQueue.DeleteJob(jobID); err != nil {
		c.JSON(jobErrStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Job deleted successfully",
		"job_id":  jobID,
	})
}

// handleJobConfigNotImplemented answers the per-job and default settings
// endpoints. Jobs run with the startup settings until per-job settings exist (D2).
func handleJobConfigNotImplemented(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{
		"error":  "per-job settings are not supported yet: jobs run with the startup settings",
		"config": startupJobConfig(),
	})
}

// handleJobStartAll starts all queued jobs
func handleJobStartAll(c *gin.Context) {
	started := jobQueue.StartAllQueued()
	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("Started %d job(s)", started),
		"count":   started,
	})
}

// handleAPIStats returns statistics as JSON for AJAX requests
func handleAPIStats(c *gin.Context) {
	stats := collectStats()
	c.JSON(http.StatusOK, stats)
}

// collectStats gathers current statistics for display. It only copies
// counters: no segment list is walked and no lock is held for long (plan 4.3).
func collectStats() WebStats {
	stats := WebStats{
		Uptime:     formatDuration(time.Since(booted)),
		RecentLogs: []string{},
		Providers:  []ProviderStats{},
		ActiveJobs: []JobInfo{},
	}

	// memlim is created by the first LaunchSession, under globalmux
	globalmux.RLock()
	ml := memlim
	globalmux.RUnlock()
	if ml != nil {
		stats.MemUsed, stats.MemMax = ml.Usage()
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

	if webProcessor != nil {
		// copy the active sessions' provider lists, then read the providers without p.mux
		var providerLists [][]*Provider
		func() {
			// closure + defer: a panic in here must never leave webProcessor.mux read-locked,
			// or removeSession() blocks forever and the whole web server wedges
			webProcessor.mux.RLock()
			defer webProcessor.mux.RUnlock()
			for _, session := range webProcessor.sessMap {
				if session == nil {
					continue
				}
				session.mux.RLock()
				active := session.active             // written under session.mux by GoBootWorkers and closeSession
				providerList := session.providerList // published under session.mux by loadProviderList
				session.mux.RUnlock()
				if active {
					providerLists = append(providerLists, providerList)
				}
			}
		}()
		for _, providerList := range providerLists {
			for _, prov := range providerList {
				// KillConnPool sets prov.ConnPool = nil and the workers update the
				// counters under prov.mux: copy everything under one read lock
				prov.mux.RLock()
				cp := prov.ConnPool
				articles := prov.articles
				downloadSpeed := prov.speed.downloadSpeed // stored by Speedmeter
				uploadSpeed := prov.speed.uploadSpeed
				prov.mux.RUnlock()
				if cp == nil {
					continue // pool already closed: session is shutting down
				}
				openConns, idleConns := cp.GetStats()

				// Get bandwidth from provider counter
				provRX := cp.Counter.GetValue("TOTAL_RXbytes")
				provTX := cp.Counter.GetValue("TOTAL_TXbytes")

				stats.Providers = append(stats.Providers, ProviderStats{
					Name:          prov.Name,
					Group:         prov.Group,
					OpenConns:     openConns,
					IdleConns:     idleConns,
					Checks:        articles.checked,
					Downloads:     articles.downloaded,
					Uploads:       articles.refreshed,
					Checked:       articles.checked,
					Available:     articles.available,
					Missing:       articles.missing,
					Downloaded:    articles.downloaded,
					Uploaded:      articles.refreshed,
					TotalRXbyte:   provRX,
					TotalTXbyte:   provTX,
					TotalDownload: provRX,        // For JSON compatibility
					TotalUpload:   provTX,        // For JSON compatibility
					DownloadSpeed: downloadSpeed, // bytes/sec (from Speedmeter)
					UploadSpeed:   uploadSpeed,   // bytes/sec (from Speedmeter)
				})
			}
		}
	}

	// running jobs, with their sessions' live progress (copies: nothing is written)
	if jobQueue != nil {
		for _, job := range jobQueue.runningJobs() {
			stats.ActiveJobs = append(stats.ActiveJobs, JobInfo{
				ID:                job.ID,
				NZBName:           job.NZBName,
				Status:            job.Status,
				StartedAt:         job.StartedAt,
				StartTime:         job.StartedAt, // For JSON compatibility
				TotalSegments:     job.TotalArticles,
				Progress:          job.Progress,
				ArticlesProcessed: job.ArticlesProcessed,
			})
		}
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
