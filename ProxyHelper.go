package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

func LinesWriter(cliwriter *bufio.Writer, conn net.Conn, code int, item *segmentChanItem) (txb uint64, err error) {
	defer func() {
		if cliwriter != nil {
			err := cliwriter.Flush() // Ensure all buffered data is written to the client
			if err != nil {
				log.Printf("Error in DotWriter: %v", err)
			}
		}
	}()
	var lines *[]string // lines to be sent to the client
	switch code {
	case 220:
		lines = &item.article
	case 221:
		lines = &item.head
	case 222: // Valid response codes for ARTICLE, HEAD, BODY
		lines = &item.body
		// These codes indicate successful retrieval of article, header, or body
	}
	// code Num <messageID>
	n, err := io.WriteString(cliwriter, fmt.Sprintf("%d 0 %s", code, item.segment.Id)+CRLF)
	txb += uint64(n)
	if err != nil {
		return txb, fmt.Errorf("error DotWriter WriteString writer @ '%s' err='%v'", conn.RemoteAddr(), err)
	}
	// Write lines to the client connection using our own buffered dot writer
	for _, line := range *lines {
		// dot-stuffing when sending to client
		if strings.HasPrefix(line, ".") {
			line = "." + line // prepend a dot to the line
		}
		n, err := io.WriteString(cliwriter, line+CRLF)
		txb += uint64(n)
		if err != nil {
			return txb, fmt.Errorf("error DotWriter WriteString writer err='%v'", err)
		}

	}
	// final sequence
	n, err = io.WriteString(cliwriter, DOT+CRLF)
	txb += uint64(n)
	if err != nil {
		return txb, fmt.Errorf("error DotWriter WriteString writer err='%v'", err)
	}
	return txb, nil
} // end func LinesWriter

func (ps *ProxySession) printCapabilities() {
	// Respond with server capabilities (RFC 3977 Section 5.3)
	ps.CliTp.PrintfLine("101 Capability list:")
	dw := ps.CliTp.DotWriter()
	fmt.Fprintln(dw, "VERSION 2")          // Indicates RFC 3977 support
	fmt.Fprintln(dw, "READER")             // Indicates MODE READER support
	fmt.Fprintln(dw, "AUTHINFO USER PASS") // Supports AUTHINFO USER and AUTHINFO PASS
	// Add other capabilities like LIST, IHAVE, POST, etc., as implemented
	dw.Close()
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

func (ps *ProxySession) Close() {
	ps.mux.Lock()
	defer ps.mux.Unlock()
	if ps.CliTp != nil {
		ps.CliTp.Close() // Close the textproto connection
		ps.CliTp = nil   // Clear textproto connection to avoid dangling pointer
	}
	if ps.Conn != nil {
		ps.Conn.Close() // Close the network connection
		ps.Conn = nil   // Clear the connection to avoid dangling pointer
	}
	ps.Authed = false       // Mark session as unauthenticated
	ps.Username = ""        // Clear username to avoid dangling pointer
	ps.Password = ""        // Clear username to avoid dangling pointer
	ps.ExpireAt = 0         // Clear expiration time
	username := ps.Username // Store username for logging
	ps.Username = ""        // Clear username to avoid dangling pointer
	ps.Password = ""        // Clear username to avoid dangling pointer
	ps.ExpireAt = 0         // Clear expiration time
	log.Printf("Closed session for user '%s'", username)
}

func (ps *ProxySession) IsExpired() (isExpired bool) {
	ps.mux.RLock()
	isExpired = ps.ExpireAt > 0 && time.Now().Unix() > ps.ExpireAt
	ps.mux.RUnlock()
	if isExpired {
		ps.Close() // Close the session if it has expired
	}
	return
} // end func IsExpired

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

func IsArticleNotFoundAtProviderGroup(messageId string, providerGroup string) bool {
	// Check if the article is not found at the provider group
	articleNotFound.mux.RLock()
	defer articleNotFound.mux.RUnlock()
	if providerGroupMap, exists := articleNotFound.Map[providerGroup]; exists {
		if a430, found := providerGroupMap[messageId]; found {
			if time.Now().Before(a430.expires) {
				log.Printf("cache: a430 isflag messageId '%s' not found at provider group '%s'", messageId, providerGroup)
				return true // messageId flaged as not found and entry not expired
			}
			log.Printf("cache: a430 expired messageId '%s' at provider group '%s'", messageId, providerGroup)
			go ClearArticleNotFoundAtProviderGroup(messageId, providerGroup) // Clear expired article not found entry
		}
	}
	return false // not cached
}

func ClearArticleNotFoundAtProviderGroup(messageId string, providerGroup string) {
	// Clear the article not found at the provider group
	articleNotFound.mux.Lock()
	defer articleNotFound.mux.Unlock()
	if providerGroupMap, exists := articleNotFound.Map[providerGroup]; exists {
		if _, found := providerGroupMap[messageId]; found {
			delete(providerGroupMap, messageId) // Remove the article not found entry
			log.Printf("Cleared article not found for message ID '%s' at provider group '%s'", messageId, providerGroup)
		} else {
			log.Printf("Article '%s' not found at provider group '%s' (nothing to clear)", messageId, providerGroup)
		}
	} else {
		log.Printf("Provider group '%s' does not exist in article not found map", providerGroup)
	}
}

func SetArticleNotFoundAtProviderGroup(messageId string, providerGroup string) {
	// Set the article not found at the provider group
	articleNotFound.mux.Lock()
	defer articleNotFound.mux.Unlock()
	if _, exists := articleNotFound.Map[providerGroup]; !exists {
		articleNotFound.Map[providerGroup] = make(map[string]*A430)
	}
	articleNotFound.Map[providerGroup][messageId] = &A430{
		expires: time.Now().Add(1 * time.Minute), // Set expiration to 1 minute from now
	}
	log.Printf("Set article not found for message ID '%s' at provider group '%s'", messageId, providerGroup)
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
