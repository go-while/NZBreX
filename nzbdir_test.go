package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	gotesting "testing"
	"time"
)

func testWatcher(t *gotesting.T) (*dirWatcher, string, *[]string) {
	t.Helper()
	testCfg()
	dir := t.TempDir()
	var ran []string
	w := &dirWatcher{dir: dir, refresh: time.Second, pending: map[string]dirFile{}, skip: map[string]bool{}, stop: make(chan struct{})}
	w.run = func(name string) error {
		ran = append(ran, name)
		if strings.HasPrefix(name, "bad") {
			return errors.New("broken NZB")
		}
		return nil
	}
	return w, dir, &ran
}

func writeAt(t *gotesting.T, path, data string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// A file is taken on its second look if it didn't change; oldest first;
// other files and directories are ignored.
func TestDirWatcherScan(t *gotesting.T) {
	w, dir, _ := testWatcher(t)
	base := time.Now().Add(-time.Hour)
	writeAt(t, filepath.Join(dir, "b.nzb"), "b", base.Add(2*time.Minute))
	writeAt(t, filepath.Join(dir, "a.nzb.gz"), "a", base.Add(time.Minute))
	writeAt(t, filepath.Join(dir, "notes.txt"), "x", base)
	os.Mkdir(filepath.Join(dir, "sub.nzb"), 0o755)

	if got, _ := w.scan(); len(got) != 0 {
		t.Fatalf("first scan took %v; files need a second, unchanged look", got)
	}
	writeAt(t, filepath.Join(dir, "b.nzb"), "b grew", base.Add(3*time.Minute)) // still being written
	got, _ := w.scan()
	if len(got) != 1 || got[0] != "a.nzb.gz" {
		t.Fatalf("second scan: %v, want [a.nzb.gz] (b.nzb changed)", got)
	}
	got, _ = w.scan()
	if len(got) != 2 || got[0] != "a.nzb.gz" || got[1] != "b.nzb" {
		t.Fatalf("third scan: %v, want [a.nzb.gz b.nzb] (oldest first)", got)
	}
}

// Success goes to done/, failure to failed/; a name already there gets a timestamp.
func TestDirWatcherFilesResults(t *gotesting.T) {
	w, dir, ran := testWatcher(t)
	for _, n := range []string{"ok.nzb", "bad.nzb"} {
		writeAt(t, filepath.Join(dir, n), n, time.Now())
	}
	w.processOne("ok.nzb")
	w.processOne("bad.nzb")
	if _, err := os.Stat(filepath.Join(dir, nzbDirDone, "ok.nzb")); err != nil {
		t.Errorf("ok.nzb not in done/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, nzbDirFailed, "bad.nzb")); err != nil {
		t.Errorf("bad.nzb not in failed/: %v", err)
	}
	// the same name again: kept apart, nothing overwritten
	writeAt(t, filepath.Join(dir, "ok.nzb"), "second", time.Now())
	w.processOne("ok.nzb")
	matches, _ := filepath.Glob(filepath.Join(dir, nzbDirDone, "ok*.nzb"))
	if len(matches) != 2 {
		t.Errorf("done/ after a second ok.nzb: %v, want 2 files", matches)
	}
	if len(*ran) != 3 {
		t.Errorf("ran %v", *ran)
	}
}

// A stopped run leaves its file in place for the next start.
func TestDirWatcherStopLeavesFile(t *gotesting.T) {
	w, dir, _ := testWatcher(t)
	writeAt(t, filepath.Join(dir, "x.nzb"), "x", time.Now())
	w.run = func(string) error { w.Stop(); return errDirStopped }
	w.processOne("x.nzb")
	if _, err := os.Stat(filepath.Join(dir, "x.nzb")); err != nil {
		t.Fatalf("x.nzb moved after a stop: %v", err)
	}
}

// A file that can't be moved is not run again and again.
func TestDirWatcherSkipsUnmovable(t *gotesting.T) {
	w, dir, ran := testWatcher(t)
	writeAt(t, filepath.Join(dir, "ok.nzb"), "x", time.Now().Add(-time.Minute))
	writeAt(t, filepath.Join(dir, nzbDirDone), "a file, not a dir", time.Now()) // blocks done/
	w.scan()
	names, _ := w.scan()
	for _, n := range names {
		w.processOne(n)
	}
	names, _ = w.scan()
	names2, _ := w.scan()
	if len(names)+len(names2) != 0 || len(*ran) != 1 {
		t.Fatalf("unmovable file came back: scans %v %v, ran %v", names, names2, *ran)
	}
}

// -nzbdir alone is a valid mode now; with another mode it is still refused.
func TestStartupModeNzbDir(t *gotesting.T) {
	if err := startupModeError(false, "/x", false, false); err != nil {
		t.Errorf("-nzbdir alone: %v", err)
	}
	for _, c := range []struct{ nzb, web, proxy bool }{{true, false, false}, {false, true, false}, {false, false, true}} {
		if err := startupModeError(c.nzb, "/x", c.web, c.proxy); err == nil {
			t.Errorf("-nzbdir with %+v accepted", c)
		}
	}
}
