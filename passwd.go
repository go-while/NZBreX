package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Passwd files hold one user per line, passwords as bcrypt hashes:
//
//	username|bcrypt_hash|maxconns|ExpireAt_unix|(no)post
//
// The proxy (-proxypasswdfile) and the web server (-webpasswd) use the same
// format but separate files and separate maps: proxy users get no web access.
// The web server ignores maxconns and post. ExpireAt 0 means "never" only where
// passwdOpts.neverExpires allows it (the web file); proxy accounts always expire.

const minUsernameLen = 10

type passwdOpts struct {
	neverExpires bool // ExpireAt 0 = no expiry
}

// passwdHashCost is the bcrypt cost for new users (tests lower it).
var passwdHashCost = bcrypt.DefaultCost

// dummyHash is compared on a miss, so an unknown user costs as much as a wrong
// password (plan 5.2: the miss path returned before bcrypt, a timing oracle).
// Made on first use, not at startup.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("nzbrex-no-such-user"), bcrypt.DefaultCost)
	return h
})

// parsePasswdFile reads filename into a new map. Malformed, short, duplicate and
// expired entries are skipped with a log line. It never touches a live map.
func parsePasswdFile(filename string, opts passwdOpts) (map[string]*UserData, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open passwd file %s: %w", filename, err)
	}
	defer file.Close()

	users := make(map[string]*UserData)
	dupes := make(map[string]bool)
	now := time.Now().Unix()
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { // Skip empty lines and comments
			continue
		}
		parts := strings.SplitN(line, "|", 5) // username|bcrypt_hash|maxconns|ExpireAt|(no)post
		if len(parts) != 5 {
			log.Printf("Skipping malformed line %d in passwd file '%s' (expected 5 parts, got %d)", lineNumber, filename, len(parts))
			continue
		}
		username := strings.TrimSpace(parts[0])
		hash := parts[1]
		maxconns, err := strconv.Atoi(parts[2])
		if err != nil {
			log.Printf("Skipping line %d in passwd file '%s' for user '%s': invalid maxconns '%s'", lineNumber, filename, username, parts[2])
			continue
		}
		expireAt, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil {
			log.Printf("Skipping line %d in passwd file '%s' for user '%s': invalid ExpireAt '%s'", lineNumber, filename, username, parts[3])
			continue
		}
		if !(expireAt == 0 && opts.neverExpires) && expireAt < now {
			log.Printf("Skipping line %d in passwd file '%s' for user '%s': expired (ExpireAt: %d, now: %d)", lineNumber, filename, username, expireAt, now)
			continue
		}
		if len(username) < minUsernameLen {
			log.Printf("Skipping line %d in passwd file '%s': username shorter than %d characters", lineNumber, filename, minUsernameLen)
			continue
		}
		if len(hash) < 60 {
			// bcrypt hashes are 60 characters long
			log.Printf("Skipping user '%s' on line %d in passwd file '%s': empty or short password hash", username, lineNumber, filename)
			continue
		}
		if dupes[username] {
			continue
		}
		if _, exists := users[username]; exists {
			// a name listed twice is ambiguous: drop it entirely
			delete(users, username)
			dupes[username] = true
			log.Printf("WARNING: Skipping duplicate user '%s' on line %d in passwd file '%s'.", username, lineNumber, filename)
			continue
		}
		users[username] = &UserData{
			Username: username,
			Password: hash,
			MaxConns: maxconns,
			ExpireAt: expireAt,
			Posting:  strings.HasPrefix(strings.ToLower(parts[4]), "post"),
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading passwd file %s: %w", filename, err)
	}
	return users, nil
}

// verifyUser checks user and password against users. It costs one bcrypt
// comparison whether or not the user exists, and rejects expired accounts.
func verifyUser(users map[string]*UserData, user, plainPassword string) (*UserData, bool) {
	userData, ok := users[user]
	hash := dummyHash()
	if ok {
		hash = []byte(userData.Password)
	}
	match := bcrypt.CompareHashAndPassword(hash, []byte(plainPassword)) == nil
	if !ok || !match {
		return nil, false
	}
	if userData.ExpireAt != 0 && userData.ExpireAt < time.Now().Unix() {
		return nil, false
	}
	return userData, true
}

// mergeNewPasswdFiles appends filename.new.* files (written by -proxyadduser /
// -webadduser) to filename and removes them.
func mergeNewPasswdFiles(filename string) {
	dir := filepath.Dir(filename)
	pattern := filepath.Base(filename) + ".new."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), pattern) {
			continue
		}
		newfile := filepath.Join(dir, entry.Name())
		func() {
			mainf, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
			if err != nil {
				log.Printf("Could not open %s for appending: %v", filename, err)
				return
			}
			f, err := os.Open(newfile)
			if err != nil {
				log.Printf("Could not open %s: %v", newfile, err)
				mainf.Close() // nothing was written
				return
			}
			defer f.Close()
			_, err = io.Copy(mainf, f)
			// a failed close can lose the appended users: keep the .new file then
			if err = errors.Join(err, mainf.Close()); err != nil {
				log.Printf("Failed to import %s into %s: %v", newfile, filename, err)
				return
			}
			log.Printf("Imported new user(s) from %s into %s", newfile, filename)
			os.Remove(newfile)
		}()
	}
}

// appendPasswdUser hashes userData.Password (plaintext) and appends the user to filename.
func appendPasswdUser(userData *UserData, filename string) error {
	if userData.Password == "" {
		return fmt.Errorf("cannot add user '%s': plaintext password is empty", userData.Username)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(userData.Password), passwdHashCost)
	if err != nil {
		return fmt.Errorf("failed to generate bcrypt hash for user '%s': %w", userData.Username, err)
	}
	posting := "nopost"
	if userData.Posting {
		posting = "post"
	}
	line := fmt.Sprintf("%s|%s|%d|%d|%s\n", userData.Username, hashed, userData.MaxConns, userData.ExpireAt, posting)
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("failed to open passwd file '%s' for appending: %w", filename, err)
	}
	_, err = file.WriteString(line)
	if err = errors.Join(err, file.Close()); err != nil {
		return fmt.Errorf("failed to write new user '%s' to passwd file '%s': %w", userData.Username, filename, err)
	}
	log.Printf("Successfully added user '%s' to '%s' with a bcrypt hashed password.", userData.Username, filename)
	return nil
}
