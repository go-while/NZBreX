package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	mrand "math/rand"
	"net"
	"net/textproto"
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
	id          uint64            // Unique session ID, can be used for tracking
	mux         sync.RWMutex      // Mutex for session data access
	cmdmux      sync.RWMutex      // Mutex for command handling
	Authed      bool              // Indicates if the user is authenticated
	Username    string            // Username of the authenticated user
	Password    string            // password for the session, can be used for re-authentication
	ExpireAt    int64             // session expiration time (Unix timestamp)
	Conn        net.Conn          // The user's network connection
	Writer      *bufio.Writer     // bufio writer for the client connection to send articles, headers, bodies, list, xover, xhdr, ... (big data)
	tpReader    *textproto.Reader // textproto reader for easier command handling
	tpWriter    *textproto.Writer // textproto writer for easier command handling
	CliTp       *textproto.Conn   // textproto connection for easier command handling
	tmpRXBytes  uint64            // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in last 60 seconds
	RXBytes     uint64            // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in total this session
	tmpTXBytes  uint64            // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in last 60 seconds
	TXBytes     uint64            // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in total this session
	ConnectedAt time.Time         // Timestamp when the session was created
	LastCmd     time.Time         // Timestamp of the last command received
	Group       string            // current group the user is in (used by GROUP command)
	MsgNum      int64             // current message number in the group (used by STAT, ARTICLE, etc. commands)
	Cron        time.Time         // last run of periodic tasks, e.g., checking session expiration
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
