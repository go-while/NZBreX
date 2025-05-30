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
	"io"
	"log"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// if conn was parked more than DefaultConnExpireSeconds:
	// try read from conn which takes some µs
	// no idea if this actually works! REVIEW
	DefaultConnExpireSeconds int64 = 20

	// if conn was parked more than DefaultConnCloserSeconds:
	// we close it and get a new one...
	// some provider close idle connections early without telling you...
	DefaultConnCloserSeconds int64 = 30
)

var (
	//DebugConnPool = false           // set to true to debug the ConnPool
	noDeadLine   time.Time         // used as zero value!
	readDeadConn = time.Unix(1, 0) // no syscall needed. value is 1 second after "0" (1970-01-01 00:00:01 UTC)
	ConnPools    = make(map[int]*ProviderConns)
	PoolsLock    sync.RWMutex
)

// holds an active connection to share around
type ConnItem struct {
	conn   net.Conn
	srvtp  *textproto.Conn
	writer *bufio.Writer
	parked int64 // will be set when parked. default 20s is fine on 99% of providers. some may close early as 30s.
}

// attaches to *Provider.Conns
type ProviderConns struct {
	openConns int // counter
	mux       sync.RWMutex
	pool      chan *ConnItem // idle/parked conns are in here
	//wait       []chan *ConnItem
	rserver    string // "host:port"
	wants_auth bool
	// we have created an endless loop
	// provider.Conns.provider.Conns.provider.Conns.provider.Conns.provider.Conns.provider.Conns.CloseConn(provider, connitem) xD
	// if we want to call closeconn from outside the routine which keep a provider ptr too
	// we cann call ConnPools[provider.id].CloseConn(provider, connitem)
	provider *Provider
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
		provider: provider,
	}
	PoolsLock.Lock()
	ConnPools[provider.id] = provider.Conns
	PoolsLock.Unlock()
	// no return value as we mutate the provider pointer!
} // end func NewConnPool

func KillConnPool(provider *Provider) {
	PoolsLock.Lock()
	defer PoolsLock.Unlock()

	if provider == nil || provider.Conns == nil {
		return
	}

	provider.Conns.mux.RLock()
	openConns := provider.Conns.openConns
	provider.Conns.mux.RUnlock()

	if openConns > 0 {
		if cfg.opt.DebugConnPool {
			log.Printf("KillConnPool: '%s'", provider.Name)
		}
		killed := 0
		for {
			select {
			case connitem := <-provider.Conns.pool:
				provider.Conns.CloseConn(connitem, nil)
				if cfg.opt.DebugConnPool {
					log.Printf("KillConnPool: '%s' closed a conn", provider.Name)
				}
				killed++
			default:
				// chan ran empty
			}
			provider.Conns.mux.RLock()
			openConns := provider.Conns.openConns
			provider.Conns.mux.RUnlock()
			if cfg.opt.DebugConnPool {
				log.Printf("KillConnPool: '%s' openConns=%d killed=%d", provider.Name, openConns, killed)
			}
			if openConns > 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			break
		} // end for
	}

	delete(ConnPools, provider.id)

	provider.mux.Lock()
	provider.Conns = nil
	provider.mux.Unlock()
	if cfg.opt.DebugConnPool {
		log.Printf("KillConnPool: closed '%s'", provider.Name)
	}
} // end func KillConnPool

// you should use GetConn which gets an idle conn from pool if available or a new one!
// connect() does not implement a MaxConn check!
// a conn returned from here can not be returned in the pool!
func (c *ProviderConns) connect(retry int) (connitem *ConnItem, err error) {

	if c.provider.MaxConnErrors >= 0 && retry > c.provider.MaxConnErrors {
		// set provider.MaxConnErrors to -1 to retry infinite
		return nil, fmt.Errorf("error connect MaxConnErrors > %d '%s'", c.provider.MaxConnErrors, c.provider.Name)
	} else if c.provider.MaxConnErrors < 0 && retry > 0 {
		retry = -1
	}

	var conn net.Conn
	start := time.Now()

	switch c.provider.SSL {
	case false:

		d := net.Dialer{Timeout: DefaultConnectTimeout}
		conn, err = d.Dial(c.provider.TCPMode, c.rserver)
		if cfg.opt.DebugConnPool {
			log.Printf("ConnPool connect tcp.Dial took='%v' '%s'", time.Since(start), c.provider.Name)
		}

	case true:

		conf := &tls.Config{
			InsecureSkipVerify: c.provider.SkipSslCheck,
		}
		ctx, cancel := context.WithTimeout(context.Background(), DefaultConnectTimeout)
		d := tls.Dialer{
			Config: conf,
		}
		conn, err = d.DialContext(ctx, c.provider.TCPMode, c.rserver)
		cancel()
		if cfg.opt.DebugConnPool {
			log.Printf("ConnPool connect tls.Dial took='%v' '%s'", time.Since(start), c.provider.Name)
		}
	} // end switch

	if err != nil {
		defer conn.Close()
		if isNetworkUnreachable(err) {
			return nil, fmt.Errorf("error connect Unreachable network! '%s' @ '%s' err='%v'", c.provider.Host, c.provider.Name, err)
		}
		log.Printf("ERROR connect Dial '%s' retry in %.0fs wants_ssl=%t err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), c.provider.SSL, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	srvtp := textproto.NewConn(conn)

	code, msg, err := srvtp.ReadCodeLine(20)

	if code < 200 || code > 201 {
		defer conn.Close()
		log.Printf("ERROR connect welcome '%s' retry in %.0fs code=%d msg='%s' err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, msg, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	if !c.wants_auth {
		return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
	}

	// send auth sequence
	id, err := srvtp.Cmd("AUTHINFO USER %s", c.provider.Username)
	if err != nil {
		defer conn.Close()
		log.Printf("ERROR AUTH#1 Cmd(AUTHINFO USER ...) '%s' retry in %.0fs err='%v' ", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(381)
	srvtp.EndResponse(id)
	if err != nil {
		defer conn.Close()
		log.Printf("ERROR AUTH#2 ReadCodeLine(381) step#2 '%s' retry in %.0fs code=%d err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	id, err = srvtp.Cmd("AUTHINFO PASS %s", c.provider.Password)
	if err != nil {
		defer conn.Close()
		log.Printf("ERROR AUTH#3 Cmd(AUTHINFO PASS ...) '%s' retry in %.0fs err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(281)
	srvtp.EndResponse(id)
	if err != nil {
		defer conn.Close()
		log.Printf("ERROR AUTH#4 ReadCodeLine(281) '%s' retry in %.0fs code=%d err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
} // end func connect

func (c *ProviderConns) GetConn() (connitem *ConnItem, err error) {

	if cfg.opt.DebugConnPool {
		GCounter.Incr("WaitingGetConns")
		defer GCounter.Decr("WaitingGetConns")
	}

getConnFromPool:
	for {
		select {
		// try to get an idle conn from pool
		case connitem = <-c.pool:
			if connitem == nil || connitem.conn == nil {
				log.Printf("WARN ConnPool GetConn: got nil conn @ '%s'... continue", c.provider.Name)
				c.CloseConn(connitem, nil)
				continue getConnFromPool // until chan rans empty
			}
			// instantly got an idle conn from pool!

			if connitem.parked > 0 && connitem.parked+DefaultConnCloserSeconds < time.Now().Unix() {

				// but this conn is old and could be already closed from remote
				log.Printf("INFO ConnPool GetConn: conn old '%s' ... continue", c.provider.Name)
				c.CloseConn(connitem, nil)
				continue getConnFromPool // until chan rans empty

			} else if connitem.parked > 0 && connitem.parked+DefaultConnExpireSeconds < time.Now().Unix() {
				buf := make([]byte, 1)
				// some provider have short timeout values.
				// try reading from conn. check takes some µs
				connitem.conn.SetReadDeadline(readDeadConn)
				if readBytes, rerr := connitem.conn.Read(buf); isNetConnClosedErr(rerr) || readBytes > 0 {
					log.Printf("INFO ConnPool GetConn: dead idle '%s' readBytes=(%d != 0?) err='%v' ... continue", c.provider.Name, readBytes, rerr)
					c.CloseConn(connitem, nil)
					continue getConnFromPool // until chan rans empty
				}
				connitem.conn.SetReadDeadline(noDeadLine)
				buf = nil
				log.Printf("INFO ConnPool GetConn: got long idle=(%d sec) '%s'", time.Now().Unix()-connitem.parked, c.provider.Name)

				// conn should be fine. take that!
			}

			// connitem.parked is set to 0 when connitem is no longer parked in pool
			connitem.parked = 0
			if cfg.opt.DebugConnPool {
				GCounter.Incr("TOTAL_GetConns")
				log.Printf("GetConn got one from pool '%s'", c.provider.Name)
			}
			return

		default:
			// chan ran empty: no idle conn in pool

			// check if provider has more connections established??
			// should be impossible to trigger
			c.mux.RLock()
			if c.openConns > c.provider.MaxConns {
				err = fmt.Errorf("error in GetConn: %d openConns > provider.MaxConns=%d @ '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
				c.mux.RUnlock()
				return
			}

			// check if provider has all connections established
			if c.openConns == c.provider.MaxConns {
				log.Printf("ConnPool GetConn: waiting all connections established! openConns=%d provider.MaxConns=%d '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
				c.mux.RUnlock()
				waiting := time.Now()
				// infinite wait for a connection as we have 3 routines (check, down, reup) sharing the same connection
				connitem = <-c.pool
				if cfg.opt.DebugConnPool {
					GCounter.Incr("TOTAL_GetConns")
					log.Printf("GetConn waited='%v'", time.Since(waiting))
				}

				log.Printf("ConnPool GetConn: released, got conn %s", c.provider.Name)
				return
			}
			c.mux.RUnlock()

			// try to open a new connection
			c.mux.Lock()
			connid := 0
			if c.openConns < c.provider.MaxConns {
				c.openConns++
				connid = c.openConns
				c.mux.Unlock()

				connitem, err = c.connect(0)
				if err != nil {
					c.mux.Lock()
					c.openConns--
					c.mux.Unlock()
					log.Printf("Error in ConnPool GetConn: connect failed '%s' err='%v'", c.provider.Name, err)
					return nil, err // connitem as nil and the error
				}
				// we have a new connection!
				GCounter.Incr("TOTAL_NewConns")
				if cfg.opt.DebugConnPool {
					c.mux.RLock()
					// always print NewConn message
					log.Printf("NewConn connid=%d '%s' inPool=%d open (after connect)=%d/%d", connid, c.provider.Name, len(c.pool), c.openConns, c.provider.MaxConns)
					c.mux.RUnlock()
				}
				return // established new connection and returns connitem
			} else {
				// another routine was faster... continue getConnFromPool
				c.mux.RLock()
				log.Printf("ConnPool GetConn: another routine was faster! openConns=%d provider.MaxConns=%d '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
				c.mux.RUnlock()
			}
			c.mux.Unlock()
		} // end select
	} // end for
	// vet says: unreachable code
	//return nil, fmt.Errorf("error in ConnPool GetConn: uncatched return! wid=%d", wid)
} // end func GetConn

func (c *ProviderConns) ParkConn(connitem *ConnItem) {

	connitem.parked = time.Now().Unix()
	select {
	case c.pool <- connitem:
		// parked in pool
		if cfg.opt.DebugConnPool {
			GCounter.Incr("TOTAL_ParkedConns")
			log.Printf("ParkConn '%s' inPool=%d", c.provider.Name, len(c.pool))
		}
	default:
		// pool chan is full!!
		log.Printf("ERROR in ConnPool ParkConn: chan is full! provider '%s'. forceClose conn?!", c.provider.Name)
		c.CloseConn(connitem, nil)
	}
} // end func ParkConn

func (c *ProviderConns) CloseConn(connitem *ConnItem, sharedConnChan chan *ConnItem) {

	if cfg.opt.DebugConnPool {
		GCounter.Incr("TOTAL_DisConns")
	}
	if connitem.conn != nil {
		connitem.conn.Close()
	}
	c.mux.Lock()
	c.openConns--
	// always print DisConn message
	log.Printf("DisConn '%s' inPool=%d open=%d", c.provider.Name, len(c.pool), c.openConns)
	c.mux.Unlock()
	// if a sharedConnChan is supplied we send a nil to the channel
	// a nil as connitem signals the routines to get a new conn
	// mostly because conn was closed by network, protocol error or timeout
	if sharedConnChan != nil {
		sharedConnChan <- nil
	}
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

// GetNewSharedConnChannel creates a new shared connection channel.
// The sharedCC channel is used to share connections between (anonymous) goroutines which will work on the same item.
// which will work on the same item.
func GetNewSharedConnChannel(wid int, provider *Provider) (sharedCC chan *ConnItem, err error) {
	sharedCC = make(chan *ConnItem, 1)        // buffered channel with size 1
	connitem, err := provider.Conns.GetConn() // get an idle or new connection from the pool
	if err != nil {
		log.Printf("ERROR a GoWorker (%d) failed to connect '%s' err='%v'", wid, provider.Name, err)
		return nil, err
	}
	//sharedCC <- nil                    // put a nil item into the channel to signal that no connection is available yet
	sharedCC <- connitem
	return sharedCC, nil
}

// SharedConnGet gets a connection from the sharedCC channel.
// If no connection is available (got a nil connitem), it will request a new one from the provider's connection pool.
// SharedConnGet returns a ConnItem with a valid connection or an error if no connection is available.
func SharedConnGet(sharedCC chan *ConnItem, provider *Provider) (connitem *ConnItem, err error) {
	needNewConn := false
	// get a connection from the shared conn channel
	// blocks until an item with a connection is available
	// goWorker should have put a nil item first into the sharedCC channel
	// if any routine in this worker gets a nil connitem from sharedCC:
	//  - it will get a new connection from the provider
	//  - and put the conn back into the sharedCC channel later
	aconnitem := <-sharedCC
	switch aconnitem {
	case nil:
		// no connection available
		needNewConn = true
	default:
		// connitem is a ConnItem, check the .conn field
		if aconnitem.conn == nil {
			// but the .conn is nil
			log.Printf("ERROR SharedConnGet: .conn is nil aconnitem='%#v' provider='%s'", aconnitem, provider.Name)
			needNewConn = true
		}
	}
	if !needNewConn && aconnitem != nil && aconnitem.conn != nil {
		// we have a valid connection, use it
		log.Printf("SharedConnGet: got shared connection '%s'", provider.Name)
		return aconnitem, nil
	}

	// no valid connection available, try to get one from the pool
	log.Printf("SharedConnGet: no shared connection available, trying to get a new one from pool '%s'", provider.Name)
	newconnitem, err := provider.Conns.GetConn()
	if err != nil || newconnitem == nil || newconnitem.conn == nil {
		// unable to get a new connection, put nil back into sharedCC
		log.Printf("ERROR SharedConnGet connect failed Provider '%s' err='%v'", provider.Name, err)
		sharedCC <- nil // put nil back into sharedCC
		return
	}
	connitem = newconnitem // use the new connection item

	return
} // end func SharedConnGet

func SharedConnReturn(sharedCC chan *ConnItem, connitem *ConnItem) {
	sharedCC <- connitem // put the connection back into the channel to share it with other goroutines
} // end func SharedConnReturn

// ReturnSharedConnToPool returns a shared connection to the pool.
// This is used when the connection is no longer needed but still valid.
// It does not close the connection, just parks it back into the pool.
// This function is used to return a connection to the pool for reuse.
func ReturnSharedConnToPool(sharedCC chan *ConnItem, provider *Provider, connitem *ConnItem) {
	if cfg.opt.DebugConnPool {
		log.Printf("ReturnSharedConnToPool: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	}
	if connitem == nil || connitem.conn == nil {
		provider.Conns.CloseConn(connitem, sharedCC) // close the connection to reduce the counter of open connections
		log.Printf("WARN ReturnSharedConnToPool: connitem is nil or conn.conn is nil! provider='%s'", provider.Name)
		return
	}
	provider.Conns.ParkConn(connitem) // put the connection back into the pool
}

// ReturnSharedConnToPoolAndClose returns a shared connection to the pool and closes it.
// This is used when the connection is no longer needed or has expired.
func ReturnSharedConnToPoolAndClose(sharedCC chan *ConnItem, provider *Provider, connitem *ConnItem) {
	if cfg.opt.DebugConnPool {
		log.Printf("ReturnSharedConnToPoolAndClose: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	}
	if connitem == nil || connitem.conn == nil {
		provider.Conns.CloseConn(connitem, sharedCC) // close the connection to reduce the counter of open connections
		log.Printf("WARN ReturnSharedConnToPoolAndClose: connitem is nil or conn.conn is nil! provider='%s'", provider.Name)
		return
	}
	provider.Conns.CloseConn(connitem, sharedCC) // close the connection and return it to the shared channel
}
