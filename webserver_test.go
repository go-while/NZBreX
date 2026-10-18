package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	gotesting "testing"
)

const webTestNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
 <file poster="p" date="1700000000" subject="[1/1] &quot;a.bin&quot; yEnc (1/1)">
  <groups><group>alt.test</group></groups>
  <segments><segment bytes="100" number="1">seg1@nzbrex.test</segment></segments>
 </file>
</nzb>
`

// webTestServer returns the real routes (webHandler) with a fresh job queue.
func webTestServer(t *gotesting.T, auth *webAuth) http.Handler {
	t.Helper()
	testCfg()
	// other tests leave a bare &PROCESSOR{} (nil mutex) from StartWebServer
	oldQ, oldDir, oldP := jobQueue, webUploadDir, webProcessor
	jobQueue, webUploadDir, webProcessor = NewJobQueue(), t.TempDir(), nil
	t.Cleanup(func() { jobQueue, webUploadDir, webProcessor = oldQ, oldDir, oldP })
	h, err := webHandler("127.0.0.1:11911", auth)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type webResp struct {
	code  int
	ctype string
	body  string
	hdr   http.Header
}

func (r webResp) json(t *gotesting.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not JSON (%d %s): %q", r.code, r.ctype, r.body)
	}
	return m
}

func webDo(h http.Handler, method, path string, body io.Reader, hdr map[string]string) webResp {
	req := httptest.NewRequest(method, "http://127.0.0.1:11911"+path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return webResp{w.Code, w.Header().Get("Content-Type"), w.Body.String(), w.Header()}
}

var csrf = map[string]string{webRequestHeader: "1"}

func upload(t *gotesting.T, name, content string, fields map[string]string) (io.Reader, map[string]string) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write([]byte(content))
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	return &b, map[string]string{webRequestHeader: "1", "Content-Type": mw.FormDataContentType()}
}

// Every route: status, content type and the JSON fields the pages use.
func TestWebRoutes(t *gotesting.T) {
	h := webTestServer(t, nil)

	for _, page := range []string{"/", "/stats", "/jobs"} {
		r := webDo(h, "GET", page, nil, nil)
		if r.code != 200 || !strings.HasPrefix(r.ctype, "text/html") || !strings.Contains(r.body, "<html") {
			t.Errorf("GET %s: %d %q", page, r.code, r.ctype)
		}
	}
	if r := webDo(h, "GET", "/static/style.css", nil, nil); r.code != 200 || !strings.HasPrefix(r.ctype, "text/css") {
		t.Errorf("GET /static/style.css: %d %q", r.code, r.ctype)
	}
	if r := webDo(h, "GET", "/static/bootstrap.bundle.min.js", nil, nil); r.code != 200 || !strings.Contains(r.ctype, "javascript") {
		t.Errorf("GET bootstrap js: %d %q", r.code, r.ctype)
	}
	for _, p := range []string{"/nope", "/static/nope.css", "/api/nope"} {
		if r := webDo(h, "GET", p, nil, nil); r.code != 404 {
			t.Errorf("GET %s: %d, want 404", p, r.code)
		}
	}

	r := webDo(h, "GET", "/api/stats", nil, nil)
	if r.code != 200 || !strings.HasPrefix(r.ctype, "application/json") {
		t.Fatalf("GET /api/stats: %d %q", r.code, r.ctype)
	}
	if _, ok := r.json(t)["providers"]; !ok {
		t.Errorf("/api/stats has no providers field: %s", r.body)
	}
	if r := webDo(h, "GET", "/api/jobs", nil, nil); r.code != 200 || r.json(t)["jobs"] == nil {
		t.Errorf("GET /api/jobs: %d %s", r.code, r.body)
	}

	// job routes on a missing job
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/jobs/job-99/start"}, {"POST", "/api/jobs/job-99/stop"}, {"DELETE", "/api/jobs/job-99"},
	} {
		r := webDo(h, c.method, c.path, nil, csrf)
		if r.code != 404 || r.json(t)["error"] == nil {
			t.Errorf("%s %s: %d %s, want 404 with an error", c.method, c.path, r.code, r.body)
		}
	}
	if r := webDo(h, "POST", "/api/jobs/start-all", nil, csrf); r.code != 200 || r.json(t)["count"] != float64(0) {
		t.Errorf("start-all: %d %s", r.code, r.body)
	}
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/jobs/job-1/config"}, {"GET", "/api/jobs/default-config"}, {"PUT", "/api/jobs/default-config"},
	} {
		r := webDo(h, c.method, c.path, nil, csrf)
		if r.code != 501 || r.json(t)["config"] == nil {
			t.Errorf("%s %s: %d %s, want 501 with the startup config", c.method, c.path, r.code, r.body)
		}
	}

	// upload: no file, wrong type, too big, a good one with a config field
	if r := webDo(h, "POST", "/upload", strings.NewReader(""), csrf); r.code != 400 {
		t.Errorf("upload without a file: %d", r.code)
	}
	body, hdr := upload(t, "x.txt", "hello", nil)
	if r := webDo(h, "POST", "/upload", body, hdr); r.code != 400 {
		t.Errorf("upload of a .txt: %d %s", r.code, r.body)
	}
	body, hdr = upload(t, "big.nzb", strings.Repeat("x", webMaxUploadBytes+1), nil)
	if r := webDo(h, "POST", "/upload", body, hdr); r.code != 413 {
		t.Errorf("oversized upload: %d %s", r.code, r.body)
	}
	body, hdr = upload(t, "good.nzb", webTestNZB, map[string]string{"checkonly": "true"})
	r = webDo(h, "POST", "/upload", body, hdr)
	m := r.json(t)
	if r.code != 200 || m["job_id"] == nil || m["status"] != jobQueued || m["note"] == nil {
		t.Fatalf("good upload: %d %s", r.code, r.body)
	}
	id := m["job_id"].(string)
	jobs := webDo(h, "GET", "/api/jobs", nil, nil).json(t)["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["ID"] != id || jobs[0].(map[string]any)["total_articles"] != float64(1) {
		t.Fatalf("job list after upload: %v", jobs)
	}
	matches, _ := filepath.Glob(filepath.Join(webUploadDir, "*good.nzb"))
	if len(matches) != 1 {
		t.Fatalf("uploaded file not in the upload dir: %v", matches)
	}
	if r := webDo(h, "DELETE", "/api/jobs/"+id, nil, csrf); r.code != 200 || r.json(t)["job_id"] != id {
		t.Errorf("delete %s: %d %s", id, r.code, r.body)
	}
	if r := webDo(h, "DELETE", "/api/jobs/"+id, nil, csrf); r.code != 404 {
		t.Errorf("second delete: %d", r.code)
	}
}

// The guard runs in front of every route: Host check and the CSRF header.
func TestWebRoutesAreGuarded(t *gotesting.T) {
	h := webTestServer(t, nil)
	if r := webDo(h, "POST", "/api/jobs/start-all", nil, nil); r.code != 403 {
		t.Errorf("POST without %s: %d, want 403", webRequestHeader, r.code)
	}
	req := httptest.NewRequest("GET", "http://evil.example:11911/api/jobs", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("unknown Host: %d, want 421", w.Code)
	}
}

// With a login every route asks for it, pages and static files too.
func TestWebRoutesNeedLogin(t *gotesting.T) {
	file := filepath.Join(t.TempDir(), "webpasswd")
	writeWebUser(t, file, "webadmin0001", "secretpass01", 0)
	a, err := newWebAuth(file)
	if err != nil {
		t.Fatal(err)
	}
	h := webTestServer(t, a)
	for _, p := range []string{"/", "/api/jobs", "/static/style.css"} {
		r := webDo(h, "GET", p, nil, nil)
		if r.code != 401 || !strings.HasPrefix(r.hdr.Get("WWW-Authenticate"), "Basic") {
			t.Errorf("GET %s without login: %d %q", p, r.code, r.hdr.Get("WWW-Authenticate"))
		}
	}
	login := func(user, pass string) int {
		req := httptest.NewRequest("GET", "http://127.0.0.1:11911/api/jobs", nil)
		req.SetBasicAuth(user, pass)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := login("webadmin0001", "secretpass01"); c != 200 {
		t.Errorf("good login: %d", c)
	}
	if c := login("webadmin0001", "wrongpass001"); c != 401 {
		t.Errorf("wrong password: %d", c)
	}
}
