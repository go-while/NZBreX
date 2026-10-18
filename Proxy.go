package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	nntpWelcomeMessage = "ready" // 20x 'NNTP Welcome message' for NNTP clients. x will be set by the server: globalAllowPosting true|false
	welcomeCode        = 201     // don't change, will be set by server on boot to 200 or 201 depending on globalAllowPosting
	// Allow posting by default, can be set to false to disable posting (e.g., for read-only mode)
	// this is a global flag. if user/passwd config does not allow posting. user will not be able to post, even if this is true.
	globalAllowPosting = true

	CID = uint64(0) // Global connection ID counter, guarded by proxyMutex

	proxyMutex      = &sync.RWMutex{}                                              // proxyMutex is used to synchronize access to passwdMap, CountConns and CID
	proxyCron       = time.Now()                                                   // reload passwdMap every minute
	passwdMap       = make(map[string]*UserData)                                   // passwdMap holds user credentials (k is username, v is UserData)
	CountConns      = make(map[string]int)                                         // CountConns keeps track of active connections per user (k is username, v is count)
	ProxyParent     *SESSION                                                       // the proxy's session: its providers and pools. Set, and its providers loaded, before any listener starts
	CliRxTxCounter  = make(map[string]*Counter_uint64)                             // RxTxCounter is a global counter for received and sent bytes (used for statistics)
	CliRxTxMux      = &sync.RWMutex{}                                              // CliRxTxMux is used to synchronize access to CliRxTxCounter
	statsChan       = make(chan *statsItem, 1000)                                  // statsChan is used to send segment items for statistics processing
	articleNotFound = &ArticleNotFound{Map: make(map[string]map[string]time.Time)} // negative cache: not found at a provider group

	proxyListeners   []net.Listener // closed on shutdown
	proxyListenersMu sync.Mutex
)

const (
	proxyMaxReplyBytes = 64 << 20 // LIST/XOVER/XHDR replies are buffered: cap them
	negCacheTTL        = time.Minute
	negCacheMax        = 100000 // entries over all provider groups
)

// ArticleNotFound is the negative cache: provider group → key → expiry. Keys
// are message-IDs, or "group:number" for requests by number (a bare number
// used to poison that number in every group).
type ArticleNotFound struct {
	mux sync.RWMutex
	Map map[string]map[string]time.Time
	n   int // entries in Map
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

// ProxySession is one client connection. Only its own goroutine uses it.
type ProxySession struct {
	id               uint64          // Unique session ID
	Authed           bool            // Indicates if the user is authenticated
	Username         string          // Username of the authenticated user
	ExpireAt         int64           // session expiration time (Unix timestamp)
	Conn             net.Conn        // The user's network connection
	CliTp            *textproto.Conn // writes to the client (commands are read with readCommandLine)
	writeErr         error           // first failed write to the client: the connection is closed
	tmpRXBytes       uint64          // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in last 60 seconds
	RXBytes          uint64          // proxy has RECEIVED this amount of bytes FROM CLIENT via POST/IHAVE/TAKETHIS in total this session
	tmpTXBytes       uint64          // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in last 60 seconds
	TXBytes          uint64          // proxy has SENT this amount of bytes TO CLIENT via ARTICLE/HEAD/BODY in total this session
	ConnectedAt      time.Time       // Timestamp when the session was created
	LastCmd          time.Time       // Timestamp of the last command received
	Group            string          // current group the user is in (used by GROUP command)
	MsgNum           int64           // current article number in the group (0 = invalid)
	Cron             time.Time       // last run of periodic tasks
	selectedProvider *Provider       // The provider selected for this session, used for routing commands
}

// listenNNTP opens one proxy listener. TLS when certFile and keyFile are set.
func listenNNTP(addr, certFile, keyFile string) (net.Listener, error) {
	if certFile != "" && keyFile != "" {
		cer, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("TLS NNTP proxy on %s: load key pair: %w", addr, err)
		}
		ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cer}, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, fmt.Errorf("TLS NNTP proxy on %s: %w", addr, err)
		}
		dlog(always, "TLS NNTP server listening on %s", addr)
		return ln, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("NNTP proxy on %s: %w", addr, err)
	}
	dlog(always, "Non-TLS NNTP server listening on %s", addr)
	return ln, nil
}

// serveNNTP accepts clients until the listener is closed.
func serveNNTP(ln net.Listener) {
	var backoff time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				dlog(always, "NNTP server on %s stopped.", ln.Addr())
				return
			}
			// e.g. EMFILE: back off instead of spinning (it used to retry at once, at 100% CPU)
			backoff = min(max(2*backoff, 5*time.Millisecond), time.Second)
			dlog(always, "Error accepting connection: %v (retry in %v)", err, backoff)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		if n := proxyPreAuthConns.Add(1); n > proxyMaxPreAuth {
			proxyPreAuthConns.Add(-1)
			conn.SetWriteDeadline(time.Now().Add(time.Second))
			io.WriteString(conn, "400 Too many connections, try again later\r\n")
			conn.Close()
			continue
		}
		go handleConnection(conn) // decrements proxyPreAuthConns at login or close
	}
}

// StartProxyServers loads the proxy users and starts the TCP and/or TLS
// listeners. ProxyParent's providers must be loaded already. A listener that
// fails is logged; it is an error only if none starts.
func StartProxyServers(appOpt *CFG) error {
	if appOpt == nil {
		return errors.New("proxy: no configuration")
	}
	if globalAllowPosting {
		welcomeCode = 200 // If posting is allowed, use 200
	}
	if err := loadPasswdFile(appOpt.ProxyPasswdFile); err != nil {
		dlog(always, "WARNING: Could not load passwd file '%s': %v. Server starting without users.", appOpt.ProxyPasswdFile, err)
		dlog(always, "To add users, use the '-proxyadduser' command or craft a new .passwd file manually")
		dlog(always, "Example -proxyadduser \"HelloWorld|NotAsecurePassword|5|42d|nopost\" creates a user with 5 conns and 42 days to expiration")
	} else if proxyUserCount() == 0 {
		dlog(always, "Warning: Passwd file '%s' loaded, but no users found or all entries were invalid.", appOpt.ProxyPasswdFile)
	}
	go negCacheJanitor()

	var errs []error
	start := func(addr, cert, key string) {
		ln, err := listenNNTP(addr, cert, key)
		if err != nil {
			dlog(always, "ERROR %v", err)
			errs = append(errs, err)
			return
		}
		proxyListenersMu.Lock()
		proxyListeners = append(proxyListeners, ln)
		proxyListenersMu.Unlock()
		go serveNNTP(ln)
	}
	if appOpt.ProxyTCP > 0 {
		start(fmt.Sprintf(":%d", appOpt.ProxyTCP), "", "")
	}
	if appOpt.ProxyTLS > 0 {
		if appOpt.TLSCertPem == "" || appOpt.TLSPrivKey == "" {
			err := fmt.Errorf("TLS NNTP proxy on port %d: -tlscrt and -tlskey are needed", appOpt.ProxyTLS)
			dlog(always, "ERROR %v", err)
			errs = append(errs, err)
		} else {
			start(fmt.Sprintf(":%d", appOpt.ProxyTLS), appOpt.TLSCertPem, appOpt.TLSPrivKey)
		}
	}
	proxyListenersMu.Lock()
	started := len(proxyListeners)
	proxyListenersMu.Unlock()
	if started == 0 {
		if len(errs) == 0 {
			return errors.New("no NNTP proxy listener configured")
		}
		return errors.Join(errs...)
	}
	return nil
}

// proxyShutdownOnSignal closes the listeners on SIGINT/SIGTERM and exits.
// Connected clients are cut off with the process.
func proxyShutdownOnSignal() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		dlog(always, "Proxy: got %v, closing listeners", sig)
		proxyListenersMu.Lock()
		for _, ln := range proxyListeners {
			ln.Close()
		}
		proxyListenersMu.Unlock()
		os.Exit(0)
	}()
}

// GroupInfo holds information about a newsgroup
type GroupInfo struct {
	Name  string // Group name
	Count int    // Article count
	Low   int64  // Low water mark (oldest article number)
	High  int64  // High water mark (newest article number)
}

// upstreamCmd sends one command and reads its status line. code 0 means the
// conn is broken (write or read failed): close it, don't park it.
func upstreamCmd(connitem *ConnItem, format string, args ...any) (int, string, error) {
	id, err := connitem.srvtp.Cmd(format, args...)
	if err != nil {
		return 0, "", err
	}
	connitem.srvtp.StartResponse(id)
	defer connitem.srvtp.EndResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(0) // 0: any code, err only for I/O or a malformed line
	if err != nil {
		return 0, "", err
	}
	return code, msg, nil
}

var errReplyTooLarge = errors.New("upstream reply too large")

// readDotLinesLimited reads a dot-terminated block of at most max bytes.
// On any error the block was not read to its end: close the conn.
func readDotLinesLimited(r *textproto.Reader, max int64) ([]string, error) {
	lr := &io.LimitedReader{R: r.DotReader(), N: max + 1}
	br := bufio.NewReader(lr)
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimRight(line, "\r\n"))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if lr.N <= 0 {
		return nil, errReplyTooLarge
	}
	return lines, nil
}

// upstreamBlock sends a command that answers with a dot-terminated block on
// okCode. It returns the code and message, and the lines on okCode. The conn
// is parked or closed here, before the caller writes to the client: a slow
// client must not hold an upstream conn. code 0: try another provider.
func upstreamBlock(provider *Provider, connitem *ConnItem, okCode int, format string, args ...any) (int, string, []string) {
	id, err := connitem.srvtp.Cmd(format, args...)
	if err != nil {
		provider.ConnPool.CloseConn(connitem, nil)
		return 0, "", nil
	}
	connitem.srvtp.StartResponse(id)
	code, msg, err := connitem.srvtp.ReadCodeLine(0)
	if err != nil {
		connitem.srvtp.EndResponse(id)
		provider.ConnPool.CloseConn(connitem, nil)
		return 0, "", nil
	}
	if code != okCode {
		connitem.srvtp.EndResponse(id)
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		return code, msg, nil
	}
	lines, err := readDotLinesLimited(&connitem.srvtp.Reader, proxyMaxReplyBytes)
	connitem.srvtp.EndResponse(id)
	if err != nil {
		dlog(always, "ERROR proxy reading '%s' reply @ '%s': %v", strings.Fields(format)[0], provider.Name, err)
		provider.ConnPool.CloseConn(connitem, nil)
		if errors.Is(err, errReplyTooLarge) {
			return 503, "Reply too large", nil
		}
		return 0, "", nil
	}
	provider.ConnPool.ParkConn(0, connitem, "proxy")
	return code, msg, lines
}

// readerProviders returns the providers to try for newsreader commands:
// the session's selected one if set, else every one that allows it.
func (ps *ProxySession) readerProviders() []*Provider {
	var out []*Provider
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload || !provider.Newsreader {
			continue
		}
		if ps.selectedProvider != nil && provider != ps.selectedProvider {
			continue
		}
		out = append(out, provider)
	}
	return out
}

// selectGroup sends GROUP ps.Group on connitem. ok false: the conn was handled
// (closed, or parked with the client answered). answered: the client got a reply.
func (ps *ProxySession) selectGroup(provider *Provider, connitem *ConnItem) (ok, answered bool) {
	code, msg, _ := upstreamCmd(connitem, "GROUP %s", ps.Group)
	if code == 0 {
		provider.ConnPool.CloseConn(connitem, nil)
		return false, false
	}
	if code != 211 {
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		ps.reply("%d %s", code, msg)
		return false, true
	}
	return true, false
}

// handleGroupCommand processes the GROUP command by selecting a newsgroup
func (ps *ProxySession) handleGroupCommand(args []string) {
	if len(args) != 1 {
		ps.reply("501 Syntax error: GROUP <group>")
		return
	}
	groupName := args[0]
	var group *GroupInfo
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload || !provider.Newsreader {
			continue
		}
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}
		code, msg, _ := upstreamCmd(connitem, "GROUP %s", groupName)
		if code == 0 {
			provider.ConnPool.CloseConn(connitem, nil) // it used to be parked even when dead
			continue
		}
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		parts := strings.Fields(msg) // 211 count low high group_name
		if code != 211 || len(parts) < 4 {
			continue
		}
		count, _ := strconv.Atoi(parts[0])
		low, _ := strconv.ParseInt(parts[1], 10, 64)
		high, _ := strconv.ParseInt(parts[2], 10, 64)
		group = &GroupInfo{Name: groupName, Count: count, Low: low, High: high}
		ps.selectedProvider = provider // Set the selected provider for this session when requesting GROUP
		break
	}
	if group == nil {
		ps.selectedProvider = nil
		ps.reply("411 No such newsgroup")
		return
	}
	ps.Group = group.Name
	// RFC 3977 §6.1.1: the current article is the first one (it was set to High);
	// an empty group has none
	ps.MsgNum = 0
	if group.Count > 0 {
		ps.MsgNum = group.Low
	}
	ps.reply("211 %d %d %d %s", group.Count, group.Low, group.High, group.Name)
	dlog(cfg.opt.Debug, "%s | Selected group: %s (articles: %d, range: %d-%d)", ps.Username, group.Name, group.Count, group.Low, group.High)
}

// handleListCommand processes the LIST command, with all its arguments
// (LIST ACTIVE <wildmat> used to fetch the full list).
func (ps *ProxySession) handleListCommand(args []string) {
	command := "LIST"
	if len(args) >= 1 {
		command += " " + strings.ToUpper(args[0])
		if len(args) > 1 {
			command += " " + strings.Join(args[1:], " ")
		}
	}
	for _, provider := range ps.readerProviders() {
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}
		code, msg, lines := upstreamBlock(provider, connitem, 215, "%s", command)
		if code == 0 {
			continue
		}
		if ps.selectedProvider == nil {
			ps.selectedProvider = provider
		}
		if code == 215 {
			ps.writeDotBlock("215 "+msg, lines)
		} else {
			ps.reply("%d %s", code, msg)
		}
		return
	}
	ps.selectedProvider = nil
	ps.reply("503 No providers available for LIST command")
}

// handleXOverCommand processes the XOVER/OVER command
func (ps *ProxySession) handleXOverCommand(args []string, isXOVER bool) {
	if ps.Group == "" {
		ps.reply("412 No newsgroup selected")
		return
	}
	var rangeArg string
	switch len(args) {
	case 0:
		if ps.MsgNum <= 0 {
			ps.reply("420 Current article number is invalid")
			return
		}
		rangeArg = strconv.FormatInt(ps.MsgNum, 10)
	case 1:
		rangeArg = args[0]
	default:
		ps.reply("501 Syntax error: OVER [range]")
		return
	}
	// range: n, n-, or n-m. "1-2-3" used to be sent upstream as "0-".
	parts := strings.Split(rangeArg, "-")
	if len(parts) > 2 {
		ps.reply("501 Invalid article range")
		return
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		ps.reply("501 Invalid article range")
		return
	}
	if len(parts) == 2 && parts[1] != "" {
		if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
			ps.reply("501 Invalid article range")
			return
		}
	}
	command := "OVER"
	if isXOVER {
		command = "XOVER"
	}
	for _, provider := range ps.readerProviders() {
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}
		if ok, answered := ps.selectGroup(provider, connitem); !ok {
			if answered {
				return
			}
			continue
		}
		code, msg, lines := upstreamBlock(provider, connitem, 224, "%s %s", command, rangeArg)
		if code == 0 {
			continue
		}
		if code == 224 {
			ps.writeDotBlock("224 "+msg, lines)
		} else {
			ps.reply("%d %s", code, msg)
		}
		return
	}
	ps.selectedProvider = nil
	ps.reply("503 No providers have the selected group")
}

// handleXHdrCommand processes the XHDR/HDR command
func (ps *ProxySession) handleXHdrCommand(args []string, isXHDR bool) {
	if len(args) < 1 || len(args) > 2 {
		ps.reply("501 Syntax error: HDR <header> [range|<message-id>]")
		return
	}
	var messageID, rangeSpec string
	if len(args) == 2 {
		if strings.HasPrefix(args[1], "<") {
			messageID = args[1]
		} else {
			rangeSpec = args[1]
		}
	}
	if messageID == "" && ps.Group == "" {
		ps.reply("412 No newsgroup selected")
		return
	}
	if messageID == "" && rangeSpec == "" {
		if ps.MsgNum <= 0 {
			ps.reply("420 Current article number is invalid")
			return
		}
		rangeSpec = strconv.FormatInt(ps.MsgNum, 10)
	}
	command := "HDR"
	if isXHDR {
		command = "XHDR"
	}
	target := rangeSpec
	if messageID != "" {
		target = messageID
	}
	okCode := 225 // HDR (RFC 3977 §8.5)
	if isXHDR {
		okCode = 221 // XHDR (RFC 2980)
	}
	for _, provider := range ps.readerProviders() {
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}
		if messageID == "" {
			if ok, answered := ps.selectGroup(provider, connitem); !ok {
				if answered {
					return
				}
				continue
			}
		}
		code, msg, lines := upstreamBlock(provider, connitem, okCode, "%s %s %s", command, args[0], target)
		if code == 0 {
			continue
		}
		if code == okCode {
			ps.writeDotBlock(fmt.Sprintf("%d %s", code, msg), lines)
		} else {
			ps.reply("%d %s", code, msg)
		}
		return
	}
	ps.selectedProvider = nil
	ps.reply("503 No providers have the selected group or article")
}

// handleNextOrLastCommand processes the NEXT/LAST command. Any upstream reply
// is forwarded (it used to answer nothing for 422 or an I/O error, and "421 No
// previous" for LAST); a broken conn tries the next provider.
func (ps *ProxySession) handleNextOrLastCommand(isNext bool) {
	if ps.Group == "" {
		ps.reply("412 No newsgroup selected")
		return
	}
	if ps.MsgNum <= 0 {
		ps.reply("420 Current article number is invalid")
		return
	}
	command := "LAST"
	if isNext {
		command = "NEXT"
	}
	for _, provider := range ps.readerProviders() {
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue
		}
		if ok, answered := ps.selectGroup(provider, connitem); !ok {
			if answered {
				return
			}
			continue
		}
		// set the upstream's current article to ours, then move it
		code, msg, _ := upstreamCmd(connitem, "STAT %d", ps.MsgNum)
		if code == 0 {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}
		if code != 223 {
			provider.ConnPool.ParkConn(0, connitem, "proxy")
			ps.reply("%d %s", code, msg)
			return
		}
		code, msg, _ = upstreamCmd(connitem, "%s", command)
		if code == 0 {
			provider.ConnPool.CloseConn(connitem, nil)
			continue
		}
		provider.ConnPool.ParkConn(0, connitem, "proxy")
		if code == 223 {
			parts := strings.Fields(msg) // 223 n <message-id>
			if len(parts) < 2 {
				ps.reply("503 Invalid response from server")
				return
			}
			if n, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				ps.MsgNum = n
			}
			ps.reply("223 %s %s", parts[0], parts[1])
			return
		}
		ps.reply("%d %s", code, msg) // 421 no next, 422 no previous, ...
		return
	}
	ps.selectedProvider = nil
	ps.reply("503 No providers have the selected group")
}
