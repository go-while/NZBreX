package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/textproto"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/go-while/go-loggedrwmutex"
)

const (
	proxyMaxLineLen = 512 // a command line, CRLF included (RFC 3977 §3.1)
	proxyMaxPreAuth = 64  // connections that have not logged in yet
)

// vars, so tests can shorten them
var (
	proxyLoginTimeout = 60 * time.Second // time after connecting to log in
	proxyIdleTimeout  = 5 * time.Minute  // idle time between commands after login (RFC 3977 §3.1: at least 3 min)
	proxyWriteTimeout = 5 * time.Minute  // to write one response to the client
)

var (
	proxyPreAuthConns atomic.Int64 // connections accepted but not logged in
	errLineTooLong    = errors.New("command line too long")
	errClientQuit     = errors.New("client quit")
)

// readCommandLine reads one line of at most proxyMaxLineLen bytes (CRLF
// included). textproto's ReadLine has no limit: the old "> 128" check ran after
// the whole line was in memory. A longer line is read to its end and dropped.
func readCommandLine(br *bufio.Reader) (string, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > proxyMaxLineLen {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return "", err
		}
		break
	}
	if tooLong {
		return "", errLineTooLong
	}
	return strings.TrimRight(string(buf), "\r\n"), nil
}

// reply writes one status line to the client. A failed write is remembered in
// ps.writeErr; the command loop then closes the connection.
func (ps *ProxySession) reply(format string, args ...any) {
	if ps.writeErr != nil {
		return
	}
	ps.writeErr = ps.CliTp.PrintfLine(format, args...)
}

// writeDotBlock writes a status line and a dot-terminated block of lines.
func (ps *ProxySession) writeDotBlock(status string, lines []string) {
	ps.reply("%s", status)
	if ps.writeErr != nil {
		return
	}
	dw := ps.CliTp.DotWriter()
	for _, line := range lines {
		if _, err := io.WriteString(dw, line+"\n"); err != nil {
			ps.writeErr = err
			dw.Close()
			return
		}
	}
	if err := dw.Close(); err != nil {
		ps.writeErr = err
	}
}

// handleConnection manages a single NNTP proxy client connection. The
// connection was counted in proxyPreAuthConns by the accept loop.
func handleConnection(conn net.Conn) {
	// Safety net: one bad client must never take down the whole process.
	// This only covers this goroutine; it is not a substitute for fixing the panic.
	// Registered first, so it runs after the cleanup defer below.
	defer func() {
		if r := recover(); r != nil {
			dlog(always, "PANIC in proxy handleConnection remote='%s': %v\n%s", conn.RemoteAddr(), r, debug.Stack())
			conn.Close()
		}
	}()
	preAuth := true
	defer func() {
		if preAuth {
			proxyPreAuthConns.Add(-1)
		}
	}()

	// commands are read through br (bounded lines); cliTp is used for writing only
	br := bufio.NewReaderSize(conn, 1024)
	cliTp := textproto.NewConn(conn)
	var currentUser string // Stores username after AUTHINFO USER
	now := time.Now()
	ps := &ProxySession{
		Conn:        conn,
		CliTp:       cliTp, // set before any command is handled: CAPABILITIES works before auth
		LastCmd:     now,
		ConnectedAt: now,
	}

	defer func() {
		if ps.Authed && ps.Username != "" {
			proxyMutex.Lock()
			if CountConns[ps.Username] > 0 {
				CountConns[ps.Username]--
				log.Printf("Decremented connection count for user '%s'. Active connections: %d", ps.Username, CountConns[ps.Username])
			} else {
				log.Printf("Connection count for user '%s' was already 0 or less, not decrementing. This might indicate an issue.", ps.Username)
			}
			proxyMutex.Unlock()
			statsChan <- &statsItem{username: ps.Username, clear: fmt.Errorf("1")} // outside the lock: may block
			dlog(always, "Closed connection for user '%s'", ps.Username)
		}
		cliTp.Close()
	}()

	// Send initial welcome message (RFC 3977: 200 or 201)
	time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay to simulate server startup
	conn.SetWriteDeadline(time.Now().Add(proxyWriteTimeout))
	ps.reply("%d %s", welcomeCode, nntpWelcomeMessage)

	for ps.writeErr == nil {
		// read deadline: the login window before login, then the idle timeout (not past expiry)
		var deadline time.Time
		if ps.Authed {
			deadline = time.Now().Add(proxyIdleTimeout)
			if exp := time.Unix(ps.ExpireAt, 0); ps.ExpireAt > 0 && exp.Before(deadline) {
				deadline = exp.Add(time.Second) // the next command then gets 480
			}
		} else {
			deadline = ps.ConnectedAt.Add(proxyLoginTimeout)
		}
		conn.SetReadDeadline(deadline)
		line, err := readCommandLine(br)
		conn.SetWriteDeadline(time.Now().Add(proxyWriteTimeout))
		if errors.Is(err, errLineTooLong) {
			ps.reply("501 Command line too long (max %d bytes)", proxyMaxLineLen)
			continue
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				// RFC 3977 §3.1: a server that closes the connection sends 400 first
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if ps.Authed {
					ps.reply("400 Idle timeout, closing connection")
				} else {
					ps.reply("400 Login timeout, closing connection")
				}
			} else if err != io.EOF {
				dlog(cfg.opt.Debug, "proxy: read error from client %s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			ps.reply("500 Empty command")
			continue
		}
		command := strings.ToUpper(parts[0])
		args := parts[1:]
		ps.LastCmd = time.Now()

		if ps.Authed {
			if err := ps.handleRequest(command, args); err != nil {
				if !errors.Is(err, errClientQuit) {
					log.Printf("Closing connection for user '%s': %v", ps.Username, err)
				}
				return
			}
			continue
		}

		switch command {
		case "CAPABILITIES":
			ps.printCapabilities()

		case "AUTHINFO":
			if len(args) < 1 {
				ps.reply("501 Syntax error: AUTHINFO USER|PASS <value>")
				continue
			}
			switch strings.ToUpper(args[0]) {
			case "USER":
				if len(args) != 2 {
					ps.reply("501 Syntax error: AUTHINFO USER <username>")
					continue
				}
				currentUser = args[1]
				// always 381, so usernames can't be probed
				time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay
				ps.reply("381 Password required")

			case "PASS":
				if currentUser == "" {
					ps.reply("482 Authentication commands out of sequence (AUTHINFO USER first)")
					continue
				}
				if len(args) != 2 {
					// passwords have no spaces (RFC 4643): "pw junk" used to log in as "pw"
					ps.reply("501 Syntax error: AUTHINFO PASS <password>")
					continue
				}
				if !ps.login(currentUser, args[1]) {
					return // failed logins disconnect: each guess costs a new connection
				}
				preAuth = false
				proxyPreAuthConns.Add(-1)

			default:
				ps.reply("501 Unknown AUTHINFO subcommand: %s", args[0])
			}

		case "MODE":
			if len(args) == 1 && strings.ToUpper(args[0]) == "READER" {
				ps.reply("201 Posting prohibited")
			} else {
				ps.reply("501 Syntax error in MODE command")
			}

		case "QUIT":
			ps.reply("205 Closing connection - goodbye.")
			return

		default:
			ps.reply("480 Authentication required")
		}
	}
}

// login checks the password and the user's limits and answers the client.
// It reports whether the user is now logged in.
func (ps *ProxySession) login(user, password string) bool {
	conn := ps.Conn
	time.Sleep(time.Duration(mrand.Intn(128)) * time.Millisecond) // Random delay
	if !verifyPassword(user, password) {
		ps.reply("481 Authentication failed")
		log.Printf("Failed authentication attempt for user '%s' from %s", user, conn.RemoteAddr())
		return false
	}

	proxyMutex.Lock()
	reload := time.Since(proxyCron) > time.Minute
	if reload {
		proxyCron = time.Now()
	}
	proxyMutex.Unlock()
	if reload {
		// a failed reload keeps the users loaded before (see loadPasswdFile)
		if err := loadPasswdFile(cfg.opt.ProxyPasswdFile); err != nil {
			log.Printf("Failed to reload passwd file: %v", err)
		}
	}

	proxyMutex.Lock()
	defer proxyMutex.Unlock()
	userData, ok := passwdMap[user]
	if !ok {
		// removed by the reload just now
		ps.reply("481 Authentication failed")
		return false
	}
	if userData.ExpireAt > 0 && time.Now().Unix() > userData.ExpireAt {
		ps.reply("481 Authentication failed (account expired)")
		log.Printf("Authentication failed for user '%s' from %s: account expired (ExpireAt: %d, Current: %d)", user, conn.RemoteAddr(), userData.ExpireAt, time.Now().Unix())
		return false
	}
	if userData.MaxConns > 0 && CountConns[user] >= userData.MaxConns {
		ps.reply("502 Too many connections for this user")
		log.Printf("Connection denied for user '%s' from %s: too many connections (current: %d, max: %d)", user, conn.RemoteAddr(), CountConns[user], userData.MaxConns)
		return false
	}
	CountConns[user]++
	CID++
	ps.id = CID
	ps.Authed = true
	ps.Username = user
	ps.ExpireAt = userData.ExpireAt
	ps.Cron = ps.ConnectedAt
	ps.reply("281 Welcome to NZBreX Proxy! Your conns: %d/%d. Exp: '%v'",
		CountConns[user], userData.MaxConns, time.Unix(userData.ExpireAt, 0).Format(time.RFC1123Z))
	dlog(cfg.opt.Debug, "User '%s' authenticated. Active connections for user: %d/%d", user, CountConns[user], userData.MaxConns)
	return true
}

// handleRequest processes one command of a logged-in user. A returned error
// closes the connection: QUIT, an expired login, or a failed write to the
// client. Everything else (syntax errors, unknown commands, failed lookups) is
// answered and the connection stays open (RFC 3977 §3.2.1).
func (ps *ProxySession) handleRequest(command string, args []string) error {
	if !ps.Authed || ps.Username == "" || ps.ExpireAt < time.Now().Unix() {
		ps.reply("480 Authentication required for %s command", command)
		return fmt.Errorf("login expired")
	}
	if time.Since(ps.Cron) > time.Second*15 { // every 15s
		if ps.tmpTXBytes > 0 || ps.tmpRXBytes > 0 {
			statsChan <- &statsItem{
				username: ps.Username,
				rxbytes:  ps.tmpRXBytes,
				txbytes:  ps.tmpTXBytes,
			}
			ps.tmpTXBytes = 0
			ps.tmpRXBytes = 0
		}
		ps.Cron = time.Now()
	}

	switch command {
	case "STAT", "ARTICLE", "BODY", "HEAD":
		ps.handleArticleCommand(command, args)
	case "CAPABILITIES":
		ps.printCapabilities()
	case "DATE":
		ps.reply("111 %s", time.Now().UTC().Format("20060102150405")) // RFC 3977 §7.1: yyyymmddhhmmss
	case "LIST":
		ps.handleListCommand(args)
	case "XOVER", "OVER":
		ps.handleXOverCommand(args, command == "XOVER")
	case "XHDR", "HDR":
		ps.handleXHdrCommand(args, command == "XHDR")
	case "GROUP":
		ps.handleGroupCommand(args)
	case "NEXT":
		ps.handleNextOrLastCommand(true)
	case "LAST":
		ps.handleNextOrLastCommand(false)
	case "MODE":
		// RFC 4643 §2.2: not after login, but many newsreaders send it anyway
		if len(args) == 1 && strings.ToUpper(args[0]) == "READER" {
			ps.reply("201 Posting prohibited")
		} else {
			ps.reply("501 Syntax error in MODE command")
		}
	case "QUIT":
		ps.reply("205 Closing connection - goodbye. uploaded=%d downloaded=%d connected='%v'", ps.RXBytes, ps.TXBytes, time.Since(ps.ConnectedAt))
		log.Printf(" %s | quit", ps.Username)
		return errClientQuit
	default:
		ps.reply("500 Unknown command")
	}
	if ps.writeErr != nil {
		return fmt.Errorf("writing to client: %w", ps.writeErr)
	}
	return nil
}

// handleArticleCommand relays ARTICLE/HEAD/BODY/STAT by message-ID, by number
// in the selected group, or (no argument) for the current article.
func (ps *ProxySession) handleArticleCommand(command string, args []string) {
	var id string     // what is sent upstream: <message-id> or a number
	byNumber := false // request by article number in ps.Group
	var num int64
	if len(args) == 0 {
		// RFC 3977 §6.2: no argument means the current article
		switch {
		case ps.Group == "":
			ps.reply("412 No newsgroup selected")
			return
		case ps.MsgNum <= 0:
			ps.reply("420 Current article number is invalid")
			return
		}
		byNumber, num = true, ps.MsgNum
	} else {
		isvalid, n := isValidMessageID(ps, args[0])
		switch {
		case isvalid:
			id = args[0]
		case n > 0:
			if ps.Group == "" {
				ps.reply("412 No newsgroup selected")
				return
			}
			byNumber, num = true, n
		default:
			ps.reply("501 Syntax error: %s requires a message-ID or an article number", command)
			return
		}
	}
	cacheKey := id
	if byNumber {
		id = strconv.FormatInt(num, 10)
		cacheKey = ps.Group + ":" + id // a number only means something in its group
	}
	item := &segmentChanItem{
		segment: &nzbparser.NzbSegment{Id: id},
		mux:     &loggedrwmutex.LoggedSyncRWMutex{Name: "PROXYITEM"}, // readDotLines locks item.mux
	}

	// statusLine builds "code number message-id" for the client: by number from
	// the upstream reply (the number used to be hardcoded 0, with the number in
	// the message-ID slot), by message-ID with number 0 (RFC 3977 §6.2.1.2).
	statusLine := func(code int, msg string, lines []string) string {
		if byNumber {
			if f := strings.Fields(msg); len(f) >= 2 && strings.HasPrefix(f[1], "<") {
				return fmt.Sprintf("%d %s %s", code, f[0], f[1])
			}
			// the upstream left the message-ID out: take it from the headers if we have them
			for _, line := range lines {
				if line == "" {
					break // end of headers
				}
				if len(line) > 11 && strings.EqualFold(line[:11], "Message-ID:") {
					return fmt.Sprintf("%d %s %s", code, id, strings.TrimSpace(line[11:]))
				}
			}
			return fmt.Sprintf("%d %s <0>", code, id)
		}
		return fmt.Sprintf("%d 0 %s", code, id)
	}

	response := ""
	notFound := "430 No such article"
	if byNumber {
		notFound = "423 No article with that number" // RFC 3977 §6.2.1.3
	}
	checkedProviderGroups := make(map[string]bool)
loopProvider:
	for _, provider := range ProxyParent.providerList {
		if provider.NoDownload || (byNumber && !provider.Newsreader) {
			response = notFound
			continue loopProvider
		}
		if checkedProviderGroups[provider.Group] || IsArticleNotFoundAtProviderGroup(cacheKey, provider.Group) {
			response = notFound
			continue loopProvider
		}
		connitem, err := provider.ConnPool.GetConn()
		if err != nil {
			dlog(always, "ERROR GetConn for provider %s: %v", provider.Name, err)
			continue loopProvider
		}
		if byNumber {
			code, msg, err := upstreamCmd(connitem, "GROUP %s", ps.Group)
			if code == 0 {
				dlog(always, "ERROR proxy GROUP @ '%s': %v", provider.Name, err)
				provider.ConnPool.CloseConn(connitem, nil)
				continue loopProvider
			}
			if code != 211 {
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				response = fmt.Sprintf("%d %s", code, msg)
				continue loopProvider
			}
		}

		switch command {
		case "ARTICLE", "BODY", "HEAD":
			code, msg, rxb, err := CMD(connitem, item, command)
			if rxb > 0 {
				provider.ConnPool.Counter.Add("TMP_RXbytes", rxb)
				provider.ConnPool.Counter.Add("TOTAL_RXbytes", rxb)
			}
			// CMD also returns an error for status codes it doesn't know (423, 412, ...):
			// the conn is fine then. It is broken only if nothing was read (code 0)
			// or reading the article itself failed (220-222).
			if err != nil && (code == 0 || code == 220 || code == 221 || code == 222) {
				provider.ConnPool.CloseConn(connitem, nil)
				dlog(always, "ERROR CMD %s for provider %s: code=%d msg='%s' err='%v'", command, provider.Name, code, msg, err)
				continue loopProvider
			}
			switch code {
			case 220, 221, 222:
				provider.ConnPool.ParkConn(0, connitem, "proxy") // before writing: a slow client must not hold the upstream conn
				lines := item.article
				if code == 221 {
					lines = item.head
				} else if code == 222 {
					lines = item.body
				}
				hdrs := lines
				if code == 222 {
					hdrs = nil // a body has no headers to take the message-ID from
				}
				txb, err := LinesWriter(ps.CliTp.W, statusLine(code, msg, hdrs), lines)
				ps.tmpTXBytes += txb
				ps.TXBytes += txb
				if err != nil {
					ps.writeErr = err
					return
				}
				if byNumber {
					ps.MsgNum = num // the pointer moves only on success
				}
				return
			case 99932:
				// broken or bad-CRC yEnc at this provider (with -crc32): try the others.
				// The article was read in full, the conn is fine.
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				response = "430 Article damaged at the provider (yEnc check failed)"
				checkedProviderGroups[provider.Group] = true
				continue loopProvider
			case 423, 430, 451:
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				checkedProviderGroups[provider.Group] = true
				response = fmt.Sprintf("%d %s", code, msg)
				SetArticleNotFoundAtProviderGroup(cacheKey, provider.Group)
				continue loopProvider
			default:
				provider.ConnPool.ParkConn(0, connitem, "proxy")
				dlog(cfg.opt.Debug, "proxy: %s for provider %s: code=%d msg='%s'", command, provider.Name, code, msg)
				response = fmt.Sprintf("%d %s", code, msg) // e.g. 423, 412: forward it
				continue loopProvider
			}

		case "STAT":
			code, msg, err := upstreamCmd(connitem, "STAT %s", id)
			if code == 0 {
				dlog(always, "ERROR STAT for provider %s: err='%v'", provider.Name, err)
				provider.ConnPool.CloseConn(connitem, nil)
				continue loopProvider
			}
			provider.ConnPool.ParkConn(0, connitem, "proxy")
			switch code {
			case 223:
				if byNumber {
					ps.MsgNum = num
				}
				ps.reply("%s", statusLine(code, msg, nil))
				return
			case 423, 430, 451:
				checkedProviderGroups[provider.Group] = true
				response = fmt.Sprintf("%d %s", code, msg)
				SetArticleNotFoundAtProviderGroup(cacheKey, provider.Group)
				continue loopProvider
			default:
				dlog(always, "ERROR STAT for provider %s: code=%d msg='%s'", provider.Name, code, msg)
				response = fmt.Sprintf("%d %s", code, msg)
				continue loopProvider
			}
		}
	}
	if response == "" {
		response = "503 No provider available"
	}
	ps.reply("%s", response)
}
