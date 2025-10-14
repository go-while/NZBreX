package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
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

func printCapabilities(tpWriter *textproto.Writer) {
	// Respond with server capabilities (RFC 3977 Section 5.3)
	tpWriter.PrintfLine("101 Capability list:")
	dw := tpWriter.DotWriter()
	fmt.Fprintln(dw, "VERSION 2")          // Indicates RFC 3977 support
	fmt.Fprintln(dw, "READER")             // Indicates MODE READER support
	fmt.Fprintln(dw, "AUTHINFO USER PASS") // Supports AUTHINFO USER and AUTHINFO PASS
	// Add other capabilities like LIST, IHAVE, POST, etc., as implemented
	dw.Close()
}

// Passwords in the file are expected to be bcrypt hash strings.
// File format: username:bcrypt_hash:maxconns:ExpireAt_unix_timestamp
func loadPasswdFile(filename string) error {
	// Before loading, merge any .new.* files into the main passwd file
	base := filename
	if idx := strings.LastIndex(filename, "/"); idx != -1 {
		base = filename[idx+1:]
	}
	dir := "."
	if idx := strings.LastIndex(filename, "/"); idx != -1 {
		dir = filename[:idx]
	}
	pattern := fmt.Sprintf("%s.new.", base)
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if strings.HasPrefix(name, pattern) {
				newfile := dir + "/" + name
				// Append contents to main passwd file
				func() {
					mainf, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
					if err != nil {
						log.Printf("Could not open %s for appending: %v", filename, err)
						return
					}
					defer mainf.Close()
					f, err := os.Open(newfile)
					if err != nil {
						log.Printf("Could not open %s: %v", newfile, err)
						return
					}
					defer f.Close()
					if _, err := io.Copy(mainf, f); err != nil {
						log.Printf("Failed to import %s into %s: %v", newfile, filename, err)
					} else {
						log.Printf("Imported new user(s) from %s into %s", newfile, filename)
						os.Remove(newfile)
					}
				}()
			}
		}
	}

	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open passwd file %s: %w", filename, err)
	}
	defer file.Close()

	proxyMutex.Lock()
	defer proxyMutex.Unlock()

	// Clear existing map before loading to support reloading
	for k := range passwdMap {
		delete(passwdMap, k)
	}

	scanner := bufio.NewScanner(file)
	lineNumber := 0
	usersLoaded := 0
	newpasswdMap := make(map[string]*UserData) // Temporary map to hold new users
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") { // Skip empty lines and comments
			continue
		}
		parts := strings.SplitN(line, "|", 5) // username|bcrypt_hash|maxconns|ExpireAt|(no)post
		if len(parts) != 5 {
			log.Printf("Skipping malformed line in passwd file (%d) (expected 5 parts, got %d): %s", lineNumber, len(parts), line)
			continue
		}
		// check the splitted line parts
		username := strings.TrimSpace(parts[0])
		bcryptHashFromFile := parts[1]
		maxconnsStr := parts[2]
		ExpireAtStr := parts[3]
		posting := strings.HasPrefix(strings.ToLower(parts[4]), "post")

		maxconns, err := strconv.ParseInt(maxconnsStr, 10, 64)
		if err != nil {
			log.Printf("Skipping line in passwd file (%d) for user '%s' due to invalid maxconns ('%s'): %v", lineNumber, username, maxconnsStr, err)
			continue
		}
		ExpireAt, err := strconv.ParseInt(ExpireAtStr, 10, 64)
		if err != nil {
			log.Printf("Skipping line in passwd file (%d) for user '%s' due to invalid ExpireAt ('%s'): %v", lineNumber, username, ExpireAtStr, err)
			continue
		}
		if ExpireAt < time.Now().Unix() {
			log.Printf("Skipping line in passwd file (%d) for user '%s' due to expired account (ExpireAt: %d, Current: %d)", lineNumber, username, ExpireAt, time.Now().Unix())
			continue
		}

		if len(username) < 10 {
			log.Printf("Skipping line in passwd file (%d) due to short username: %s", lineNumber, line)
			continue
		}
		if len(bcryptHashFromFile) < 60 {
			// Bcrypt hashes are typically 60 characters long, so we check for that.
			// If the hash is empty or too short, we skip this user.
			log.Printf("Skipping user '%s' on line in passwd file (%d) due to empty/short password hash.", username, lineNumber)
			continue
		}

		// check if the username already exists in the map
		if _, exists := newpasswdMap[username]; exists {
			delete(newpasswdMap, username) // Remove existing entry to avoid duplicates
			log.Printf("WARNING: Skipping duplicate user '%s' on line %d in passwd file.", username, lineNumber)
			continue // Skip duplicate usernames
		}
		newpasswdMap[username] = &UserData{
			Username: username,
			Password: bcryptHashFromFile, // Store the bcrypt hash directly
			MaxConns: int(maxconns),
			ExpireAt: ExpireAt,
			Posting:  posting,
		}
		usersLoaded++
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading passwd file %s: %w", filename, err)
	}

	if usersLoaded == 0 {
		log.Printf("Warning: No users loaded from passwd file '%s'.", filename)
	} else {
		passwdMap = newpasswdMap
		log.Printf("Successfully loaded %d users from '%s'.", usersLoaded, filename)
	}
	return nil
}

// verifyPassword checks if the provided plaintext password matches the stored bcrypt hash for the user.
func verifyPassword(user string, plainPassword string) bool {
	proxyMutex.RLock()
	userData, ok := passwdMap[user]
	proxyMutex.RUnlock()

	if !ok {
		return false // User not found
	}
	time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // small delay
	// userData.Password holds the full bcrypt hash string from the .passwd file
	err := bcrypt.CompareHashAndPassword([]byte(userData.Password), []byte(plainPassword))
	return err == nil // If err is nil, the password matches
}

// addUserToPasswdFile adds a new user to the passwd file with a bcrypt hashed password.
// The UserData.Password field should contain the plaintext password for the new user.
func addUserToProxyPasswdFile(userData *UserData, filename string) error {
	if userData.Password == "" {
		return fmt.Errorf("cannot add user '%s': plaintext password is empty", userData.Username)
	}

	// Generate bcrypt hash from the plaintext password
	// bcrypt.DefaultCost is 10. You can increase this for more security (e.g., 12-14),
	// but it will also be slower to hash and verify.
	hashedPasswordBytes, err := bcrypt.GenerateFromPassword([]byte(userData.Password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to generate bcrypt hash for user '%s': %w", userData.Username, err)
	}
	bcrpytHashString := string(hashedPasswordBytes)

	// Format the line to be appended to the file
	// username:bcrypt_hash_string:maxconns:ExpireAt
	posting := "nopost" // Default ACL for new users, can be changed if needed
	if userData.Posting {
		posting = "post" // Set ACL to post if userData.Posting is true
	}
	newUserLine := fmt.Sprintf("%s|%s|%d|%d|%s\n", userData.Username, bcrpytHashString, userData.MaxConns, userData.ExpireAt, posting)

	// Open the file in append mode, create if it doesn't exist
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("failed to open passwd file '%s' for appending: %w", filename, err)
	}
	defer file.Close()

	if _, err := file.WriteString(newUserLine); err != nil {
		return fmt.Errorf("failed to write new user '%s' to passwd file '%s': %w", userData.Username, filename, err)
	}

	log.Printf("Successfully added user '%s' to '%s' with a bcrypt hashed password.", userData.Username, filename)
	// Optionally, reload passwdMap or add the new user directly to the in-memory map
	// For simplicity here, we assume a restart or separate reload mechanism if immediate in-memory update is needed.
	// To update in-memory map immediately:
	/* TODO
	proxyMutex.Lock()
	passwdMap[userData.Username] = &UserData{
		Username: userData.Username,
		Password: bcrpytHashString, // Store the newly generated hash
		MaxConns: userData.MaxConns,
		ExpireAt: userData.ExpireAt,
	}
	proxyMutex.Unlock()
	log.Printf("User '%s' also updated in the in-memory passwdMap.", userData.Username)
	*/
	return nil
}

// generateRandomHexCredentials generates a random username and password, each 10 bytes, output as hex strings.
func generateRandomHexCredentials() (username string, password string, err error) {
	const length = 8 // 8 bytes will give us 16 hex characters, which is a common length for usernames/passwords
	// Use crypto/rand for cryptographically secure random bytes
	// If you want longer usernames/passwords, adjust the length accordingly.
	// For example, 10 bytes would give 20 hex characters.
	buf := make([]byte, length)
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
	ps.Authed = false // Mark session as unauthenticated
	ps.Username = ""  // Clear username to avoid dangling pointer
	ps.Password = ""  // Clear username to avoid dangling pointer
	ps.ExpireAt = 0   // Clear expiration time
	username := ps.Username // Store username for logging
	ps.Username = ""  // Clear username to avoid dangling pointer
	ps.Password = ""  // Clear username to avoid dangling pointer
	ps.ExpireAt = 0   // Clear expiration time
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
