package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed web/templates/*
var templatesFS embed.FS

// bootstrap is served from here, not a CDN: no third-party script, works offline
//
//go:embed web/static/*
var staticFS embed.FS

var (
	webServerEnabled bool
	webServerAddr    string
	webServerSSL     bool
	webServerCert    string
	webServerKey     string
	webProcessor     *PROCESSOR              // set by StartWebServer before the server serves
	jobQueue         *JobQueue               // set by StartWebServer before the server serves
	webSrv           *http.Server            // the running server, for Shutdown
	webBaseSession   atomic.Pointer[SESSION] // set in main: the providers-only session behind -web
	webUploadDir     string                  // private dir (0700) for uploaded NZBs, made at start
	webUploadSeq     atomic.Uint64
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
	// login (D1): its own passwd file; no file is allowed only on loopback
	var auth *webAuth
	if webPasswdFile == "" {
		if !isLoopbackAddr(addr) {
			return fmt.Errorf("refusing to serve on '%s' without a login: set -webpasswd (users via -webadduser), or bind to 127.0.0.1", addr)
		}
		dlog(always, "WARNING: web server has no login (no -webpasswd): anyone who can reach %s can run jobs", addr)
	} else {
		var err error
		if auth, err = newWebAuth(webPasswdFile); err != nil {
			return err
		}
	}

	webProcessor = processor
	jobQueue = NewJobQueue()

	handler, err := webHandler(addr, auth)
	if err != nil {
		return err
	}

	var tlsConf *tls.Config
	if webServerSSL {
		cert, err := tls.LoadX509KeyPair(webServerCert, webServerKey)
		if err != nil {
			return fmt.Errorf("web server TLS cert='%s' key='%s': %w", webServerCert, webServerKey, err)
		}
		tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	// a fresh private dir: the old fixed /tmp/nzbrex-uploads (0755) let another
	// local user plant symlinks that the old save (gin's SaveUploadedFile) followed
	if webUploadDir, err = os.MkdirTemp("", "nzbrex-uploads-"); err != nil {
		return fmt.Errorf("web server upload dir: %w", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		os.Remove(webUploadDir)
		return fmt.Errorf("web server listen on '%s': %w", addr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsConf,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		// no WriteTimeout/ReadTimeout: slow uploads are fine, their size is capped instead
	}
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

// H is a JSON object for responses.
type H = map[string]any

// webHandler returns the web server's routes behind the Host/CSRF guard and,
// with auth, the login. Standard library only (net/http routing, Go 1.22+).
func webHandler(addr string, auth *webAuth) (http.Handler, error) {
	tmpl, err := template.ParseFS(templatesFS, "web/templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}
	static, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	// pages: they load their data from the API
	mux.HandleFunc("GET /{$}", page(tmpl, "index.html"))
	mux.HandleFunc("GET /stats", page(tmpl, "stats.html"))
	mux.HandleFunc("GET /jobs", page(tmpl, "jobs.html"))
	mux.Handle("GET /static/", http.StripPrefix("/static/", filesOnly(http.FileServerFS(static))))

	mux.HandleFunc("POST /upload", handleUpload)
	mux.HandleFunc("GET /api/stats", handleAPIStats)

	// job queue
	mux.HandleFunc("GET /api/jobs", handleAPIJobsList)
	mux.HandleFunc("POST /api/jobs/{id}/start", handleJobStart)
	mux.HandleFunc("POST /api/jobs/start-all", handleJobStartAll)
	mux.HandleFunc("POST /api/jobs/{id}/stop", handleJobStop)
	mux.HandleFunc("DELETE /api/jobs/{id}", handleJobDelete)
	// per-job settings are not implemented yet (D2)
	mux.HandleFunc("PATCH /api/jobs/{id}/config", handleJobConfigNotImplemented)
	mux.HandleFunc("GET /api/jobs/default-config", handleJobConfigNotImplemented)
	mux.HandleFunc("PUT /api/jobs/default-config", handleJobConfigNotImplemented)

	var h http.Handler = mux
	if auth != nil {
		h = auth.middleware(h)
	}
	h = webGuard(webAllowedHosts(addr, auth != nil), h)
	return recoverPanics(accessLog(h)), nil
}

// writeJSON answers with v as JSON (no trailing newline, like before).
func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		dlog(always, "ERROR Web: encoding a response: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(b)
}

// page serves one template. It renders into a buffer first, so a template
// error gives a 500 instead of half a page.
func page(tmpl *template.Template, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, name, nil); err != nil {
			dlog(always, "ERROR Web: template %s: %v", name, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(buf.Bytes())
	}
}

// filesOnly answers 404 for directories: no listings of the static files.
func filesOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a panic in a handler into a 500 and a log line with the
// stack (gin's Recovery did this before).
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				dlog(always, "ERROR Web: panic in %s %s: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusWriter records the status code for the access log.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) { s.code = code; s.ResponseWriter.WriteHeader(code) }

// accessLog logs every request with -debug. (gin logged them always; the pages
// poll every few seconds, so that was a line every few seconds per open page.)
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.opt.Debug {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r)
		dlog(always, "Web: %s %s %s -> %d (%v)", peerIP(r), r.Method, r.URL.Path, sw.code, time.Since(start).Round(time.Microsecond))
	})
}

// webShutdownOnSignal stops the web server cleanly on SIGINT/SIGTERM: no new
// requests, waiting jobs canceled, the running job stopped (its session tears
// down as usual), the upload dir removed. Then the process exits.
func webShutdownOnSignal() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		dlog(always, "Web: got %v, shutting down (again to force)", sig)
		go func() { <-sigs; dlog(always, "Web: forced exit"); os.Exit(1) }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := webSrv.Shutdown(ctx); err != nil {
			dlog(always, "Web: shutdown: %v", err)
		}
		cancel()
		jobQueue.stopAll()
		if !jobQueue.waitIdle(60 * time.Second) {
			dlog(always, "WARN Web: the running job did not stop within 60s, exiting anyway")
		}
		if webUploadDir != "" {
			os.RemoveAll(webUploadDir)
		}
		dlog(always, "Web: shut down")
		os.Exit(0)
	}()
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

// uploadName turns a client file name into a safe, unique name in webUploadDir.
// The job's display name stays the client's name.
func uploadName(clientName string) string {
	base := filepath.Base(strings.ReplaceAll(clientName, "\\", "/"))
	clean := []rune{}
	for _, r := range base {
		if r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			clean = append(clean, r)
		} else {
			clean = append(clean, '_')
		}
	}
	if len(clean) > 100 {
		clean = clean[len(clean)-100:] // keep the extension
	}
	return fmt.Sprintf("%d-%s", webUploadSeq.Add(1), string(clean))
}

// saveUpload writes the upload to a new file in webUploadDir. O_EXCL: an
// existing file or symlink at that name makes it fail instead of being followed.
func saveUpload(file *multipart.FileHeader) (string, error) {
	src, err := file.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst := filepath.Join(webUploadDir, uploadName(file.Filename))
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dst)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	return dst, nil
}

// handleUpload processes NZB file uploads and adds them to the job queue
func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, webMaxUploadBytes)
	var file *multipart.FileHeader
	err := r.ParseMultipartForm(8 << 20) // bigger uploads spool to a temp file
	if err == nil {
		if r.MultipartForm.File["file"] == nil {
			err = http.ErrMissingFile
		} else {
			file = r.MultipartForm.File["file"][0]
		}
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, H{"error": fmt.Sprintf("upload larger than %d MiB", webMaxUploadBytes>>20)})
			return
		}
		writeJSON(w, http.StatusBadRequest, H{"error": "No file uploaded"})
		return
	}

	// Validate file extension
	ext := filepath.Ext(file.Filename)
	if ext != ".nzb" && ext != ".gz" {
		writeJSON(w, http.StatusBadRequest, H{"error": "Invalid file type. Only .nzb and .nzb.gz files are allowed"})
		return
	}

	// saved under a unique name: two uploads of foo.nzb used to overwrite each other
	dst, err := saveUpload(file)
	if err != nil {
		dlog(always, "ERROR Web: saving upload '%s': %v", file.Filename, err)
		writeJSON(w, http.StatusInternalServerError, H{"error": "Failed to save file"})
		return
	}

	dlog(always, "Web: Uploaded NZB file: %s (size: %d bytes)", file.Filename, file.Size)

	jobID, err := jobQueue.AddJob(dst, file.Filename)
	if err != nil {
		os.Remove(dst)
		writeJSON(w, jobErrStatus(err), H{"error": err.Error()})
		return
	}

	resp := H{
		"message":  "File uploaded and added to queue",
		"filename": file.Filename,
		"size":     file.Size,
		"job_id":   jobID,
		"status":   jobQueued,
	}
	for _, field := range []string{"checkonly", "checkfirst", "crc32", "cachedir", "yenctest", "yenccpu"} {
		if r.PostFormValue(field) != "" {
			resp["note"] = "per-job settings are not supported yet and were ignored: the job runs with the startup settings"
			break
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAPIJobsList returns all jobs as JSON
func handleAPIJobsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, H{
		"jobs": jobQueue.ListJobs(),
	})
}

// handleJobStart starts a queued job
func handleJobStart(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := jobQueue.StartJob(jobID); err != nil {
		writeJSON(w, jobErrStatus(err), H{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, H{
		"message": "Job started successfully",
		"job_id":  jobID,
	})
}

// handleJobStop cancels a waiting job or stops a running one
func handleJobStop(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := jobQueue.StopJob(jobID); err != nil {
		writeJSON(w, jobErrStatus(err), H{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, H{
		"message": "Job stopping",
		"job_id":  jobID,
	})
}

// handleJobDelete deletes a queued or finished job
func handleJobDelete(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := jobQueue.DeleteJob(jobID); err != nil {
		writeJSON(w, jobErrStatus(err), H{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, H{
		"message": "Job deleted successfully",
		"job_id":  jobID,
	})
}

// handleJobConfigNotImplemented answers the per-job and default settings
// endpoints. Jobs run with the startup settings until per-job settings exist (D2).
func handleJobConfigNotImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, H{
		"error":  "per-job settings are not supported yet: jobs run with the startup settings",
		"config": startupJobConfig(),
	})
}

// handleJobStartAll starts all queued jobs
func handleJobStartAll(w http.ResponseWriter, r *http.Request) {
	started := jobQueue.StartAllQueued()
	writeJSON(w, http.StatusOK, H{
		"message": fmt.Sprintf("Started %d job(s)", started),
		"count":   started,
	})
}

// handleAPIStats returns statistics as JSON for AJAX requests
func handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := collectStats()
	writeJSON(w, http.StatusOK, stats)
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
		if len(providerLists) == 0 {
			// no job running: show the providers the web session loaded at startup
			// (it never boots workers, so it is never active; plan 6.3)
			if base := webBaseSession.Load(); base != nil {
				base.mux.RLock()
				providerList := base.providerList
				base.mux.RUnlock()
				providerLists = append(providerLists, providerList)
			}
		}
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
