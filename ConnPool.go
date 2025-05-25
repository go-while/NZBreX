package main

/*
 * there is not much error handling in this pool implementation!
 * simply: get a conn, park a conn, close a conn
 * when executing commands on a conn: always check for errors!
 * on errors: close conn, get another one and repeat to success!
 */
import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"io"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultConnExpireSeconds int64 = 20 // if conn was parked more than N seconds we close it and get a new one...
)

var (
	noDeadLine time.Time // used as zero value!
	readDeadConn = time.Unix(1, 0) // no syscall needed
)

// holds an active connection to share around
type ConnItem struct {
	conn    net.Conn
	srvtp   *textproto.Conn
	writer  *bufio.Writer
	expires int64 // will be set when parked. default 20s is fine on 99% of providers. some may close early as 30s.
}

// attaches to *Provider.Conns
type ProviderConns struct {
	openConns int // counter
	mux       sync.RWMutex
	pool      chan *ConnItem // idle/parked conns are in here
	//wait       []chan *ConnItem
	provider   *Provider
	rserver    string // "host:port"
	wants_auth bool
}

func NewConnPool(provider *Provider) {
	provider.mux.Lock()         // NewConnPool mutex #d028
	defer provider.mux.Unlock() // NewConnPool mutex #d028

	if provider.Conns != nil {
		return
	}

	if provider.Host == "" {
		log.Printf("ERROR connect '%s' provider.Host empty!", provider.Name)
		return
	}

	switch provider.TCPMode {
	case "tcp4":
		// pass
	case "tcp6":
		// pass
	case "tcp":
		// pass
	default:
		provider.TCPMode = "tcp"
	}

	if provider.Port <= 0 {
		if provider.SSL {
			provider.Port = 563
			log.Print("WARN provider.Port not set. default to :563")
		} else {
			provider.Port = 119
			log.Print("WARN provider.Port not set. default to :119")
		}
	}

	rserver := fmt.Sprintf("%s:%d", provider.Host, provider.Port)
	wants_auth := (provider.Username != "" && provider.Password != "")
	provider.Conns = &ProviderConns{
		pool:    make(chan *ConnItem, provider.MaxConns),
		rserver: rserver, wants_auth: wants_auth,
	}

} // end func NewConnPool

func (c *ProviderConns) connect(wid int, provider *Provider, retry int) (connitem *ConnItem, err error) {

	if provider.MaxConnErrors >= 0 && retry > provider.MaxConnErrors {
		// set provider.MaxConnErrors to -1 to retry infinite
		return nil, fmt.Errorf("ERROR connect MaxConnErrors '%s'", provider.Name)
	} else if provider.MaxConnErrors < 0 && retry > 0 {
		retry = -1
	}

	var conn net.Conn
	start := time.Now()

	switch provider.SSL {
	case false:

		d := net.Dialer{Timeout: DefaultConnectTimeout}
		conn, err = d.Dial(provider.TCPMode, c.rserver)
		if cfg.opt.Debug {
			log.Printf("ConnPool connect tcp.Dial took='%v' '%s'", time.Since(start), provider.Name)
		}

	case true:

		conf := &tls.Config{
			InsecureSkipVerify: provider.SkipSslCheck,
		}
		ctx, cancel := context.WithTimeout(context.Background(), DefaultConnectTimeout)
		d := tls.Dialer{
			Config: conf,
		}
		conn, err = d.DialContext(ctx, provider.TCPMode, c.rserver)
		cancel()
		if cfg.opt.Debug {
			log.Printf("ConnPool connect tls.Dial took='%v' '%s'", time.Since(start), provider.Name)
		}
	} // end switch

	if err != nil || conn == nil {
		if conn != nil {
			conn.Close()
		}
		if isNetworkUnreachable(err) {
			return nil, fmt.Errorf("ERROR connect Unreachable network! '%s' @ '%s' err='%v'", provider.Host, provider.Name, err)
		}
		log.Printf("ERROR connect Dial rserver=%s wants_ssl=%t err='%v' retry in %.0fs", c.rserver, provider.SSL, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}

	srvtp := textproto.NewConn(conn)

	code, msg, err := srvtp.ReadCodeLine(20)

	if code < 200 || code > 201 {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR connect '%s' code=%d msg='%s' err='%v' retry in %.0fs", provider.Name, code, msg, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}

	if !c.wants_auth {
		return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
	}

	// send auth sequence
	id, err := srvtp.Cmd("AUTHINFO USER %s", provider.Username)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR AUTH1 FAILED '%s' err='%v' retry in %.0fs", provider.Name, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(381)
	srvtp.EndResponse(id)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR AUTH2 FAILED '%s' err='%v' retry in %.0fs", provider.Name, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}

	id, err = srvtp.Cmd("AUTHINFO PASS %s", provider.Password)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR AUTH3 FAILED '%s' err='%v' retry in %.0fs", provider.Name, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(281)
	srvtp.EndResponse(id)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR AUTH4 FAILED '%s' err='%v' retry in %.0fs", provider.Name, err, DefaultConnectErrSleep.Seconds())
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(wid, provider, retry)
	}

	return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
} // end func connect

func (c *ProviderConns) GetConn(wid int, provider *Provider) (connitem *ConnItem, err error) {
	if cfg.opt.Debug {
		Counter.incr("WaitingGetConns")
		defer Counter.decr("WaitingGetConns")
	}
	buf := make([]byte, 1, 1)
getConnFromPool:
	for {
		buf = buf[:0] // resets buf
		select {
		// try to get an idle conn from pool
		case connitem = <-c.pool:
			if connitem.conn == nil {
				log.Printf("WARN ConnPool GetConn: got nil conn @ '%s'... continue", provider.Name)
				c.CloseConn(provider, connitem)
				continue getConnFromPool // until chan rans empty
			}
			// instantly got an idle conn from pool!

			if connitem.expires > 0 && connitem.expires < time.Now().Unix() {
				// but this conn is old and could be already closed from remote
				// some provider have short timeout values.
				// try reading from conn. check takes some µs
				connitem.conn.SetReadDeadline(readDeadConn)
				if readBytes, rerr := connitem.conn.Read(buf); isNetConnClosedErr(rerr) || readBytes > 0 {
					log.Printf("INFO ConnPool GetConn: dead idle '%s' readBytes=(%d != 0?) err='%v' ... continue", provider.Name, readBytes, rerr)
					c.CloseConn(provider, connitem)
					continue getConnFromPool // until chan rans empty
				}
				connitem.conn.SetReadDeadline(noDeadLine)
			}
			connitem.expires = -1
			// conn should be fine. take that!
			if cfg.opt.Debug {
				Counter.incr("TOTAL_GetConns")
				log.Printf("GetConn OK '%s'", provider.Name)
			}
			return

		default:
			// chan ran empty: no idle conn in pool
			c.mux.RLock()
			oC := c.openConns
			c.mux.RUnlock()

			if oC == provider.MaxConns {
				waiting := time.Now()
				// infinite wait for a connection as we have 3 routines sharing the same connection
				connitem = <-c.pool
				if cfg.opt.Debug {
					Counter.incr("TOTAL_GetConns")
					log.Printf("GetConn waited='%v'", time.Since(waiting))
				}
				return
			}
			// open a new connection
			c.mux.Lock()
			if c.openConns < provider.MaxConns {
				c.openConns++
				if cfg.opt.Debug {
					log.Printf("NewConn '%s' inPool=%d open=%d/%d", provider.Name, len(c.pool), c.openConns, provider.MaxConns)
				}
				c.mux.Unlock()

				connitem, err = c.connect(wid, provider, 0)
				if err != nil {
					c.mux.Lock()
					c.openConns--
					c.mux.Unlock()
					return
				}
				if cfg.opt.Debug {
					Counter.incr("TOTAL_NewConns")
				}
				return // established new connection and returns connitem
			}
			c.mux.Unlock()
		} // end select
	} // end for
	// vet says: unreachable code
	//return nil, fmt.Errorf("ERROR in ConnPool GetConn: uncatched return! wid=%d", wid)
} // end func GetConn

func (c *ProviderConns) ParkConn(provider *Provider, connitem *ConnItem) {
	connitem.expires = time.Now().Unix() + DefaultConnExpireSeconds
	select {
	case c.pool <- connitem:
		// parked in pool
		if cfg.opt.Debug {
			Counter.incr("TOTAL_ParkedConns")
			log.Printf("ParkConn '%s' inPool=%d", provider.Name, len(c.pool))
		}
	default:
		// pool chan is full!!
		log.Printf("ERROR in ConnPool ParkConn: chan is full! provider '%s'. forceClose conn?!", provider.Name)
		c.CloseConn(provider, connitem)
	}
} // end func ParkConn

func (c *ProviderConns) CloseConn(provider *Provider, connitem *ConnItem) {
	if cfg.opt.Debug {
		Counter.incr("TOTAL_DisConns")
	}
	if connitem.conn != nil {
		connitem.conn.Close()
	}
	c.mux.Lock()
	c.openConns--
	if cfg.opt.Debug {
		log.Printf("DisConn '%s' inPool=%d open=%d", provider.Name, len(c.pool), c.openConns)
	}
	c.mux.Unlock()
} // end func CloseConn

func (c *ProviderConns) GetStats() (openconns int, idle int) {
	c.mux.RLock()
	openconns, idle = c.openConns, len(c.pool)
	c.mux.RUnlock()
	return
}

func isNetConnClosedErr(err error) bool {
	switch {
	case
		errors.Is(err, net.ErrClosed),
		errors.Is(err, io.EOF),
		errors.Is(err, syscall.EPIPE):
		return true
	default:
		return false
	}
} // end func isNetConnClosedErr (copy/paste from stackoverflow)

func isNetworkUnreachable(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if ok := errors.As(err, &opErr); ok {
		// This string check is still needed because "network is unreachable" is platform-specific
		return strings.Contains(opErr.Err.Error(), "network is unreachable")
	}

	return false
} // end func isNetworkUnreachable (written by AI! GPT-4o)
