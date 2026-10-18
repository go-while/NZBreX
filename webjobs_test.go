package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	gotesting "testing" // Workers.go declares a const named testing
)

const testNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
 <file poster="t" date="1" subject="&quot;a.bin&quot; yEnc (1/2)">
  <groups><group>alt.test</group></groups>
  <segments>
   <segment bytes="100" number="1">a1@test</segment>
   <segment bytes="100" number="2">a2@test</segment>
  </segments>
 </file>
</nzb>
`

func writeFile(t *gotesting.T, name, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Plan 4.3: AddJob swallowed the parse error and accepted a corrupt file.
func TestAddJobRejectsBrokenNZB(t *gotesting.T) {
	testCfg()
	jq := NewJobQueue()
	if _, err := jq.AddJob(writeFile(t, "bad.nzb", "this is not xml"), "bad.nzb"); err == nil {
		t.Fatal("a broken NZB was accepted")
	}
	id, err := jq.AddJob(writeFile(t, "ok.nzb", testNZB), "ok.nzb")
	if err != nil {
		t.Fatalf("valid NZB rejected: %v", err)
	}
	job, ok := jq.GetJob(id)
	if !ok || job.TotalArticles != 2 || job.Status != jobQueued {
		t.Fatalf("job = %+v ok=%t, want 2 articles, queued", job, ok)
	}
}

// Plan 4.2: ListJobs handed out live pointers and wrote progress into them
// under a read lock. It now returns copies; changing one changes nothing.
func TestListJobsReturnsCopies(t *gotesting.T) {
	testCfg()
	jq := NewJobQueue()
	id, err := jq.AddJob(writeFile(t, "ok.nzb", testNZB), "ok.nzb")
	if err != nil {
		t.Fatal(err)
	}
	jobs := jq.ListJobs()
	jobs[0].Status = "tampered"
	if job, _ := jq.GetJob(id); job.Status != jobQueued {
		t.Fatalf("changing a listed job changed the queue: status=%q", job.Status)
	}
}

// A running job's progress comes from its own session, and the final counts
// are kept when it finishes (it used to advance only while someone polled).
func TestJobProgressFromItsSession(t *gotesting.T) {
	testCfg()
	jq := NewJobQueue()
	id, _ := jq.AddJob(writeFile(t, "ok.nzb", testNZB), "ok.nzb")
	s := &SESSION{}
	s.progress.total.Store(4)
	s.progress.done.Store(1)
	s.progress.dead.Store(1)
	jq.mu.Lock()
	_, job := jq.find(id)
	job.Status, job.session = jobRunning, s
	jq.mu.Unlock()

	if got, _ := jq.GetJob(id); got.Progress != 0.5 || got.ArticlesProcessed != 2 || got.TotalArticles != 4 {
		t.Fatalf("running job: progress=%v processed=%d total=%d, want 0.5 2 4", got.Progress, got.ArticlesProcessed, got.TotalArticles)
	}
	s.progress.done.Store(3)
	jq.mu.Lock()
	jq.finishLocked(job, nil)
	jq.mu.Unlock()
	if got, _ := jq.GetJob(id); got.Status != jobCompleted || got.Progress != 1 || got.ArticlesDone != 3 {
		t.Fatalf("finished job: %+v, want completed with progress 1 and 3 done", got)
	}
}

func TestJobStateErrors(t *gotesting.T) {
	testCfg()
	jq := NewJobQueue()
	if err := jq.StartJob("job-404"); !errors.Is(err, errJobNotFound) {
		t.Fatalf("start unknown job: err=%v, want errJobNotFound", err)
	}
	id, _ := jq.AddJob(writeFile(t, "ok.nzb", testNZB), "ok.nzb")
	jq.mu.Lock()
	_, job := jq.find(id)
	job.Status = jobRunning
	jq.mu.Unlock()
	if err := jq.StartJob(id); !errors.Is(err, errJobState) {
		t.Fatalf("start running job: err=%v, want errJobState", err)
	}
	if err := jq.DeleteJob(id); !errors.Is(err, errJobState) {
		t.Fatalf("delete running job: err=%v, want errJobState", err)
	}
	if jobErrStatus(errJobNotFound) != 404 || jobErrStatus(errJobState) != 409 {
		t.Fatal("wrong HTTP status mapping")
	}
}

// Plan 4.1: a busy port was only logged from a goroutine; StartWebServer returned nil.
func TestStartWebServerReportsBindError(t *gotesting.T) {
	testCfg()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := StartWebServer(ln.Addr().String(), &PROCESSOR{}); err == nil {
		t.Fatal("StartWebServer on a busy port returned no error")
	}
}
