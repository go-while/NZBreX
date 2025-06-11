package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	nntpWelcomeMessage = "ready" // 20x 'NNTP Welcome message' for NNTP clients. x will be set by the server: globalAllowPosting true|false
	welcomeCode        = 201     // don't change, will be set by server on boot to 200 or 201 depending on globalAllowPosting
	// Allow posting by default, can be set to false to disable posting (e.g., for read-only mode)
	// this is a global flag. if user/passwd config does not allow posting. user will not be able to post, even if this is true.
	globalAllowPosting = true

	CID = uint64(0) // Global connection ID counter, can be used for session tracking - currently unused

	proxyMutex      = &sync.RWMutex{}                                          // proxyMutex is used to synchronize access to passwdMap, ProxySessions and CountConns
	proxyCron       = time.Now()                                               // reload passwdMap every minute
	passwdMap       = make(map[string]*UserData)                               // passwdMap holds user credentials (k is username, v is UserData)
	ProxySessions   = make(map[string]*ProxySession)                           // ProxySessions map to hold active user sessions (k is username, v is ProxySession)
	CountConns      = make(map[string]int)                                     // CountConns keeps track of active connections per user (k is username, v is count)
	ProxyParent     *SESSION                                                   // ProxyParent is the parent session for the proxy, used to link sessions to the main loop
	CliRxTxCounter  = make(map[string]*Counter_uint64)                         // RxTxCounter is a global counter for received and sent bytes (used for statistics)
	CliRxTxMux      = &sync.RWMutex{}                                          // CliRxTxMux is used to synchronize access to CliRxTxCounter
	statsChan       = make(chan *statsItem, 1000)                              // statsChan is used to send segment items for statistics processing
	articleNotFound = &ArticleNotFound{Map: make(map[string]map[string]*A430)} // Global variable to track articles not found by provider
)

// ArticleNotFound is a map to track articles not found by provider (k is provider group, v is map of message IDs)
type ArticleNotFound struct {
	mux sync.RWMutex                // Mutex to protect access to the map
	Map map[string]map[string]*A430 // Map of provider groups to message IDs not found
}

type A430 struct {
	expires time.Time // Expiration time for the A430 article not found
}
type statsItem struct {
	username string // Username of the client
	rxbytes  uint64 // Received bytes
	txbytes  uint64 // Sent bytes
	clear    error  // Clear is used to indicate that the stats item should be cleared
}

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
	id               uint64            // Unique session ID, can be used for tracking
	mux              sync.RWMutex      // Mutex for session data access
	cmdmux           sync.RWMutex      // Mutex for command handling
	Authed           bool              // Indicates if the user is authenticated
	Username         string            // Username of the authenticated user
	Password         string            // password for the session, can be used for re-authentication
	ExpireAt         int64             // session expiration time (Unix timestamp)
	Conn             net.Conn          // The user's network connection
	Writer           *bufio.Writer     // bufio writer for the client connection to send articles, headers, bodies, list, xover, xhdr, ... (big data)
	tpReader         *textproto.Reader // textproto reader for easier command handling
	tpWriter         *textproto.Writer // textproto writer for easier command handling
	CliTp            *textproto.Conn   // textproto connection for easier command handling
	tmpRXBytes       uint64            // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in last 60 seconds
	RXBytes          uint64            // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in total this session
	tmpTXBytes       uint64            // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in last 60 seconds
	TXBytes          uint64            // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in total this session
	ConnectedAt      time.Time         // Timestamp when the session was created
	LastCmd          time.Time         // Timestamp of the last command received
	Group            string            // current group the user is in (used by GROUP command)
	MsgNum           int64             // current message number in the group (used by STAT, ARTICLE, etc. commands)
	Cron             time.Time         // last run of periodic tasks, e.g., checking session expiration
	selectedProvider *Provider         // The provider selected for this session, used for routing commands
	// Add other session-specific data here, e.g., current group, article pointer, etc.
}

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

// GroupInfo holds information about a newsgroup
type GroupInfo struct {
	Name        string // Group name
	Description string // Group description
	Count       int    // Article count
	Low         int64  // Low water mark (oldest article number)
	High        int64  // High water mark (newest article number)
	Posting     bool   // Posting allowed flag
}

// ArticleOverview holds fields for the XOVER/OVER response
type ArticleOverview struct {
	Number      int64             // Article number
	Subject     string            // Subject header
	From        string            // From header
	Date        string            // Date header
	MessageID   string            // Message-ID header
	References  string            // References header
	Bytes       int               // Size in bytes
	Lines       int               // Lines count
	ExtraFields map[string]string // Additional fields
}

// handleGroupCommand processes the GROUP command by selecting a newsgroup
func (ps *ProxySession) handleGroupCommand(args []string) error {
	if len(args) < 1 {
		ps.tpWriter.PrintfLine("501 Syntax error: GROUP <group>")
		return nil
	}
	groupName := args[0]

	// Find a provider that has this group
	var group *GroupInfo

	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload {
			continue // Skip providers that don't allow downloads
		}

		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue // Try next provider
		}

		id, err := connitem.srvtp.Cmd("GROUP %s", groupName)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue // Try next provider
		}

		connitem.srvtp.StartResponse(id)
		code, msg, err := connitem.srvtp.ReadCodeLine(211)
		connitem.srvtp.EndResponse(id)

		// 211 count low high group_name
		if err == nil && code == 211 {
			parts := strings.Fields(msg)
			if len(parts) >= 4 {
				count, _ := strconv.Atoi(parts[0])
				low, _ := strconv.ParseInt(parts[1], 10, 64)
				high, _ := strconv.ParseInt(parts[2], 10, 64)

				group = &GroupInfo{
					Name:  groupName,
					Count: count,
					Low:   low,
					High:  high,
				}

				ps.selectedProvider = provider
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				break
			}
		}

		provider.ConnPool.ParkConn(0, connitem, "proxy")
	}

	if group == nil {
		ps.selectedProvider = nil // Reset selected provider if no group found
		ps.tpWriter.PrintfLine("411 No such newsgroup")
		return nil
	}

	// Update the session with the selected group info
	ps.Group = group.Name
	ps.MsgNum = group.High // Set to the high water mark as default

	// Return a successful GROUP response: 211 count low high group_name
	ps.tpWriter.PrintfLine("211 %d %d %d %s",
		group.Count, group.Low, group.High, group.Name)

	dlog(always, "%s | Selected group: %s (articles: %d, range: %d-%d)",
		ps.Username, group.Name, group.Count, group.Low, group.High)

	return nil
}

// handleListCommand processes the LIST command
func (ps *ProxySession) handleListCommand(args []string) error {
	var variant string
	if len(args) >= 1 {
		variant = strings.ToUpper(args[0])
	}
	// Find a suitable provider
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload {
			continue
		}
		if ps.selectedProvider != nil && provider != ps.selectedProvider {
			continue // Skip if this provider is not the selected one for this session
		}

		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}

		// Different LIST variants
		var command string
		if variant == "" {
			command = "LIST"
		} else {
			command = fmt.Sprintf("LIST %s", variant)
		}

		id, err := connitem.srvtp.Cmd("%s", command)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err := connitem.srvtp.ReadCodeLine(215)
		if err != nil || code != 215 {
			connitem.srvtp.EndResponse(id)
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Read the dot-delimited response directly into a list of strings
		lines, err := connitem.srvtp.ReadDotLines()
		connitem.srvtp.EndResponse(id)

		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}
		provider.ConnPool.ParkConn(0, connitem, "proxy")

		ps.selectedProvider = provider // Set the selected provider for this session

		// Start our response to the client
		ps.tpWriter.PrintfLine("215 List of newsgroups follows")
		dw := ps.tpWriter.DotWriter()

		// Pass through the lines
		for _, line := range lines {
			fmt.Fprintln(dw, line)
		}

		dw.Close()
		return nil
	}

	// If we couldn't find any provider or all failed
	if ps.selectedProvider == nil {
		ps.tpWriter.PrintfLine("503 No providers available for LIST command")
		return nil
	}

	return nil
}

// handleXOverCommand processes the XOVER/OVER command
func (ps *ProxySession) handleXOverCommand(args []string, isXOVER bool) error {
	// Must have a selected group first
	if ps.Group == "" {
		ps.tpWriter.PrintfLine("412 No newsgroup selected")
		return nil
	}

	var rangeArg string
	if len(args) >= 1 {
		rangeArg = args[0]
	} else {
		// If no range specified, use current article number
		if ps.MsgNum <= 0 {
			ps.tpWriter.PrintfLine("420 No current article selected")
			return nil
		}
		rangeArg = fmt.Sprintf("%d", ps.MsgNum)
	}

	// Process the range argument
	var startNum, endNum int64

	if strings.Contains(rangeArg, "-") {
		parts := strings.Split(rangeArg, "-")
		if len(parts) == 2 {
			var err error
			startNum, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				ps.tpWriter.PrintfLine("501 Invalid article range")
				return nil
			}

			if parts[1] == "" {
				// Format like "1000-" means "1000 to the end"
				endNum = 0 // Will be handled as "to the end" by the server
			} else {
				endNum, err = strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					ps.tpWriter.PrintfLine("501 Invalid article range")
					return nil
				}
			}
		}
	} else {
		var err error
		startNum, err = strconv.ParseInt(rangeArg, 10, 64)
		if err != nil {
			ps.tpWriter.PrintfLine("501 Invalid article number")
			return nil
		}
		endNum = startNum // Just one article
	}

	// Find provider with this group
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload {
			continue
		}
		if ps.selectedProvider != nil && provider != ps.selectedProvider {
			continue // Skip if this provider is not the selected one for this session
		}
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}

		// Select the group first (required before XOVER/OVER)
		id, err := connitem.srvtp.Cmd("GROUP %s", ps.Group)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err := connitem.srvtp.ReadCodeLine(211)
		connitem.srvtp.EndResponse(id)

		if err != nil || code != 211 {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Now run the XOVER/OVER command
		var command string
		if isXOVER {
			command = "XOVER"
		} else {
			command = "OVER"
		}

		if endNum > 0 {
			command = fmt.Sprintf("%s %d-%d", command, startNum, endNum)
		} else if endNum == 0 {
			command = fmt.Sprintf("%s %d-", command, startNum)
		} else {
			command = fmt.Sprintf("%s %d", command, startNum)
		}

		id, err = connitem.srvtp.Cmd("%s", command)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err = connitem.srvtp.ReadCodeLine(224)
		if err != nil || code != 224 {
			connitem.srvtp.EndResponse(id)
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Read the dot-delimited response
		lines, err := connitem.srvtp.ReadDotLines()
		connitem.srvtp.EndResponse(id)

		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Start our response to the client
		ps.tpWriter.PrintfLine("224 Overview information follows")
		dw := ps.tpWriter.DotWriter()

		// Pass through the lines
		for _, line := range lines {
			fmt.Fprintln(dw, line)
		}

		dw.Close()
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		return nil
	}

	// If we couldn't find any provider or all failed
	ps.tpWriter.PrintfLine("503 No providers have the selected group")
	return nil
}

// handleXHdrCommand processes the XHDR/HDR command
func (ps *ProxySession) handleXHdrCommand(args []string, isXHDR bool) error {
	if len(args) < 1 {
		ps.tpWriter.PrintfLine("501 Syntax error: XHDR <header> [range|<message-id>]")
		return nil
	}

	// Must have a selected group first, unless message-id is specified
	if ps.Group == "" && !strings.HasPrefix(args[len(args)-1], "<") {
		ps.tpWriter.PrintfLine("412 No newsgroup selected")
		return nil
	}

	headerField := args[0]

	// Get the message ID or range
	var messageID string
	var rangeSpec string

	if len(args) >= 2 {
		if strings.HasPrefix(args[1], "<") {
			// It's a message ID
			messageID = args[1]
		} else {
			// It's a range spec
			rangeSpec = args[1]
		}
	} else {
		// If no range/msgid specified, use current article number
		if ps.MsgNum <= 0 {
			ps.tpWriter.PrintfLine("420 No current article selected")
			return nil
		}
		rangeSpec = fmt.Sprintf("%d", ps.MsgNum)
	}

	// Find provider with this group
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload {
			continue
		}
		if ps.selectedProvider != nil && provider != ps.selectedProvider {
			continue // Skip if this provider is not the selected one for this session
		}

		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}

		// Select the group first if using an article number range
		if messageID == "" {
			id, err := connitem.srvtp.Cmd("GROUP %s", ps.Group)
			if err != nil {
				provider.ConnPool.CloseConn(connitem, nil)
				continue
			}

			connitem.srvtp.StartResponse(id)
			code, _, err := connitem.srvtp.ReadCodeLine(211)
			connitem.srvtp.EndResponse(id)

			if err != nil || code != 211 {
				provider.ConnPool.CloseConn(connitem, nil)
				continue
			}
		}

		// Now run the XHDR/HDR command
		var command string
		if isXHDR {
			command = "XHDR"
		} else {
			command = "HDR"
		}

		if messageID != "" {
			command = fmt.Sprintf("%s %s %s", command, headerField, messageID)
		} else {
			command = fmt.Sprintf("%s %s %s", command, headerField, rangeSpec)
		}

		id, err := connitem.srvtp.Cmd("%s", command)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err := connitem.srvtp.ReadCodeLine(221)
		if err != nil || code != 221 {
			connitem.srvtp.EndResponse(id)
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Read the dot-delimited response
		lines, err := connitem.srvtp.ReadDotLines()
		connitem.srvtp.EndResponse(id)

		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		// Start our response to the client
		ps.tpWriter.PrintfLine("221 Header follows")
		dw := ps.tpWriter.DotWriter()

		// Pass through the lines
		for _, line := range lines {
			fmt.Fprintln(dw, line)
		}

		dw.Close()
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		return nil
	}

	// If we couldn't find any provider or all failed
	ps.tpWriter.PrintfLine("503 No providers have the selected group or article")
	return nil
}

// handleNextOrLastCommand processes the NEXT/LAST command
func (ps *ProxySession) handleNextOrLastCommand(isNext bool) error {
	// Must have a selected group first
	if ps.Group == "" {
		ps.tpWriter.PrintfLine("412 No newsgroup selected")
		return nil
	}

	// Must have a current article selected
	if ps.MsgNum <= 0 {
		ps.tpWriter.PrintfLine("420 No current article selected")
		return nil
	}

	// Find provider with this group
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload {
			continue
		}
		if ps.selectedProvider != nil && provider != ps.selectedProvider {
			continue // Skip if this provider is not the selected one for this session
		}

		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}

		// Select the group first
		id, err := connitem.srvtp.Cmd("GROUP %s", ps.Group)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err := connitem.srvtp.ReadCodeLine(211)
		connitem.srvtp.EndResponse(id)

		if err != nil {
			if code > 0 {
				provider.ConnPool.ParkConn(0, connitem, "proxy")
			} else {
				provider.ConnPool.CloseConn(connitem, nil)
			}
			continue
		}

		// Set the current article
		id, err = connitem.srvtp.Cmd("STAT %d", ps.MsgNum)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, _, err = connitem.srvtp.ReadCodeLine(223)
		connitem.srvtp.EndResponse(id)

		if err != nil {
			if code > 0 {
				provider.ConnPool.ParkConn(0, connitem, "proxy")
			} else {
				provider.ConnPool.CloseConn(connitem, nil)
			}
			continue
		}

		// Now run the NEXT/LAST command
		var command string
		if isNext {
			command = "NEXT"
		} else {
			command = "LAST"
		}

		id, err = connitem.srvtp.Cmd("%s", command)
		if err != nil {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}

		connitem.srvtp.StartResponse(id)
		code, msg, err := connitem.srvtp.ReadCodeLine(223)
		connitem.srvtp.EndResponse(id)
		if code > 0 {
			provider.ConnPool.ParkConn(0, connitem, "proxy")
		} else {
			provider.ConnPool.CloseConn(connitem, nil)
		}
		if err != nil || code != 223 {
			if code == 421 {
				// No next/previous article in the group
				var which string
				if isNext {
					which = "next"
				} else {
					which = "previous"
				}
				ps.tpWriter.PrintfLine("421 No %s article to retrieve", which)
				return nil
			}
		} else {
			// 223 article_number message_id
			parts := strings.Fields(msg)
			if len(parts) >= 2 {
				newNum, _ := strconv.ParseInt(parts[0], 10, 64)
				messageID := parts[1]

				// Update the current article pointer
				ps.MsgNum = newNum

				// Return successful response
				ps.tpWriter.PrintfLine("223 %d %s", newNum, messageID)
			} else {
				ps.tpWriter.PrintfLine("503 Invalid response from server")
			}
		}
		return nil
	}

	// If we couldn't find any provider or all failed
	ps.tpWriter.PrintfLine("503 No providers have the selected group")
	return nil
}
