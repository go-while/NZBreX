package main

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"golang.org/x/crypto/bcrypt"
)

func guardRouter(allowed map[string]bool) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return webGuard(allowed, ok)
}

func doReq(r http.Handler, method, host string, hdr map[string]string) int {
	req := httptest.NewRequest(method, "http://"+host+"/x", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// Plan 4.4: POST /upload, /api/jobs/start-all and /api/jobs/:id/start were
// plain cross-site requests; DNS rebinding could reach a loopback server.
func TestWebGuardBlocksCrossSiteAndUnknownHosts(t *gotesting.T) {
	r := guardRouter(webAllowedHosts("127.0.0.1:11911", false))
	hdr := map[string]string{webRequestHeader: "1"}
	cases := []struct {
		name, method, host string
		hdr                map[string]string
		want               int
	}{
		{"GET needs no header", "GET", "127.0.0.1:11911", nil, 200},
		{"POST without header", "POST", "127.0.0.1:11911", nil, 403},
		{"POST with header", "POST", "127.0.0.1:11911", hdr, 200},
		{"POST localhost", "POST", "localhost:11911", hdr, 200},
		{"POST cross-site", "POST", "127.0.0.1:11911", map[string]string{webRequestHeader: "1", "Sec-Fetch-Site": "cross-site"}, 403},
		{"POST foreign Origin", "POST", "127.0.0.1:11911", map[string]string{webRequestHeader: "1", "Origin": "http://evil.example"}, 403},
		{"POST same Origin", "POST", "127.0.0.1:11911", map[string]string{webRequestHeader: "1", "Origin": "http://127.0.0.1:11911", "Sec-Fetch-Site": "same-origin"}, 200},
		{"rebound host", "GET", "evil.example:11911", nil, http.StatusMisdirectedRequest},
	}
	for _, c := range cases {
		if got := doReq(r, c.method, c.host, c.hdr); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	// with a login and no -webhosts any Host is accepted
	if got := doReq(guardRouter(webAllowedHosts(":11911", true)), "GET", "nzbrex.example.org", nil); got != 200 {
		t.Errorf("login, no -webhosts: status %d, want 200", got)
	}
}

func writeWebUser(t *gotesting.T, file, user, pass string, expireAt int64) {
	t.Helper()
	passwdHashCost = bcrypt.MinCost // fast tests; verifyUser's dummy hash keeps the default cost
	if err := appendPasswdUser(&UserData{Username: user, Password: pass, ExpireAt: expireAt}, file); err != nil {
		t.Fatal(err)
	}
}

func TestWebAuthLoginCacheAndReload(t *gotesting.T) {
	file := filepath.Join(t.TempDir(), "webpasswd")
	writeWebUser(t, file, "webadmin0001", "secretpass01", 0)
	writeWebUser(t, file, "webexpired01", "secretpass01", time.Now().Add(-time.Hour).Unix())
	a, err := newWebAuth(file)
	if err != nil {
		t.Fatal(err)
	}
	if !a.check("webadmin0001", "secretpass01") {
		t.Fatal("valid login rejected")
	}
	if a.check("webadmin0001", "wrongpass001") || a.check("nosuchuser01", "secretpass01") || a.check("webexpired01", "secretpass01") {
		t.Fatal("bad login accepted")
	}
	start := time.Now()
	for i := 0; i < 50; i++ {
		a.check("webadmin0001", "secretpass01")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("50 cached checks took %v: the cache is not used", d)
	}
	// a new user from -webadduser (.new.* file) is picked up without a restart
	writeWebUser(t, file+".new.1.1", "webadmin0002", "secretpass02", 0)
	if !a.check("webadmin0002", "secretpass02") {
		t.Fatal("user added via .new file not picked up")
	}
}

// Plan 5.2: an unknown user returned before bcrypt (timing oracle).
func TestVerifyUserUnknownCostsBcrypt(t *gotesting.T) {
	file := filepath.Join(t.TempDir(), "pw")
	writeWebUser(t, file, "someuser0001", "secretpass01", 0)
	users, err := parsePasswdFile(file, passwdOpts{neverExpires: true})
	if err != nil {
		t.Fatal(err)
	}
	timeIt := func(user string) time.Duration {
		start := time.Now()
		for i := 0; i < 5; i++ {
			verifyUser(users, user, "wrongpass001")
		}
		return time.Since(start)
	}
	known, unknown := timeIt("someuser0001"), timeIt("nosuchuser01")
	if unknown < known/3 {
		t.Fatalf("unknown user took %v, known user with a wrong password %v: the miss path skips bcrypt", unknown, known)
	}
}

// Plan 5.2: loadPasswdFile cleared the live map first, so a failed or empty
// reload locked everyone out until restart.
func TestLoadPasswdFileKeepsUsersOnBadReload(t *gotesting.T) {
	file := filepath.Join(t.TempDir(), "proxypasswd")
	writeWebUser(t, file, "proxyuser001", "secretpass01", time.Now().Add(time.Hour).Unix())
	old := passwdMap
	t.Cleanup(func() { proxyMutex.Lock(); passwdMap = old; proxyMutex.Unlock() })
	if err := loadPasswdFile(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("garbage\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loadPasswdFile(file)
	if !verifyPassword("proxyuser001", "secretpass01") {
		t.Fatal("an empty reload removed the loaded users")
	}
	os.Remove(file)
	loadPasswdFile(file)
	if !verifyPassword("proxyuser001", "secretpass01") {
		t.Fatal("a failed reload removed the loaded users")
	}
}

// D1: no login is only allowed on loopback.
func TestStartWebServerRefusesPublicWithoutLogin(t *gotesting.T) {
	testCfg()
	old := webPasswdFile
	webPasswdFile = ""
	t.Cleanup(func() { webPasswdFile = old })
	if err := StartWebServer("0.0.0.0:0", &PROCESSOR{}); err == nil || !strings.Contains(err.Error(), "without a login") {
		t.Fatalf("public bind without login: err=%v", err)
	}
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, ":1": false, "0.0.0.0:1": false, "10.1.2.3:1": false} {
		if got := isLoopbackAddr(addr); got != want {
			t.Errorf("isLoopbackAddr(%q) = %t, want %t", addr, got, want)
		}
	}
}

// Plan 4.4: uploads went to a fixed /tmp dir under the client's file name:
// same-name uploads overwrote each other, a planted symlink was followed.
func TestSaveUploadUniqueAndNoSymlinkFollow(t *gotesting.T) {
	old := webUploadDir
	webUploadDir = t.TempDir()
	t.Cleanup(func() { webUploadDir = old })

	if n := uploadName(`..\..\etc/pass wd.nzb`); strings.ContainsAny(n, `/\ `) || !strings.HasSuffix(n, ".nzb") {
		t.Fatalf("unsafe upload name %q", n)
	}
	a, b := uploadName("same.nzb"), uploadName("same.nzb")
	if a == b {
		t.Fatalf("two uploads of the same name got the same file name %q", a)
	}

	// a symlink planted at the next name must make the save fail, not be followed
	target := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(target, []byte("keep"), 0600)
	next := webUploadSeq.Load() + 1
	os.Symlink(target, filepath.Join(webUploadDir, uploadName("x.nzb")))
	webUploadSeq.Store(next - 1) // the save reuses that name
	fh := multipartFile(t, "x.nzb", "data")
	if _, err := saveUpload(fh); err == nil {
		t.Fatal("save over a planted symlink succeeded")
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatalf("symlink target was overwritten: %q", got)
	}
}

func multipartFile(t *gotesting.T, name, data string) *multipart.FileHeader {
	t.Helper()
	body := &strings.Builder{}
	w := multipart.NewWriter(body)
	fw, _ := w.CreateFormFile("file", name)
	fw.Write([]byte(data))
	w.Close()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	return req.MultipartForm.File["file"][0]
}
