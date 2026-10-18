package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Directory mode (-nzbdir): watch a directory and process its NZBs one at a
// time, oldest first, like the web job queue. A file is taken once its size and
// modification time are the same in two scans in a row (not while it is still
// being copied in). Afterwards it moves to <dir>/done/, or to <dir>/failed/ if
// the run could not start (unreadable NZB, provider errors). A run stopped by a
// signal leaves the file where it is, so the next start processes it again.

// nzbDirMinRefresh is the shortest scan interval (-dirrefresh). A var for the tests.
var nzbDirMinRefresh = 15 * time.Second

const (
	nzbDirDone   = "done"
	nzbDirFailed = "failed"
)

// dirFile is what a scan saw of a file.
type dirFile struct {
	size  int64
	mtime time.Time
}

type dirWatcher struct {
	dir     string
	refresh time.Duration
	run     func(name string) error // runs one NZB (the file name in dir); tests replace it

	pending map[string]dirFile // seen by the last scan, waiting for an unchanged second look
	skip    map[string]bool    // processed but could not be moved: not run again

	mu       sync.Mutex
	current  *SESSION // the running session, stopped by Stop
	stop     chan struct{}
	stopOnce sync.Once
}

// newDirWatcher watches p's -nzbdir. refresh is clamped to nzbDirMinRefresh.
func (p *PROCESSOR) newDirWatcher(refresh time.Duration) *dirWatcher {
	w := &dirWatcher{
		dir:     p.nzbDir,
		refresh: max(refresh, nzbDirMinRefresh),
		pending: make(map[string]dirFile),
		skip:    make(map[string]bool),
		stop:    make(chan struct{}),
	}
	w.run = func(name string) error { return w.runSession(p, name) }
	return w
}

// runSession processes one NZB in a session of its own and removes the session
// afterwards, so a long-running watcher doesn't keep every segment list.
func (w *dirWatcher) runSession(p *PROCESSOR, name string) (err error) {
	sessId, s := p.newSession(name) // nzbPath = <dir>/<name>
	w.mu.Lock()
	w.current = s
	stopped := w.stopped()
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.current = nil
		w.mu.Unlock()
		p.removeSession(sessId, s)
	}()
	if stopped {
		return errDirStopped
	}
	defer func() {
		if r := recover(); r != nil { // a panic fails this NZB, not the watcher
			dlog(always, "ERROR -nzbdir: session for '%s' panicked: %v\n%s", name, r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.LaunchSession(s, "", nil)
}

var errDirStopped = errors.New("stopped")

func (w *dirWatcher) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// Stop ends the loop and the running session (its file stays in place).
func (w *dirWatcher) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.mu.Lock()
	s := w.current
	w.mu.Unlock()
	if s != nil {
		s.stop()
	}
}

// scan returns the NZBs that look complete: present in the last scan with the
// same size and modification time. Oldest first.
func (w *dirWatcher) scan() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]dirFile)
	type ready struct {
		name  string
		mtime time.Time
	}
	var out []ready
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || w.skip[name] || !(strings.HasSuffix(name, ".nzb") || strings.HasSuffix(name, ".nzb.gz")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone since ReadDir
		}
		now := dirFile{size: info.Size(), mtime: info.ModTime()}
		seen[name] = now
		if before, ok := w.pending[name]; ok && before == now {
			out = append(out, ready{name, now.mtime})
		}
	}
	w.pending = seen
	sort.Slice(out, func(i, j int) bool {
		if !out[i].mtime.Equal(out[j].mtime) {
			return out[i].mtime.Before(out[j].mtime)
		}
		return out[i].name < out[j].name
	})
	names := make([]string, len(out))
	for i, r := range out {
		names[i] = r.name
	}
	return names, nil
}

// processOne runs one NZB and files it under done/ or failed/.
func (w *dirWatcher) processOne(name string) {
	start := time.Now()
	dlog(always, "-nzbdir: processing '%s'", name)
	err := w.run(name)
	if errors.Is(err, errDirStopped) || w.stopped() {
		dlog(always, "-nzbdir: '%s' stopped, it stays in '%s'", name, w.dir)
		return
	}
	sub := nzbDirDone
	if err != nil {
		sub = nzbDirFailed
		dlog(always, "ERROR -nzbdir: '%s' failed after %v: %v", name, time.Since(start).Round(time.Second), err)
	} else {
		dlog(always, "-nzbdir: '%s' done in %v", name, time.Since(start).Round(time.Second))
	}
	dst, merr := moveInto(filepath.Join(w.dir, name), filepath.Join(w.dir, sub))
	delete(w.pending, name)
	if merr != nil {
		w.skip[name] = true // don't run it again in a loop
		dlog(always, "ERROR -nzbdir: moving '%s' to %s/: %v (it is skipped until restart)", name, sub, merr)
		return
	}
	dlog(cfg.opt.Verbose, "-nzbdir: moved '%s' to '%s'", name, dst)
}

// moveInto moves file into dir (created if needed). An existing name there
// gets a timestamp: "x.nzb" → "x-20261018-221500.nzb".
func moveInto(file, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(file)
	dst := filepath.Join(dir, name)
	if _, err := os.Lstat(dst); err == nil {
		ext := ".nzb"
		if strings.HasSuffix(name, ".nzb.gz") {
			ext = ".nzb.gz"
		}
		dst = filepath.Join(dir, strings.TrimSuffix(name, ext)+"-"+time.Now().Format("20060102-150405.000")+ext)
	}
	return dst, os.Rename(file, dst)
}

// loop scans the directory every refresh and processes what is ready, until Stop.
func (w *dirWatcher) loop() {
	dlog(always, "-nzbdir: watching '%s' every %v (processed NZBs move to %s/ and %s/)", w.dir, w.refresh, nzbDirDone, nzbDirFailed)
	tick := time.NewTicker(w.refresh)
	defer tick.Stop()
	for {
		names, err := w.scan()
		if err != nil {
			dlog(always, "ERROR -nzbdir: reading '%s': %v", w.dir, err)
		}
		for _, name := range names {
			if w.stopped() {
				return
			}
			w.processOne(name)
		}
		select {
		case <-w.stop:
			return
		case <-tick.C:
		}
	}
}

// dirShutdownOnSignal stops the watcher on SIGINT/SIGTERM (the running NZB is
// stopped and stays in place); a second signal exits at once.
func dirShutdownOnSignal(w *dirWatcher) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		dlog(always, "-nzbdir: got %v, stopping (again to force)", sig)
		go func() { <-sigs; dlog(always, "-nzbdir: forced exit"); os.Exit(1) }()
		w.Stop()
	}()
}
