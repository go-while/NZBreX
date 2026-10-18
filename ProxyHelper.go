package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"
)

// LinesWriter sends status and the article lines (dot-stuffed, dot-terminated)
// and flushes. The flush error is returned: it used to be only logged.
func LinesWriter(w *bufio.Writer, status string, lines []string) (txb uint64, err error) {
	write := func(s string) bool {
		n, werr := io.WriteString(w, s)
		txb += uint64(n)
		err = werr
		return werr == nil
	}
	if !write(status + CRLF) {
		return txb, err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, ".") {
			line = "." + line // dot-stuffing
		}
		if !write(line + CRLF) {
			return txb, err
		}
	}
	if !write(DOT + CRLF) {
		return txb, err
	}
	return txb, w.Flush()
} // end func LinesWriter

func (ps *ProxySession) printCapabilities() {
	// RFC 3977 §5.2
	ps.writeDotBlock("101 Capability list:", []string{
		"VERSION 2",
		"READER",
		"AUTHINFO USER", // RFC 4643 §2.1: the argument is the list of mechanisms ("USER" for USER/PASS)
	})
}

// loadPasswdFile merges filename.new.* into filename and loads the proxy users.
// The live map is replaced only by a successfully parsed, non-empty one (plan 5.2:
// it used to be cleared first, so a failed or empty reload locked everyone out
// until restart). Emptying the file therefore keeps the previous users.
func loadPasswdFile(filename string) error {
	mergeNewPasswdFiles(filename)
	users, err := parsePasswdFile(filename, passwdOpts{})
	if err != nil {
		return err
	}
	if len(users) == 0 {
		log.Printf("Warning: No users loaded from passwd file '%s', keeping the %d users loaded before.", filename, proxyUserCount())
		return nil
	}
	proxyMutex.Lock()
	passwdMap = users // a new map: readers holding the old one are unaffected
	proxyMutex.Unlock()
	log.Printf("Successfully loaded %d users from '%s'.", len(users), filename)
	return nil
}

// proxyUserCount returns the number of loaded proxy users.
func proxyUserCount() int {
	proxyMutex.RLock()
	defer proxyMutex.RUnlock()
	return len(passwdMap)
}

// verifyPassword checks a proxy user's password and expiry. The map is never
// changed after it is published, so it is read outside proxyMutex.
func verifyPassword(user string, plainPassword string) bool {
	proxyMutex.RLock()
	users := passwdMap
	proxyMutex.RUnlock()
	_, ok := verifyUser(users, user, plainPassword)
	return ok
}

// addUserToProxyPasswdFile adds a new user to the passwd file with a bcrypt hashed password.
// The UserData.Password field should contain the plaintext password for the new user.
func addUserToProxyPasswdFile(userData *UserData, filename string) error {
	return appendPasswdUser(userData, filename)
}

const DefaultPasswordLength = 8 // 8 bytes will give us 16 hex characters, which is a common length for usernames/passwords

// generateRandomHexCredentials generates a random username and password, each 8 bytes, output as hex strings.
func generateRandomHexCredentials() (username string, password string, err error) {
	// Use crypto/rand for cryptographically secure random bytes
	// If you want longer usernames/passwords, adjust the length accordingly.
	// For example, 10 bytes would give 20 hex characters.
	buf := make([]byte, DefaultPasswordLength)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("failed to generate random username: %w", err)
	}
	username = fmt.Sprintf("%x", buf)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("failed to generate random password: %w", err)
	}
	password = fmt.Sprintf("%x", buf)
	return username, password, nil
}

func GoCliRxTxCounter() {
	// GoCliRxTxCounter starts a goroutine to periodically log received and sent bytes per user
	go func() {
		for {
			si := <-statsChan // Wait for stats items from the channel
			CliRxTxMux.Lock() // Lock the global mutex to ensure thread-safe access to CliRxTxCounter
			if si.clear != nil {
				delete(CliRxTxCounter, si.username)
				CliRxTxMux.Unlock()
				continue
			}
			if _, ok := CliRxTxCounter[si.username]; !ok {
				CliRxTxCounter[si.username] = NewCounter(4) // Initialize counter for the user if it doesn't exist
			}
			if si.rxbytes > 0 {
				CliRxTxCounter[si.username].Add("tmpRXBytes", si.rxbytes)
			}
			if si.txbytes > 0 {
				CliRxTxCounter[si.username].Add("tmpTXBytes", si.txbytes)
			}
			CliRxTxMux.Unlock() // Unlock the global mutex
		}
	}()
	go func() {
		for {
			time.Sleep(time.Minute)
			CliRxTxMux.Lock()
			for username, counter := range CliRxTxCounter {
				if counter == nil {
					continue
				}
				rxbytes := counter.GetReset("tmpRXBytes") // Get and reset temporary RX bytes
				txbytes := counter.GetReset("tmpTXBytes") // Get and reset temporary TX bytes
				var RXspeedInKB, TXspeedInKB float64
				if rxbytes > 0 {
					counter.Add("RXBytes", rxbytes)
					RXspeedInKB = float64(rxbytes) / 1024 / 60 // Calculate users upload speed in KB/s (proxy received)
				}
				if txbytes > 0 {
					counter.Add("TXBytes", txbytes)
					TXspeedInKB = float64(txbytes) / 1024 / 60 // Calculate users download speed in KB/s (proxy transceived)
				}
				var rxMiB, txMiB float64
				trx := counter.GetValue("RXBytes")
				ttx := counter.GetValue("TXBytes")
				if trx > 1024*1024 {
					rxMiB = float64(trx) / 1024 / 1024
				}
				if ttx > 1024*1024 {
					txMiB = float64(ttx) / 1024 / 1024
				}
				if RXspeedInKB > 0 || TXspeedInKB > 0 {
					log.Printf(" %s | session DL speed: %.0f KiB/s (%.0f MiB) [%d bytes] | session UL speed: %.0f KiB/s (%.0f MiB) [%d bytes]", username, TXspeedInKB, txMiB, ttx, RXspeedInKB, rxMiB, trx)
				} else {
					log.Printf(" %s | idle, no data transfer in last minute", username)
					// TODO close session if no data transfer in last minute?
				}
			}
			CliRxTxMux.Unlock()
		}
	}()
}

// IsArticleNotFoundAtProviderGroup reports a fresh negative-cache entry.
func IsArticleNotFoundAtProviderGroup(key string, providerGroup string) bool {
	articleNotFound.mux.RLock()
	defer articleNotFound.mux.RUnlock()
	exp, ok := articleNotFound.Map[providerGroup][key]
	return ok && time.Now().Before(exp) // expired entries go in the janitor's next run
}

// SetArticleNotFoundAtProviderGroup caches a miss for negCacheTTL. A full cache
// (negCacheMax entries) caches nothing more until the janitor frees room.
func SetArticleNotFoundAtProviderGroup(key string, providerGroup string) {
	articleNotFound.mux.Lock()
	defer articleNotFound.mux.Unlock()
	m := articleNotFound.Map[providerGroup]
	if m == nil {
		m = make(map[string]time.Time)
		articleNotFound.Map[providerGroup] = m
	}
	if _, exists := m[key]; !exists {
		if articleNotFound.n >= negCacheMax {
			return
		}
		articleNotFound.n++
	}
	m[key] = time.Now().Add(negCacheTTL)
}

// purgeNegCache drops expired entries.
func purgeNegCache(now time.Time) {
	articleNotFound.mux.Lock()
	defer articleNotFound.mux.Unlock()
	for group, m := range articleNotFound.Map {
		for key, exp := range m {
			if !now.Before(exp) {
				delete(m, key)
				articleNotFound.n--
			}
		}
		if len(m) == 0 {
			delete(articleNotFound.Map, group)
		}
	}
}

// negCacheJanitor purges the negative cache every 30 s. Entries used to be
// removed only when looked up again, so the map only grew.
func negCacheJanitor() {
	for range time.Tick(negCacheTTL / 2) {
		purgeNegCache(time.Now())
	}
}

// isValidMessageID checks if the provided message ID is valid according to NNTP standards.
func isValidMessageID(ps *ProxySession, messageID string) (isvalid bool, num int64) {
	// Placeholder for message ID validation logic
	if strings.HasPrefix(messageID, "<") && strings.HasSuffix(messageID, ">") {
		// we dont check for @ in the message ID, as it is not required by RFC 5536
		isvalid = true
		return
	}
	number, err := strconv.ParseInt(messageID, 10, 64)
	if err == nil && number > 0 {
		num = number
		// If it's a number, we consider it valid
		return
	}
	log.Printf(" %s | Invalid message ID: %s", ps.Username, messageID)
	return
}
