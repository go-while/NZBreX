package main

import (
	"bufio"
	"crypto/rand" // Added for random salt generation
	"crypto/tls"  // Added for TLS support
	"strconv"
	"time"

	"github.com/Tensai75/nzbparser"
	"golang.org/x/crypto/bcrypt" // Added for bcrypt password hashing

	// "encoding/hex" // Uncomment if you want to log/debug hashes as hex strings
	"fmt"
	"io"
	"log"             // Added for random delays in password verification (to mitigate timing attacks)
	mrand "math/rand" // Added for random delays in password verification (to mitigate timing attacks)
	"net"
	"net/textproto" // Added for textproto
	"os"
	"strings"
	"sync"
)

var (
	nntpWelcomeMessage = "ready" // 20x 'NNTP Welcome message' for NNTP clients. x will be set by the server: globalAllowPosting true|false
	welcomeCode        = 201     // don't change, will be set by server on boot to 200 or 201 depending on globalAllowPosting
	// Allow posting by default, can be set to false to disable posting (e.g., for read-only mode)
	// this is a global flag. if user/passwd config does not allow posting. user will not be able to post, even if this is true.
	globalAllowPosting = true

	CID = uint64(0) // Global connection ID counter, can be used for session tracking - currently unused

	proxyMutex    = &sync.RWMutex{}                // proxyMutex is used to synchronize access to passwdMap, ProxySessions and CountConns
	proxyCron     = time.Now()                     // reload passwdMap every minute
	passwdMap     = make(map[string]*UserData)     // passwdMap holds user credentials (k is username, v is UserData)
	ProxySessions = make(map[string]*ProxySession) // ProxySessions map to hold active user sessions (k is username, v is ProxySession)
	CountConns    = make(map[string]int)           // CountConns keeps track of active connections per user (k is username, v is count)
	ProxyParent   *SESSION                         // ProxyParent is the parent session for the proxy, used to link sessions to the main loop
)

// UserData holds user information.
// When loading from .passwd, Password will be the bcrypt hash string.
// When adding a new user via addUserToPasswdFile, Password should be plaintext to be hashed.
type UserData struct {
	Username string
	Password string // For loading: bcrypt hash. For adding new user: plaintext.
	MaxConns int    // Optional: max connections per user, if needed
	ExpireAt int64  // Optional: expiration time for the user, if needed (Unix timestamp)
	Posting  bool   // Optional: indicates if the user is allowed to post articles
}

// ProxySession represents an active user session (currently a placeholder, expand as needed)
type ProxySession struct {
	id       uint64            // Unique session ID, can be used for tracking
	mux      sync.RWMutex      // Mutex for session data access
	Authed   bool              // Indicates if the user is authenticated
	Username string            // Username of the authenticated user
	Password string            // password for the session, can be used for re-authentication
	ExpireAt int64             // session expiration time (Unix timestamp)
	Conn     net.Conn          // The user's network connection
	Writer   *bufio.Writer     // bufio writer for the client connection to send articles, headers, bodies, list, xover, xhdr, ... (big data)
	tpReader *textproto.Reader // textproto reader for easier command handling
	tpWriter *textproto.Writer // textproto writer for easier command handling
	CliTp    *textproto.Conn   // textproto connection for easier command handling
	//tmpRXBytes  uint64            // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in last 60 seconds
	RXBytes     uint64    // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in total this session
	tmpTXBytes  uint64    // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in last 60 seconds
	TXBytes     uint64    // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in total this session
	ConnectedAt time.Time // Timestamp when the session was created
	LastCmd     time.Time // Timestamp of the last command received
	Group       string    // current group the user is in (used by GROUP command)
	MsgNum      int64     // current message number in the group (used by STAT, ARTICLE, etc. commands)
	Cron        time.Time // last run of periodic tasks, e.g., checking session expiration
	// Add other session-specific data here, e.g., current group, article pointer, etc.
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

// handleConnection manages a single NNTP client connection.
func handleConnection(conn net.Conn) {
	//log.Printf("Handling connection from %s", conn.RemoteAddr())

	// Use textproto.Reader and textproto.Writer
	tpReader := textproto.NewReader(bufio.NewReader(conn))
	tpWriter := textproto.NewWriter(bufio.NewWriter(conn))

	var currentUser string // Stores username after successful AUTHINFO USER
	authenticated := false
	now := time.Now() // Get the current time for session initialization
	var ps = &ProxySession{
		Conn:        conn, // Store the connection in the session
		LastCmd:     now,  // Initialize last command time
		ConnectedAt: now,  // Set the connection time
	} // ProxySession to hold user session data

	// Ensure connection is closed and proxy session cleaned up when done
	defer func(s *ProxySession) {
		if ps.Authed && s.Username != "" {
			proxyMutex.Lock()
			if CountConns[s.Username] > 0 {
				CountConns[s.Username]--
				log.Printf("Decremented connection count for user '%s'. Active connections: %d", currentUser, CountConns[currentUser])
			} else {
				log.Printf("Connection count for user '%s' was already 0 or less, not decrementing. This might indicate an issue.", currentUser)
			}
			// ProxySessions is used to track this specific session, remove it here.
			delete(ProxySessions, s.Username)
			proxyMutex.Unlock()
		}
		if ps.Authed {
			dlog(always, "Closed connection for user '%s'", s.Username)
		}
		if ps.CliTp != nil {
			ps.CliTp.Close() // Close the textproto connection
		} else if s.Conn != nil {
			ps.Conn.Close()
		}
		ps.Authed = false            // Clear authentication status
		ps.Username = ""             // Clear username to avoid dangling pointer
		ps.ConnectedAt = time.Time{} // Clear timestamp to avoid dangling pointer
		ps.LastCmd = time.Time{}     // Clear last command time to avoid dangling pointer
		ps = nil                     // Clear session to avoid dangling pointer
	}(ps)

	// Send initial welcome message (RFC 3977: 200 or 201)
	// 200 service available, posting allowed
	// 201 service available, posting prohibited
	time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay to simulate server startup
	tpWriter.PrintfLine("%d %s", welcomeCode, nntpWelcomeMessage)
	// incoming client connection is captured in this for loop until QUIT command or error occurs
	tpWriter.W.Flush() // Ensure the welcome message is sent immediately

forever:
	for {
		// Read commands from the client in a loop
		line, err := tpReader.ReadLine() // Use ReadLine from textproto
		if err != nil {
			if err != io.EOF {
				// Check for common textproto errors, like malformed lines or read errors
				if perr, ok := err.(*textproto.Error); ok {
					dlog(always, "Textproto error from client err='%v'", perr)
					// You might want to send a specific NNTP error code back to the client here
					// For example, if it's a syntax error related to line endings or length.
					// tpWriter.PrintfLine("501 Syntax error or line too long")
				} else {
					//DEBUG log.Printf("Error reading from client %s: %v", conn.RemoteAddr(), err)
				}
			} else {
				//DEBUG log.Printf("Client %s disconnected (EOF).", conn.RemoteAddr())
			}
			return
		}
		if len(line) > 128 { // only command lines are captured here
			// reading a line longer than 128 characters is not allowed by RFC 3977
			// Line is too long, send error response
			tpWriter.PrintfLine("501 Syntax error: cmd line too long")
			return
		}
		line = strings.TrimSpace(line) // TrimSpace is still useful
		parts := strings.Fields(line)
		if len(parts) == 0 {
			// just close on an empty line
			return
		}
		var args []string
		command := strings.ToUpper(parts[0])
		if len(parts) >= 2 {
			args = parts[1:] // Get all parts after the command as arguments
		}

		//dlog(cfg.opt.Bug, "Client %s command: %s args='%v'", conn.RemoteAddr(), line, args)

		ps.LastCmd = time.Now() // Update last command timestamp for unauthenticated users

		if authenticated {
			if err := ps.handleRequest(command, args); err != nil {
				log.Printf("Error handling command '%s' for user '%s': %v", command, ps.Username, err)
				break
			}
			continue forever // Continue to handle further commands after handling the request
		}

		switch command {

		case "CAPABILITIES":
			printCapabilities(tpWriter)

		case "AUTHINFO":
			if authenticated {
				tpWriter.PrintfLine("502 Already authenticated")
				return
			}
			if len(parts) < 2 {
				tpWriter.PrintfLine("501 Syntax error in AUTHINFO command")
				return
			}
			authCmd := strings.ToUpper(parts[1])
			switch authCmd {

			case "USER":

				if len(parts) < 3 {
					tpWriter.PrintfLine("501 Syntax error: AUTHINFO USER <username>")
					return
				}
				currentUser = parts[2]
				// RFC 3977 suggests 381 if user is valid, otherwise 481/502 or proceed and fail at PASS.
				// To avoid user enumeration, some servers always respond 381.
				time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay
				tpWriter.PrintfLine("381 Password required")
				continue forever // Continue to handle further commands after AUTHINFO USER

			case "PASS":

				if currentUser == "" {
					tpWriter.PrintfLine("482 Authentication commands out of sequence (AUTHINFO USER first)")
					return
				}
				if len(parts) < 3 {
					tpWriter.PrintfLine("501 Syntax error: AUTHINFO PASS <password>")
					return
				}
				time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay
				passwordToVerify := parts[2]

				if !verifyPassword(currentUser, passwordToVerify) {
					tpWriter.PrintfLine("481 Authentication failed")
					log.Printf("Failed authentication attempt for user '%s' from %s", currentUser, conn.RemoteAddr())
					return
				}

				proxyMutex.RLock() // Lock before checking and updating CountConns and user data
				reloadCron := time.Since(proxyCron) > time.Minute
				proxyMutex.RUnlock()

				if reloadCron {
					proxyMutex.Lock()
					proxyCron = time.Now()
					proxyMutex.Unlock()
					if err := loadPasswdFile(cfg.opt.ProxyPasswdFile); err != nil {
						// Unlock before sleeping
						time.Sleep(time.Duration(mrand.Intn(1000)) * time.Millisecond) // Random delay
						log.Printf("Failed to reload passwd file: %v", err)
						tpWriter.PrintfLine("481 Authentication failed (passwd file reload error)")
						return
					}
				}

				proxyMutex.RLock()

				userData, userExists := passwdMap[currentUser]
				if !userExists {
					// This case should ideally not be reached if verifyPassword relies on passwdMap
					proxyMutex.RUnlock()
					time.Sleep(time.Duration(mrand.Intn(1000)) * time.Millisecond) // Random delay
					tpWriter.PrintfLine("481 Authentication failed (user data inconsistency)")
					log.Printf("User data not found for '%s' in passwdMap after successful verifyPassword. Potential data inconsistency.", currentUser)
					return
				}

				// Check if account is expired
				if userData.ExpireAt > 0 && time.Now().Unix() > userData.ExpireAt {
					proxyMutex.RUnlock()
					time.Sleep(time.Duration(mrand.Intn(1000)) * time.Millisecond) // Random delay
					tpWriter.PrintfLine("481 Authentication failed (account expired)")
					log.Printf("Authentication failed for user '%s' from %s: account expired (ExpireAt: %d, Current: %d)", currentUser, conn.RemoteAddr(), userData.ExpireAt, time.Now().Unix())
					return
				}
				proxyMutex.RUnlock()

				proxyMutex.Lock()
				// Check connection limit
				if userData.MaxConns > 0 && CountConns[currentUser] >= userData.MaxConns {
					proxyMutex.Unlock()
					time.Sleep(time.Duration(mrand.Intn(1000)) * time.Millisecond) // Random delay
					tpWriter.PrintfLine("452 Too many connections for this user. Please try again later.")
					log.Printf("Connection denied for user '%s' from %s: too many connections (current: %d, max: %d)", currentUser, conn.RemoteAddr(), CountConns[currentUser], userData.MaxConns)
					// Do not set authenticated to true, connection is rejected before full authentication.
					return
				}
				// Connection allowed, increment count and flag authenticate
				CountConns[currentUser]++
				authenticated = true

				// Create a new ProxySession for the authenticated user

				CID++       // Increment global connection ID counter
				ps.id = CID // Assign a unique session ID
				ps.Authed = true
				ps.Username = currentUser
				ps.Password = passwordToVerify     // Store the hashed password in the session so we can check every now and then if password has changed and close the session
				ps.ExpireAt = userData.ExpireAt    // Set session expiration time from user data
				ps.Conn = conn                     // Store the connection in the session
				ps.CliTp = textproto.NewConn(conn) // Create a textproto connection for easier command handling
				ps.Writer = bufio.NewWriter(conn)  // Create a bufio writer for the client connection
				ps.tpReader = tpReader             // Store the textproto reader in the session
				ps.tpWriter = tpWriter             // Store the textproto writer in the session
				ps.Cron = ps.ConnectedAt           // Initialize cron time for periodic tasks

				// Store the session in the global ProxySessions map
				ProxySessions[currentUser] = ps

				tpWriter.PrintfLine("281 Welcome to NZBreX Proxy! Your conns: %d/%d. Exp: '%v'",
					CountConns[currentUser], userData.MaxConns, time.Unix(userData.ExpireAt, 0).Format(time.RFC1123Z))

				log.Printf("User '%s' authenticated from %s. Active connections for user: %d/%d",
					currentUser, conn.RemoteAddr(), CountConns[currentUser], userData.MaxConns)

				proxyMutex.Unlock() // Unlock after updating CountConns and user data

				continue forever // Continue to handle further commands after successful authentication

			default:
				tpWriter.PrintfLine("501 Unknown AUTHINFO subcommand: %s", authCmd)
				return
			}

		case "MODE":
			if len(parts) < 2 {
				tpWriter.PrintfLine("501 Syntax error in MODE command")
				return
			}
			switch strings.ToUpper(parts[1]) {
			case "READER":
				tpWriter.PrintfLine("201 Posting prohibited")

			case "STREAM":
				if !authenticated {
					tpWriter.PrintfLine("480 Authentication required for MODE STREAM")
					return
				}
				tpWriter.PrintfLine("200 Switching to STREAM mode")
			default:
				tpWriter.PrintfLine("501 Unknown mode")
				return
			}
			continue forever // Continue to handle further commands after MODE command

		case "QUIT":

			tpWriter.PrintfLine("205 Closing connection - goodbye.")
			log.Printf("Client %s issued QUIT.", conn.RemoteAddr())
			return

		default:
			if authenticated {
				// Handle other NNTP commands for authenticated users here
				// e.g., GROUP, ARTICLE, LIST, NEXT, LAST, DATE, etc.
				tpWriter.PrintfLine("500 Unknown command: %s (authenticated)", command)
			} else {
				// RFC 3977: "480 Authentication required" for most commands if not authenticated
				tpWriter.PrintfLine("480 Authentication required")
			}
		} // end switch command
	} // end for forever
} // end func handleConnection

var articleNotFound = &ArticleNotFound{Map: make(map[string]map[string]*A430)} // Global variable to track articles not found by provider

// ArticleNotFound is a map to track articles not found by provider (k is provider group, v is map of message IDs)
type ArticleNotFound struct {
	mux sync.RWMutex                // Mutex to protect access to the map
	Map map[string]map[string]*A430 // Map of provider groups to message IDs not found
}

type A430 struct {
	expires time.Time // Expiration time for the A430 article not found
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

// handleRequest processes NNTP commands for an authenticated user session.
func (ps *ProxySession) handleRequest(command string, args []string) error {
	// Placeholder for handling specific NNTP commands in the session
	// This function can be expanded to handle commands like GROUP, ARTICLE, etc.
	// For now, we just log the command received.
	// returning any error (e.g.: via fmt.Errorf) will disconnect the user
	var retry error = nil // retry is used to indicate that the command should be retried
	if !ps.Authed || (ps.Username == "") || (ps.CliTp == nil) || ps.ExpireAt < time.Now().Unix() {
		ps.CliTp.PrintfLine("480 Authentication required for %s command", command)
		return fmt.Errorf("authentication required")
	}
	//dlog(always, "Handling command '%s' for user '%s' in session.", command, ps.Username)
	// Handle these commands for authenticated users
	pass := false
	var item *segmentChanItem // segmentChanItem to hold the message ID or number
	switch command {          //switch command1

	case "STAT", "ARTICLE", "BODY", "HEAD":
		if len(args) == 0 {
			ps.CliTp.PrintfLine("501 Syntax error: %s requires a message ID or number", command)
			return fmt.Errorf("syntax error: %s requires a message ID or number", command)
		}
		isvalid, num := isValidMessageID(ps, args[0])
		if !isvalid && num <= 0 {
			// protocol error, message ID is not valid
			ps.CliTp.PrintfLine("501 Syntax error: command %s requires a valid message ID", command)
			return fmt.Errorf("syntax error: command %s requires a valid message ID", command)
		} else if num > 0 && ps.Group == "" {
			// TODO
			ps.tpWriter.PrintfLine("412 No newsgroup selected to read messageid: %d", num)
			return retry

		} else if isvalid {
			item = &segmentChanItem{
				segment: &nzbparser.NzbSegment{
					Id: args[0],
				},
			}
			pass = true // we have a valid message ID, so we can pass it to a provider
			// TODO: add disk caching here?
		}

	case "CAPABILITIES":
		printCapabilities(ps.tpWriter)
		return nil // No error, capabilities printed
	case "DATE":
		ps.tpWriter.PrintfLine("111 %s", time.Now().Format(time.RFC1123Z))
		return nil // No error, date printed
	case "LIST", "XOVER", "XHDR", "GROUP", "NEXT", "LAST":
		ps.tpWriter.PrintfLine("500 cmd: %s (not implemented)", command)
		return fmt.Errorf("unknown command: %s (not implemented)", command)
	case "QUIT":
		ps.tpWriter.PrintfLine("205 Closing connection - goodbye. uploaded=%d downloaded=%d connected='%v'", ps.RXBytes, ps.TXBytes, time.Since(ps.ConnectedAt))
		log.Printf("Client %s issued QUIT.", ps.Conn.RemoteAddr())
		time.Sleep(time.Millisecond) // Sleep to ensure the message is sent before closing the connection
		return fmt.Errorf("client %s issued QUIT", ps.Conn.RemoteAddr())
	default:
		ps.tpWriter.PrintfLine("502 Unknown command")
		return fmt.Errorf("unknown command: %s", command)
	} // end switch command1

	if !pass {
		ps.tpWriter.PrintfLine("501 Syntax error: command %s requires a valid message ID", command)
		return fmt.Errorf("syntax error: command %s requires a valid message ID", command)
	}
	// Now we have a valid command and item (if applicable), proceed to handle the request
	var response string // response to be sent to the client after loopProvider has completed
	checkedProviderGroups := make(map[string]bool)
loopProvider:
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload ||
			checkedProviderGroups[provider.Group] ||
			IsArticleNotFoundAtProviderGroup(item.segment.Id, provider.Group) {
			response = "430 NO"
			// Skip this provider if it has already been checked or is not available for download
			continue loopProvider
		}
		connitem, err := provider.ConnPool.GetConn() // providerconn / proxyconn
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue loopProvider // Skip this provider if connection fails
		}

		dlog(cfg.opt.BUG, " %s | provider %s: %s got pc='%v'", ps.Username, provider.Name, command, connitem)

		// got connection to a provider
		switch command { // switch command2
		case "ARTICLE", "BODY", "HEAD":
			// Handle commands ARTICLE, BODY, HEAD
			code, msg, rxb, err := CMD(connitem, item, command)
			if rxb > 0 {
				// to calulate total download speed of this provider
				// Update provider's RXBytes
				provider.ConnPool.Counter.Add("TMP_RXbytes", rxb)
				provider.ConnPool.Counter.Add("TOTAL_RXbytes", rxb)
			}
			if err != nil {
				provider.ConnPool.CloseConn(connitem, nil) // Close the connection on error
				dlog(always, "ERROR CMD_ARTICLE for provider %s: code=%d msg='%s' err='%v'", provider.Name, code, msg, err)
				continue loopProvider
			}
			switch code {
			case 220, 221, 222: // Valid response codes for ARTICLE, HEAD, BODY
				provider.ConnPool.ParkConn(0, connitem, "proxy") // Park the connection for reuse
				// send data to client
				txb, err := LinesWriter(ps.Writer, ps.Conn, code, item) // Send data to client
				if txb > 0 {
					// Update bytes sent to client
					ps.tmpTXBytes += txb
					ps.TXBytes += txb
				}
				if err != nil {
					return fmt.Errorf("error writing data command %s to client: %v", command, err)
				}
				response = "0"     // full response already sent, set response to 0 to indicate success
				break loopProvider // Break out of the provider loop after handling the command

			case 423, 430, 451: // messageid not found
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				checkedProviderGroups[provider.Group] = true
				response = fmt.Sprintf("%d %s", code, msg)
				SetArticleNotFoundAtProviderGroup(item.segment.Id, provider.Group) // Set article not found at provider group
				continue loopProvider
			default:
				provider.ConnPool.CloseConn(connitem, nil)
				dlog(always, "ERROR in CMD for provider %s: cmd=%s code=%d msg='%s'", provider.Name, command, code, msg)
				response = fmt.Sprintf("502 Unknown Response: %d %s", code, msg)
				continue loopProvider
			}
		case "STAT":
			// Handle STAT command
			code, msg, err := CMD_STAT(connitem, item)
			if err != nil {
				dlog(always, "ERROR CMD_STAT for provider %s: err='%v'", provider.Name, err)
				provider.ConnPool.CloseConn(connitem, nil) // Close the connection on error
				continue loopProvider
			}
			switch code {
			case 223: // Article found
				provider.ConnPool.ParkConn(0, connitem, "proxy") // Park the connection for reuse
				response = fmt.Sprintf("%d 0 %s", code, item.segment.Id)
				break loopProvider // Break out of the provider loop after handling the command

			case 423, 430, 451: // messageid not found
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				checkedProviderGroups[provider.Group] = true
				response = fmt.Sprintf("%d nf", code)
				SetArticleNotFoundAtProviderGroup(item.segment.Id, provider.Group) // Set article not found at provider group
				continue loopProvider

			default:
				provider.ConnPool.CloseConn(connitem, nil)
				response = fmt.Sprintf("%d %s", code, msg)
				// Handle other error codes
				dlog(always, "ERROR CMD_STAT for provider %s: code=%d msg='%s'", provider.Name, code, msg)
				continue loopProvider // Continue to the next provider
			}
		} // end switch command2
	} // end for loopProvider
	if response != "0" {
		if response != "" {
			ps.tpWriter.PrintfLine("%s", response)
		} else {
			dlog(always, " %s | ERROR response to client empty", ps.Username)
			ps.tpWriter.PrintfLine("500 Unknown error occurred while processing command %s", command)
		}
	}
	return nil // Return nil to indicate the command was handled successfully. an error will disconnect the user
} // end func handleRequest

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
} // end func DataWriter

// StartNNTPServer initializes and starts the NNTP server.
// addr is the listen address (e.g., ":1119").
// passwdFilePath is the path to the .passwd file.
// certFile is the path to the TLS certificate file (optional).
// keyFile is the path to the TLS private key file (optional).
func StartNNTPServer(addr string, passwdFilePath string, certFile string, keyFile string) {
	time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay to simulate server startup
	err := loadPasswdFile(passwdFilePath)
	if err != nil {
		// Decide if server should start if passwd file is missing/corrupt.
		// For this example, it logs a warning and continues (no users will be able to auth).
		dlog(always, "WARNING: Could not find passwd file '%s': %v. Server starting without users.", passwdFilePath, err)
		dlog(always, "To manually add users, use the '-proxyadduser' command or craft a new .passwd file manually")
		dlog(always, "Example -proxyadduser \"HelloWorld|NotAsecurePassword|5|42d\" creates a user with 5 conns and 42 days to expiration")

	} else if len(passwdMap) == 0 {
		dlog(always, "Warning: Passwd file '%s' loaded, but no users found or all entries were invalid.", passwdFilePath)
	}

	var listener net.Listener

	if certFile != "" && keyFile != "" {
		dlog(always, "Attempting to start TLS NNTP server on %s", addr)
		cer, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			log.Fatalf("Failed to load TLS key pair: %v. Falling back to non-TLS or specify valid cert/key.", err)
			// As a fallback, could attempt non-TLS, but for now, we exit if TLS is configured but fails to load.
			// To fallback, you might set a flag and then proceed to the non-TLS listener block.
			return // Or handle fallback more gracefully
		}

		config := &tls.Config{Certificates: []tls.Certificate{cer}}
		listener, err = tls.Listen("tcp", addr, config)
		if err != nil {
			log.Fatalf("Failed to start TLS NNTP server on %s: %v", addr, err)
		}
		dlog(always, "TLS NNTP server listening on %s", addr)
	} else {
		dlog(always, "Starting non-TLS NNTP server on %s", addr)
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("Failed to start non-TLS NNTP server on %s: %v", addr, err)
		}
		dlog(always, "Non-TLS NNTP server listening on %s", addr)
	}

	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			// Check if the error is due to the listener being closed.
			if opError, ok := err.(*net.OpError); ok && opError.Err.Error() == "use of closed network connection" {
				dlog(always, "Listener closed, shutting down accept loop.")
				break // Exit loop if listener is closed
			}
			dlog(always, "Error accepting connection: %v", err)
			continue // Continue to try accepting other connections
		}
		globalmux.RLock() // Lock the global mutex to ensure thread-safe access to ProxyParent
		if ProxyParent != nil {
			go handleConnection(conn) // Handle each client in a new goroutine
		} else {
			log.Printf("No ProxyParent available / not booted ... closing connection from %s", conn.RemoteAddr())
			conn.Close() // If ProxyParent is nil, we cannot handle this connection, so close it
		}
		globalmux.RUnlock() // Unlock the global mutex after checking ProxyParent
	} // end for listener.Accept()
	dlog(always, "NNTP server on %s stopped.", addr)
} // end func StartNNTPServer

// StartProxyServers launches the NNTP server on configured TCP and/or TLS ports.
// It uses the global cfg variable for configuration parameters.
func StartProxyServers(appOpt *CFG) {
	if appOpt == nil {
		dlog(always, "CRITICAL: Application configuration (appOpt) is nil. Cannot start proxy servers.")
		return
	}
	if globalAllowPosting {
		welcomeCode = 200 // If posting is allowed, use 200
	}
	started := false

	if appOpt.ProxyTCP > 0 {
		tcpAddr := fmt.Sprintf(":%d", appOpt.ProxyTCP)
		dlog(always, "Attempting to start non-TLS NNTP proxy on port %d", appOpt.ProxyTCP)
		go StartNNTPServer(tcpAddr, appOpt.ProxyPasswdFile, "", "")
		started = true
	} else {
		dlog(always, "Non-TLS NNTP proxy (ProxyTCP) not configured or port is 0, skipping.")
	}

	if appOpt.ProxyTLS > 0 {
		if appOpt.TLSCertPem == "" || appOpt.TLSPrivKey == "" {
			dlog(always, "TLS NNTP proxy (ProxyTLS) configured for port %d, but TLSCertPem or TLSPrivKey is missing. Skipping TLS proxy.", appOpt.ProxyTLS)
		} else {
			tlsAddr := fmt.Sprintf(":%d", appOpt.ProxyTLS)
			dlog(always, "Attempting to start TLS NNTP proxy (NNTPS) on port %d", appOpt.ProxyTLS)
			go StartNNTPServer(tlsAddr, appOpt.ProxyPasswdFile, appOpt.TLSCertPem, appOpt.TLSPrivKey)
			started = true
		}
	} else {
		dlog(always, "TLS NNTP proxy (ProxyTLS) not configured or port is 0, skipping.")
	}

	if !started {
		dlog(always, "No NNTP proxy servers were started (neither ProxyTCP nor ProxyTLS were configured with valid ports/settings).")
	}
} // end func StartProxyServers

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
	log.Printf("Closed session for user '%s'", ps.Username)
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
