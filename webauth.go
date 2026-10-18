package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Web login (D1): HTTP basic auth against its own passwd file (-webpasswd),
// same format as the proxy's but a separate map. Without a file the server
// only runs on a loopback address.

var (
	webPasswdFile string // flag -webpasswd
	webAddUser    string // flag -webadduser 'username|password'
	webHosts      string // flag -webhosts 'nzbrex.example.org,10.0.0.5'
)

const (
	webAuthCacheTTL   = 5 * time.Minute // a verified user|password pair skips bcrypt this long
	webAuthMaxChecks  = 4               // concurrent bcrypt comparisons
	webRequestHeader  = "X-NZBreX-Request"
	webMaxUploadBytes = 128 << 20 // one NZB upload, multipart overhead included
)

// Failed logins per client address: after webLoginMaxFails within
// webLoginWindow the address gets 429 for webLoginBlock, without a bcrypt
// check. Variables for the tests.
var (
	webLoginMaxFails = 10
	webLoginWindow   = 10 * time.Minute
	webLoginBlock    = 10 * time.Minute
	webLoginTrackMax = 10000 // addresses remembered; beyond that the oldest records go
)

// loginFails is one client address's recent failed logins.
type loginFails struct {
	count        int
	first        time.Time // start of the current window
	blockedUntil time.Time
}

type webAuth struct {
	file    string
	mu      sync.Mutex
	users   map[string]*UserData
	modTime time.Time
	size    int64
	cache   map[[32]byte]time.Time // sha256(user, password) → when the check expires
	checks  chan struct{}
	fails   map[string]*loginFails // client address → failed logins (under mu)
}

// newWebAuth loads the web passwd file. A file with no usable users is an
// error: the login would lock everyone out.
func newWebAuth(file string) (*webAuth, error) {
	a := &webAuth{file: file, cache: make(map[[32]byte]time.Time), checks: make(chan struct{}, webAuthMaxChecks), fails: make(map[string]*loginFails)}
	mergeNewPasswdFiles(file)
	if err := a.load(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	n := len(a.users)
	a.mu.Unlock()
	if n == 0 {
		return nil, fmt.Errorf("web passwd file '%s' has no usable users (add one with -webadduser)", file)
	}
	log.Printf("Web login: loaded %d users from '%s'", n, file)
	return a, nil
}

// load parses the file and swaps the users in if it has any.
func (a *webAuth) load() error {
	st, err := os.Stat(a.file)
	if err != nil {
		return fmt.Errorf("web passwd file: %w", err)
	}
	users, err := parsePasswdFile(a.file, passwdOpts{neverExpires: true})
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.modTime, a.size = st.ModTime(), st.Size()
	if len(users) == 0 && a.users != nil {
		log.Printf("Warning: web passwd file '%s' has no usable users now, keeping the %d loaded before", a.file, len(a.users))
		return nil
	}
	a.users = users
	clear(a.cache) // passwords may have changed
	return nil
}

// reloadIfChanged picks up edits to the file and new .new.* files.
func (a *webAuth) reloadIfChanged() {
	mergeNewPasswdFiles(a.file)
	st, err := os.Stat(a.file)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := !st.ModTime().Equal(a.modTime) || st.Size() != a.size
	a.mu.Unlock()
	if changed {
		if err := a.load(); err != nil {
			log.Printf("Warning: reloading web passwd file failed, keeping the old users: %v", err)
		}
	}
}

func authCacheKey(user, pass string) [32]byte {
	return sha256.Sum256([]byte(user + "\x00" + pass))
}

// check verifies user and password. Successful checks are cached for
// webAuthCacheTTL: the pages poll every few seconds, bcrypt costs ~50–100 ms.
func (a *webAuth) check(user, pass string) bool {
	if a.cached(user, pass) {
		return true
	}
	key := authCacheKey(user, pass)
	now := time.Now()

	a.reloadIfChanged()
	a.checks <- struct{}{} // bound the CPU a flood of guesses can take
	a.mu.Lock()
	users := a.users // replaced, never changed in place: safe to read unlocked
	a.mu.Unlock()
	_, ok := verifyUser(users, user, pass)
	<-a.checks
	if !ok {
		return false
	}
	a.mu.Lock()
	for k, exp := range a.cache {
		if now.After(exp) {
			delete(a.cache, k)
		}
	}
	a.cache[key] = now.Add(webAuthCacheTTL)
	a.mu.Unlock()
	return true
}

// cached reports whether user and password passed a check within webAuthCacheTTL.
func (a *webAuth) cached(user, pass string) bool {
	key := authCacheKey(user, pass)
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.cache[key]
	return ok && time.Now().Before(exp)
}

// blockedFor returns how long ip is still blocked after too many failed logins (0: not).
func (a *webAuth) blockedFor(ip string, now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if f := a.fails[ip]; f != nil && now.Before(f.blockedUntil) {
		return f.blockedUntil.Sub(now)
	}
	return 0
}

// loginFailed records a failed login from ip and reports whether ip is now blocked.
func (a *webAuth) loginFailed(ip string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.fails[ip]
	if f == nil || now.Sub(f.first) > webLoginWindow {
		if f == nil && len(a.fails) >= webLoginTrackMax {
			a.pruneFails(now)
		}
		f = &loginFails{first: now}
		a.fails[ip] = f
	}
	f.count++
	if f.count >= webLoginMaxFails {
		f.blockedUntil = now.Add(webLoginBlock)
		f.count, f.first = 0, now // the next window starts after the block
		return true
	}
	return false
}

// loginOK forgets ip's failed logins.
func (a *webAuth) loginOK(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

// pruneFails makes room in the failed-login table. First records that are
// neither blocked nor in their window; if the table is still full, unblocked
// ones until it is half full, and only then blocked ones (more than
// webLoginTrackMax addresses guessing at once). Caller holds mu.
func (a *webAuth) pruneFails(now time.Time) {
	for ip, f := range a.fails {
		if now.After(f.blockedUntil) && now.Sub(f.first) > webLoginWindow {
			delete(a.fails, ip)
		}
	}
	for _, keepBlocked := range []bool{true, false} {
		for ip, f := range a.fails {
			if len(a.fails) < webLoginTrackMax/2 {
				return
			}
			if !keepBlocked || now.After(f.blockedUntil) {
				delete(a.fails, ip)
			}
		}
	}
}

// middleware asks for basic auth on every request, and limits failed logins
// per client address (brute force): blocked addresses get 429 without a
// bcrypt check. Credentials already verified (cached) still pass, so other
// users behind the same address, e.g. a reverse proxy, keep working.
func (a *webAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && a.cached(user, pass) {
			next.ServeHTTP(w, r)
			return
		}
		ip, now := peerIP(r), time.Now()
		if wait := a.blockedFor(ip, now); wait > 0 {
			w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
			writeJSON(w, http.StatusTooManyRequests, H{"error": "too many failed logins, try again later"})
			return
		}
		if !ok || !a.check(user, pass) {
			if ok {
				log.Printf("Web: failed login for user '%s' from %s", user, ip)
				if a.loginFailed(ip, now) {
					log.Printf("Web: %s blocked for %v after %d failed logins", ip, webLoginBlock, webLoginMaxFails)
				}
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="NZBreX", charset="UTF-8"`)
			writeJSON(w, http.StatusUnauthorized, H{"error": "login required"})
			return
		}
		a.loginOK(ip)
		next.ServeHTTP(w, r)
	})
}

// peerIP is the client's address: the TCP peer, never X-Forwarded-For.
func peerIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// isLoopbackAddr reports whether a listen address only accepts local clients.
// An empty or wildcard host (":11911", "0.0.0.0:11911") is not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// webAllowedHosts returns the Host header values to accept, or nil for any.
// Without a login, a fixed list is enforced (DNS rebinding: a page on another
// domain resolving to 127.0.0.1 would otherwise drive the server). With a login,
// only -webhosts restricts it.
func webAllowedHosts(addr string, login bool) map[string]bool {
	if login && webHosts == "" {
		return nil
	}
	allowed := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		allowed[strings.ToLower(host)] = true
	}
	for _, h := range strings.Split(webHosts, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allowed[h] = true
		}
	}
	return allowed
}

// hostOnly strips the port from a Host header value.
func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

// webGuard checks the Host header and protects state-changing requests from
// cross-site requests (CSRF). Basic auth alone doesn't: browsers send cached
// credentials cross-site. The custom header forces a CORS preflight, which
// this server never answers, and Origin / Sec-Fetch-Site must be this site.
func webGuard(allowedHosts map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowedHosts != nil && !allowedHosts[hostOnly(r.Host)] {
			writeJSON(w, http.StatusMisdirectedRequest, H{"error": "unknown host"})
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(webRequestHeader) != "1" {
			writeJSON(w, http.StatusForbidden, H{"error": "missing " + webRequestHeader + " header"})
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			writeJSON(w, http.StatusForbidden, H{"error": "cross-site request refused"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
				writeJSON(w, http.StatusForbidden, H{"error": "cross-origin request refused"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
