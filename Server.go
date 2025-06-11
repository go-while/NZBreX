package main

import (
	"bufio" // Added for random salt generation
	// Added for TLS support
	"time"

	"github.com/Tensai75/nzbparser"
	// Added for bcrypt password hashing
	// "encoding/hex" // Uncomment if you want to log/debug hashes as hex strings
	"fmt"
	"io"
	"log"             // Added for random delays in password verification (to mitigate timing attacks)
	mrand "math/rand" // Added for random delays in password verification (to mitigate timing attacks)
	"net"
	"net/textproto" // Added for textproto
	"strings"
)

// handleConnection manages a single NNTP proxy client connection.
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
			statsChan <- &statsItem{
				username: s.Username,
				clear:    fmt.Errorf("1"),
			}
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
			ps.cmdmux.Lock()
			if err := ps.handleRequest(command, args); err != nil {
				log.Printf("Error handling command '%s' for user '%s': %v", command, ps.Username, err)
				ps.cmdmux.Unlock()
				break
			}
			ps.cmdmux.Unlock()
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

				dlog(cfg.opt.Debug, "User '%s' authenticated. Active connections for user: %d/%d", currentUser, CountConns[currentUser], userData.MaxConns)

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
	if time.Since(ps.Cron) > time.Second*15 { // every 15s
		// calulate download speed of this user session
		//txspeedinKB := float64(ps.tmpTXBytes) / 1024 / float64(time.Since(ps.Cron)) // speed in KB
		//rxspeedinKB := float64(ps.tmpRXBytes) / 1024 / float64(time.Since(ps.Cron)) // speed in KB
		//log.Printf(" %s | session DL speed: %.0f KB/s | session UL speed: %.0f KB/s", ps.Username, rxspeedinKB, txspeedinKB)
		if ps.tmpTXBytes > 0 || ps.tmpRXBytes > 0 {
			statsChan <- &statsItem{
				username: ps.Username,
				rxbytes:  ps.tmpRXBytes,
				txbytes:  ps.tmpTXBytes,
			}
			ps.tmpTXBytes = 0
			ps.tmpRXBytes = 0
		}
		ps.Cron = time.Now() // Reset cron time for the next speed calculation

	}
	pass := false
	var item *segmentChanItem // segmentChanItem to hold the message ID or number
	switch command {          //switch command1

	// extract messageId from command
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
			// TODO select newsgroup
			ps.tpWriter.PrintfLine("412 No newsgroup selected to read messageid: %d", num)
			return retry

		} else if isvalid {
			item = &segmentChanItem{
				segment: &nzbparser.NzbSegment{
					Id: args[0],
				},
			}
			ps.MsgNum, ps.Group = 0, "" // Reset message number and group on valid <message@ID>
			pass = true                 // we have a valid message ID, so we can pass it to a provider
			// TODO: add disk caching here?
		} else if num > 0 {
			// newsreader message number
			ps.MsgNum = num // Store the message number in the session
			pass = true     // we have a valid message number, so we can pass it to a provider
			item = &segmentChanItem{
				segment: &nzbparser.NzbSegment{
					Id: args[0],
				},
			}
		}

	case "CAPABILITIES":
		printCapabilities(ps.tpWriter)
		return nil // No error, capabilities printed

	case "DATE":
		ps.tpWriter.PrintfLine("111 %s", time.Now().Format(time.RFC1123Z))
		return nil // No error, date printed

	case "LIST":
		return ps.handleListCommand(args)

	case "XOVER", "OVER":
		return ps.handleXOverCommand(args, command == "XOVER")

	case "XHDR", "HDR":
		return ps.handleXHdrCommand(args, command == "XHDR")

	case "GROUP":
		return ps.handleGroupCommand(args)

	case "NEXT":
		return ps.handleNextOrLastCommand(true)

	case "LAST":
		return ps.handleNextOrLastCommand(false)

	case "QUIT":
		ps.tpWriter.PrintfLine("205 Closing connection - goodbye. uploaded=%d downloaded=%d connected='%v'", ps.RXBytes, ps.TXBytes, time.Since(ps.ConnectedAt))
		log.Printf(" %s | quit", ps.Username)
		return fmt.Errorf("client quit")

	default:
		ps.tpWriter.PrintfLine("502 Unknown command")
		return fmt.Errorf("unknown command: %s", command)
	} // end switch command1

	if !pass {
		ps.tpWriter.PrintfLine("501 Syntax error: command %s requires a valid message ID", command)
		return fmt.Errorf("syntax error: command %s requires a valid message ID", command)
	}
	// Now we have a valid command and messageId (if applicable), proceed to handle the request
	var response string // response to be sent to the client after loopProvider has completed
	checkedProviderGroups := make(map[string]bool)
loopProvider:
	for _, provider := range ProxyParent.providerList {
		switch command {
		case "ARTICLE", "BODY", "HEAD", "STAT":
			if provider.NoDownload || (ps.Group != "" && ps.MsgNum > 0 && !provider.Newsreader) {
				// yes, doing stat on a provider that does not allow downloading articles does not make sense
				response = "430 NODL" // 430 No Download, provider does not allow downloading articles
				continue loopProvider // Skip this provider if it does not allow downloading articles
			}
		}

		if checkedProviderGroups[provider.Group] ||
			IsArticleNotFoundAtProviderGroup(item.segment.Id, provider.Group) {
			response = "430 NOPG" // 430 cached Not Found in ProviderGroup
			// Skip this provider if it has already been checked or is not available for download
			continue loopProvider
		}
		connitem, err := provider.ConnPool.GetConn() // providerconn / proxyconn
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue loopProvider // Skip this provider if connection fails
		}
		if ps.Group != "" && ps.MsgNum > 0 {
			// execute GROUP command on remote if we have a group selected
			id, err := connitem.srvtp.Cmd("GROUP %s", ps.Group)
			if err != nil {
				dlog(always, "ERROR CMD_GROUP for provider %s: %v", provider.Name, err)
				provider.ConnPool.CloseConn(connitem, nil)
				continue loopProvider
			}
			connitem.srvtp.StartResponse(id)
			code, _, err := connitem.srvtp.ReadCodeLine(211)
			connitem.srvtp.EndResponse(id)

			if err != nil {
				dlog(always, "ERROR CMD_GROUP command=%s for provider %s: %v", provider.Name, command, err)
				if code > 0 {
					provider.ConnPool.ParkConn(0, connitem, "proxy")
				} else {
					provider.ConnPool.CloseConn(connitem, nil)
				}
				continue
			}
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
