package main

// ConnPool is a connection pool for a provider.
// It manages a pool of connections to the provider's server.
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
	"math/rand"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-while/go-loggedrwmutex"
)

const nntpWelcomeCodeMin = 200
const nntpWelcomeCodeMax = 201
const nntpMoreInfoCode = 381
const nntpAuthSuccess = 281

const (
	// if conn was parked more than DefaultConnExpire:
	// try read from conn which takes some µs
	// no idea if this actually works! REVIEW
	DefaultConnExpire = 20 * time.Second

	// if conn was parked more than DefaultConnCloser:
	// we close it and get a new one...
	// some provider close idle connections early without telling you...
	DefaultConnCloser = 30 * time.Second

	// read deadline for a connection, after this time we close the connection if no data was received
	DefaultConnReadDeadline = 60 * time.Second
)

var (
	//noDeadLine   time.Time                 // used as zero value!
	readDeadConn = time.Unix(1, 0)         // no syscall needed. value is 1 second after "0" (1970-01-01 00:00:01 UTC)
	ConnPools    = make(map[int]*ConnPool) // used in KillConnPool
	PoolsLock    sync.RWMutex
)

// holds an active connection to share around
type ConnItem struct {
	c         *ConnPool // pointer to the ConnPool which created this ConnItem
	conn      net.Conn
	connid    uint64 // unique connection id
	srvtp     *textproto.Conn
	writer    *bufio.Writer
	created   time.Time // time when this conn was created
	parktime  time.Time // will be set when parked. default 20s is fine on 99% of providers. some may close early as 30s.
	parkedCnt uint64    // how many times this conn was parked in the pool
}

// attaches to *provider.ConnPool
type ConnPool struct {
	openConns  int    // counter
	nextconnId uint64 // unique connection id
	//mux        sync.RWMutex
	mux     *loggedrwmutex.LoggedSyncRWMutex
	counter *Counter_uint64      // used to count connections, parked conns, etc.
	pool    chan *ConnItem       // idle/parked conns are in here
	poolmap map[uint64]*ConnItem // idle/parked conns are in here
	//wait       []chan *ConnItem
	rserver    string // "host:port"
	wants_auth bool
	// we have created an endless loop
	// provider.ConnPool.provider.ConnPool.provider.ConnPool.provider.ConnPool.provider.ConnPool.provider.ConnPool.CloseConn(provider, connitem) xD
	// if we want to call closeconn from outside the routine which keep a provider ptr too
	// we cann call ConnPools[provider.id].CloseConn(provider, connitem)
	provider *Provider // pointer to the provider which created this ConnPool and holds the config
	s        *SESSION  // session pointer to the session which created this ConnPool (access via c.s.%%SESSIONVARIABLES%% OR provider.ConnPool.s.%%SESSIONVARIABLES%%).
}

func NewConnPool(s *SESSION, provider *Provider, workerWGconnReady *sync.WaitGroup) {
	provider.mux.Lock()         // NewConnPool mutex #d028
	defer provider.mux.Unlock() // NewConnPool mutex #d028

	if provider.ConnPool != nil {
		dlog(always, "ERROR NewConnPool: provider.ConnPool already exists for '%s'!", provider.Name)
		return
	}

	if provider.Host == "" {
		dlog(always, "ERROR connect '%s' provider.Host empty!", provider.Name)
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

	provider.ConnPool = &ConnPool{
		counter: NewCounter(10), // used to count connections, parked conns, etc.
		pool:    make(chan *ConnItem, provider.MaxConns),
		poolmap: make(map[uint64]*ConnItem, provider.MaxConns), // use map
		rserver: rserver, wants_auth: wants_auth,
		provider: provider,
		mux:      &loggedrwmutex.LoggedSyncRWMutex{Name: fmt.Sprintf("ConnPool-%s", provider.Name)},
		s:        s, // session pointer to the session which created this ConnPool
	}
	PoolsLock.Lock()
	ConnPools[provider.id] = provider.ConnPool
	PoolsLock.Unlock()
	if !cfg.opt.CheckOnly {
		go Speedmeter(s.nzbSize, provider.ConnPool, nil, workerWGconnReady) // start speed meter for this ConnPool / Provider
	}
	go provider.ConnPool.WatchOpenConnsThread(workerWGconnReady)
	// no return value as we mutate the provider pointer!
} // end func NewConnPool

func KillConnPool(provider *Provider) {
	PoolsLock.Lock()
	defer PoolsLock.Unlock()

	if provider == nil || provider.ConnPool == nil {
		return
	}

	provider.ConnPool.mux.RLock()
	openConns := provider.ConnPool.openConns
	provider.ConnPool.mux.RUnlock()

	if openConns > 0 {
		dlog(cfg.opt.DebugConnPool, "KillConnPool: '%s'", provider.Name)
		killed := 0
		for {
			select {
			case connitem := <-provider.ConnPool.pool:
				provider.ConnPool.CloseConn(connitem, nil)

				dlog(cfg.opt.DebugConnPool, "KillConnPool: '%s' closed a conn", provider.Name)

				killed++
			default:
				// chan ran empty
			}
			provider.ConnPool.mux.RLock()
			openConns := provider.ConnPool.openConns
			provider.ConnPool.mux.RUnlock()
			dlog(cfg.opt.DebugConnPool, "KillConnPool: '%s' openConns=%d killed=%d", provider.Name, openConns, killed)
			if openConns > 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			break
		} // end for
	}

	delete(ConnPools, provider.id)

	provider.mux.Lock()
	provider.ConnPool = nil
	provider.mux.Unlock()
	dlog(cfg.opt.DebugConnPool, "KillConnPool: closed '%s'", provider.Name)
} // end func KillConnPool

// you should use GetConn which gets an idle conn from pool if available or a new one!
// connect() does not implement a MaxConn check!
// a conn returned from here can not be returned in the pool!
// this function returns a connitem in 2 places:
//   - directly after connecting if no auth is needed
//   - after successful auth
func (c *ConnPool) connect() (connitem *ConnItem, err error) {
	time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond) // random sleep to avoid all threads connecting at the same time
	var conn net.Conn
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), DefaultConnectTimeout)
	defer cancel()
	switch c.provider.SSL {
	case false:

		d := net.Dialer{}
		conn, err = d.DialContext(ctx, c.provider.TCPMode, c.rserver)
		dlog(cfg.opt.DebugConnPool, "ConnPool raw tcp.Dial took='%v' '%s'", time.Since(start), c.provider.Name)
		if err != nil {
			if isNetworkUnreachable(err) {
				return nil, fmt.Errorf("error connect Unreachable network! '%s' @ '%s' err='%v'", c.provider.Host, c.provider.Name, err)
			}
			dlog(always, "ERROR connect Dial '%s' retry in %.0fs wants_ssl=%t err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), c.provider.SSL, err)
			//time.Sleep(DefaultConnectErrSleep)
			return nil, err
		}
	case true:
		conf := &tls.Config{
			InsecureSkipVerify: c.provider.SkipSslCheck,
		}
		d := tls.Dialer{
			Config: conf,
		}
		conn, err = d.DialContext(ctx, c.provider.TCPMode, c.rserver)
		dlog(cfg.opt.DebugConnPool, "ConnPool raw tls.Dial took='%v' '%s'", time.Since(start), c.provider.Name)
		if err != nil {
			if isNetworkUnreachable(err) {
				return nil, fmt.Errorf("error connect Unreachable network! '%s' @ '%s' err='%v'", c.provider.Host, c.provider.Name, err)
			}
			dlog(always, "ERROR connect Dial '%s' retry in %.0fs wants_ssl=%t err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), c.provider.SSL, err)
			//time.Sleep(DefaultConnectErrSleep)
			return nil, err
		}
	} // end switch
	dlog(cfg.opt.DebugConnPool, "ConPool connect raw conn established '%s' took=(%d ms)", c.provider.Name, time.Since(start).Milliseconds())

	time0 := time.Now()
	srvtp := textproto.NewConn(conn)
	dlog(cfg.opt.DebugConnPool, "ConnPool connect textproto.NewConn took=(%d ms) '%s'", time.Since(time0).Milliseconds(), c.provider.Name)
	time00 := time.Now()
	code, msg, err := srvtp.ReadCodeLine(200)
	dlog(cfg.opt.DebugConnPool, "ConnPool connect ReadCodeLine(20) took=(%d ms) '%s' code=%d msg='%s' err='%v'", time.Since(time00).Milliseconds(), c.provider.Name, code, msg, err)
	time000 := time.Now()

	if code < nntpWelcomeCodeMin || code > nntpWelcomeCodeMax { // not a valid nntp welcome code
		if conn != nil {
			conn.Close()
		}
		dlog(always, "ERROR connect welcome '%s' retry in %.0fs code=%d msg='%s' err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, msg, err)
		//time.Sleep(DefaultConnectErrSleep)
		return nil, err
	}
	dlog(cfg.opt.DebugConnPool, "ConnPool connect welcome '%s' time0=(%d ms) time00=(%d ms) time000=(%d ms)", c.provider.Name, time.Since(time0).Milliseconds(), time.Since(time00).Milliseconds(), time.Since(time000).Milliseconds())
	if !c.wants_auth {
		return &ConnItem{connid: c.newconnid(), created: time.Now(), srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn), c: c}, err
	}
	return c.auth(srvtp, conn, start)
} // end func connect

func (c *ConnPool) auth(srvtp *textproto.Conn, conn net.Conn, start time.Time) (connitem *ConnItem, err error) {
	var code int
	var msg string
	time1 := time.Now()
	// send auth user sequence
	id, err := srvtp.Cmd("AUTHINFO USER %s", c.provider.Username)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		dlog(always, "ERROR AUTH#1 Cmd(AUTHINFO USER ...) '%s' retry in %.0fs err='%v' ", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		//time.Sleep(DefaultConnectErrSleep)
		return nil, err
	}
	time2 := time.Now()
	// await response from server
	srvtp.StartResponse(id)
	code, msg, err = srvtp.ReadCodeLine(nntpMoreInfoCode) // 381 is the code for "more information required"
	srvtp.EndResponse(id)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		dlog(always, "ERROR AUTH#2 ReadCodeLine(381) step#2 '%s' retry in %.0fs code=%d msg='%s' err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, msg, err)
		time.Sleep(DefaultConnectErrSleep)
		return nil, err
	}
	time3 := time.Now()
	// send auth password sequence
	id, err = srvtp.Cmd("AUTHINFO PASS %s", c.provider.Password)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		dlog(always, "ERROR AUTH#3 Cmd(AUTHINFO PASS ...) '%s' retry in %.0fs err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		//time.Sleep(DefaultConnectErrSleep)
		return nil, err
	}
	time4 := time.Now()
	// await response from server
	srvtp.StartResponse(id)
	code, msg, err = srvtp.ReadCodeLine(nntpAuthSuccess) // 281 is the code for "authentication successful"
	srvtp.EndResponse(id)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		dlog(always, "ERROR AUTH#4 ReadCodeLine(281) '%s' retry in %.0fs code=%d msg='%s' err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, msg, err)
		//time.Sleep(DefaultConnectErrSleep)
		return nil, err
	}
	time5 := time.Now()
	connitem = &ConnItem{connid: c.newconnid(), created: time.Now(), srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}
	dlog(cfg.opt.DebugConnPool, "ConnPool AUTH took=(%d ms) time1=(%d ms) time2=(%d ms) time3=(%d ms) time4=(%d ms) time5=(%d ms) '%s'", time.Since(start).Milliseconds(), time.Since(time1).Milliseconds(), time.Since(time2).Milliseconds(), time.Since(time3).Milliseconds(), time.Since(time4).Milliseconds(), time.Since(time5).Milliseconds(), c.provider.Name)
	dlog(cfg.opt.BUG, "ConnPool AUTH '%s' err='%v' newConnItem='%#v'", c.provider.Name, err, connitem)
	return connitem, err
} // end func auth

func (c *ConnPool) NewConn() (connitem *ConnItem, err error) {
	// try to open a new connection
	c.mux.Lock() // mutex c459a
	if c.openConns == c.provider.MaxConns {
		// another routine was faster...
		dlog(cfg.opt.DebugConnPool, "ConnPool NewConn: another routine was faster! openConns=%d provider.MaxConns=%d '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
		c.mux.Unlock() // mutex c459a
		err = fmt.Errorf("retry getConn, not newConn! all conns are already established")
		return
	}
	if c.openConns < c.provider.MaxConns {
		dlog(cfg.opt.DebugConnPool, "ConnPool NewConn: connect to '%s' openConns=%d provider.MaxConns=%d", c.provider.Name, c.openConns, c.provider.MaxConns)
		c.openConns++
		openConnsBefore := c.openConns
		c.mux.Unlock() // mutex c459a
		start := time.Now()
		// connect to the provider
		if c.provider.MaxConnErrors <= 0 {
			for {
				connitem, err = c.connect()
				if err != nil || connitem == nil || connitem.conn == nil {
					continue // retry connect
				}
				if cfg.opt.DebugConnPool {
					c.mux.RLock() // mutex c461
					dlog(always, "ConnPool NewConn: got new connid=%d '%s' openConns after=%d/%d before=%d connectTook=(%d ms) err='%v", connitem.connid, c.provider.Name, c.openConns, c.provider.MaxConns, openConnsBefore, time.Since(start).Milliseconds(), err)
					c.mux.RUnlock() // mutex c461
				}
				// we have a new connection!
				GCounter.Incr("TOTAL_NewConns")
				return // established new connection and returns connitem
			}
		} else {
			for retried := 0; retried < c.provider.MaxConnErrors; retried++ {
				connitem, err = c.connect()
				if err != nil || connitem == nil || connitem.conn == nil {
					continue // retry connect
				}
				if cfg.opt.DebugConnPool {
					c.mux.RLock() // mutex c461
					dlog(always, "ConnPool NewConn: got new connid=%d '%s' openConns after=%d/%d before=%d connectTook=(%d ms) err='%v", connitem.connid, c.provider.Name, c.openConns, c.provider.MaxConns, openConnsBefore, time.Since(start).Milliseconds(), err)
					c.mux.RUnlock() // mutex c461
				}
				// we have a new connection!
				GCounter.Incr("TOTAL_NewConns")
				return // established new connection and returns connitem
			}
		}
		c.mux.Lock() // mutex c460
		c.openConns--
		dlog(always, "ERROR in ConnPool NewConn: connect failed '%s' openConns=%d connitem='%v' err='%v'", c.provider.Name, c.openConns, connitem, err)
		c.mux.Unlock() // mutex c460
		return
	}
	c.mux.Unlock() // mutex c459a
	return
} // end func NewConn

func (c *ConnPool) GetConn() (connitem *ConnItem, err error) {
	start := time.Now()
	if cfg.opt.DebugConnPool {
		GCounter.Incr("WaitingGetConns")
		defer GCounter.Decr("WaitingGetConns")
		dlog(always, "ConnPool GetConn request for '%s'", c.provider.Name)
	}
	var waiting time.Time // used to measure waiting time

	retried := 0
getConnFromPool:
	for {
		select {
		// try to get an idle conn from pool
		case connitem = <-c.pool:
			if connitem == nil || connitem.conn == nil || connitem.parktime.IsZero() {
				dlog(always, "WARN ConnPool GetConn: got nil conn @ '%s'... continue", c.provider.Name)
				go c.CloseConn(connitem, nil)
				continue getConnFromPool // until chan rans empty
			}
			// instantly got an idle conn from pool!

			maybeDead := connitem.parktime.Add(DefaultConnCloser).Before(time.Now())
			if maybeDead {
				// but this conn is old and could be already closed from remote
				dlog(cfg.opt.DebugConnPool, "INFO ConnPool GetConn: conn old age=(%v) '%s' ... continue", time.Since(connitem.parktime), c.provider.Name)
				c.CloseConn(connitem, nil)
				continue getConnFromPool // until chan rans empty
			}

			// check if conn is still alive
			isExpired := connitem.parktime.Add(DefaultConnExpire).Before(time.Now())
			if isExpired {

				if UseReadDeadConn {
					buf := make([]byte, 1)
					// some provider have short timeout values.
					// try reading from conn. check takes some µs
					connitem.conn.SetReadDeadline(readDeadConn)
					if readBytes, rerr := connitem.conn.Read(buf); isNetConnClosedErr(rerr) || readBytes > 0 {
						dlog(always, "INFO ConnPool GetConn: dead idle '%s' readBytes=(%d != 0?) err='%v' ... continue", c.provider.Name, readBytes, rerr)
						c.CloseConn(connitem, nil)
						continue getConnFromPool // until chan rans empty
					}
					// long idle
					buf = nil
					dlog(cfg.opt.DebugConnPool, "INFO ConnPool GetConn: got long idle=(%d ms) '%s', passed Read test", time.Since(connitem.parktime).Milliseconds(), c.provider.Name)

				} else {

					dlog(always, "INFO ConnPool GetConn: got long idle=(%d ms) '%s', close and get NewConn", time.Since(connitem.parktime).Milliseconds(), c.provider.Name)
					c.CloseConn(connitem, nil)
					connitem, err = c.NewConn()
					if connitem == nil || err != nil {
						continue getConnFromPool
					}
					// extend the read deadline of the new connection
				}

				c.ExtendConn(connitem)
			}
			// we have a valid connection from the pool which is no longer parked
			connitem.parktime = time.Time{} // set parktime to zero value
			c.ExtendConn(connitem)

			GCounter.Incr("TOTAL_GetConns")
			dlog(cfg.opt.DebugConnPool, "GetConn got one from pool '%s' took='%v'", c.provider.Name, time.Since(start))
			return

		default:
			// chan ran empty: no idle conn in pool
			// check if provider has more connections established??
			// should be impossible to trigger
			c.mux.RLock() // mutex c458
			if c.openConns > c.provider.MaxConns {
				err = fmt.Errorf("error in GetConn: %d openConns > provider.MaxConns=%d @ '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
				c.mux.RUnlock() // mutex c458
				return
			}
			dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: no idle conn in pool '%s' openConns=%d provider.MaxConns=%d", c.provider.Name, c.openConns, c.provider.MaxConns)
			// check if provider has all connections established
			if c.openConns == c.provider.MaxConns {
				dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: waiting all connections established! openConns=%d provider.MaxConns=%d '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)

				c.mux.RUnlock() // mutex c458
				// infinite wait for a connection as we have 3 routines (check, down, reup) sharing the same connection

				waiting = time.Now()
				dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: infinite waiting for a conn '%s'", c.provider.Name)

				connitem = <-c.pool
				GCounter.Incr("TOTAL_GetConns")
				dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: released, got connid=%d age=(%v) %s: waited='%v'", connitem.connid, time.Since(connitem.parktime), c.provider.Name, time.Since(waiting))
				return
			}
			dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: try to open a new connection '%s' openConns=%d provider.MaxConns=%d", c.provider.Name, c.openConns, c.provider.MaxConns)
			c.mux.RUnlock() // mutex c458

			// try to open a new connection
			newconnitem, aerr := c.NewConn()
			if newconnitem == nil || aerr != nil {
				dlog(cfg.opt.DebugConnPool, "WARN in ConnPool GetConn: NewConn failed '%s' connitem='%v' aerr='%v'", c.provider.Name, newconnitem, aerr)
				time.Sleep(DefaultConnectErrSleep) // wait a bit before retrying
				retried++
				if retried >= c.provider.MaxConnErrors {
					// we have retried too often
					dlog(always, "ERROR in ConnPool GetConn: retried %d times to get a new conn '%s' giving up! aerr='%v'", retried, c.provider.Name, aerr)
					//c.mux.Lock()          // mutex c457
					//c.openConns--         // decrease open conns as we failed to get a new one
					//c.mux.Unlock()        // mutex c457
					err = aerr
					break getConnFromPool // break out of the for loop
				}
				continue getConnFromPool // until chan rans empty
			}
			c.ExtendConn(newconnitem)
			// we have a new connection!
			return newconnitem, aerr
		} // end select
	} // end for
	return nil, fmt.Errorf("error in ConnPool GetConn: err='%v'", err)
} // end func GetConn

// ExtendConn extends the read deadline of a connection.
func (c *ConnPool) ExtendConn(connitem *ConnItem) {
	if UseNoDeadline {
		return
	}
	if connitem == nil || connitem.conn == nil {
		return
	}
	connitem.conn.SetReadDeadline(time.Now().Add(DefaultConnReadDeadline)) // ExtendConn()
}

// newconnid generates a new unique connection id for the ConnItem.
func (c *ConnPool) newconnid() (newid uint64) {
	c.mux.Lock()
	c.nextconnId++
	newid = c.nextconnId
	c.mux.Unlock()
	return
}

// ParkConn parks a connection in the pool.
// It will set the parktime to the current time and increase the parkedCnt.
// If the pool is full, it will close the connection instead which is most likely a bug in the code.
func (c *ConnPool) ParkConn(wid int, connitem *ConnItem, src string) {

	connitem.parktime = time.Now()
	connitem.parkedCnt += 1
	c.ExtendConn(connitem) // ParkConn
	select {
	case c.pool <- connitem:
		// parked in pool
		//if cfg.opt.DebugConnPool && !silent {
		GCounter.Incr("TOTAL_ParkedConns")
		if always || cfg.opt.BUG && cfg.opt.DebugConnPool {
			c.mux.RLock()
			dlog(cfg.opt.DebugConnPool && cfg.opt.DebugWorker, "ParkedConn connid=%d (wid=%d) '%s' inPool=%d cap=%d openConns=%d src='%s'", connitem.connid, wid, c.provider.Name, len(c.pool), cap(c.pool), c.openConns, src)
			c.mux.RUnlock()
		}
	default:
		// pool chan is full!!
		dlog(always, "ERROR in ConnPool ParkConn: connid=%d (wid=%d) chan is full! provider '%s'. forceClose conn src='%s'", connitem.connid, wid, c.provider.Name, src)
		c.CloseConn(connitem, nil)
	}
} // end func ParkConn

// CloseConn closes a connection and removes it from the pool.
// If a sharedConnChan is supplied, it will send a nil to the channel to signal that the connection was closed.
// This is used to signal the routines to get a new connection from the pool.
// If the connection is nil, it will not close it and just remove it from the pool.
// If the connection is already closed, it will not close it again.
func (c *ConnPool) CloseConn(connitem *ConnItem, sharedConnChan chan *ConnItem) {

	if cfg.opt.DebugConnPool {
		GCounter.Incr("TOTAL_DisConns")
	}
	if connitem.srvtp != nil {
		// close the textproto connection
		if err := connitem.srvtp.Close(); err != nil {
			//dlog(always, "ERROR CloseConn: srvtp.Close() '%s' err='%v'", c.provider.Name, err)
		}
		connitem.srvtp = nil // set to nil to avoid double close
	}
	if connitem.conn != nil {
		if err := connitem.conn.Close(); err != nil {
			/*
				if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.EBADF) {
					// connection was already closed
				} else {
					dlog(always, "ERROR CloseConn: conn.Close() '%s' err='%v'", c.provider.Name, err)
				}
			*/
		}
		connitem.conn = nil // set to nil to avoid double close
	}
	c.mux.Lock()
	c.openConns--
	dlog(cfg.opt.DebugConnPool, "DisConn '%s' inPool=%d open=%d", c.provider.Name, len(c.pool), c.openConns)
	c.mux.Unlock()
	// if a sharedConnChan is supplied we send a nil to the channel
	// a nil as connitem signals the routines to get a new conn
	// mostly because conn was closed by network, protocol error or timeout
	if sharedConnChan != nil {
		sharedConnChan <- nil
	}
} // end func CloseConn

// GetStats returns the current stats of the connection pool.
// It returns the number of open connections and the number of idle connections in the pool.
// This is used to monitor the connection pool and to see if it is working as expected.
func (c *ConnPool) GetStats() (openconns int, idle int) {
	c.mux.RLock()
	openconns, idle = c.openConns, len(c.pool)
	c.mux.RUnlock()
	return
}

// WatchOpenConnsThread is a thread which watches the open connections and closes them if they are idle for too long.
// It will close the connection if it is idle for more than DefaultConnCloser.
// It will also try to open new connections if there are no idle connections in the pool.
// This is used to keep the connection pool healthy and to avoid idle connections which are not used.
// It will run until the global stopChan is closed or the provider is killed.
// It will also print the status of the mutexes for debugging purposes.
func (c *ConnPool) WatchOpenConnsThread(workerWGconnReady *sync.WaitGroup) {
	start := time.Now()
	workerWGconnReady.Wait()
	dlog(cfg.opt.Debug, "WatchOpenConnsThread started for '%s' waited=%v", c.provider.Name, time.Since(start))
	defer dlog(cfg.opt.Debug, "WatchOpenConnsThread for '%s' defer returned", c.provider.Name)
	//tempItems := []*ConnItem{} // temporary slice to hold ConnItems
forever:
	for {
		time.Sleep(time.Millisecond * 15000) // check every N milliseconds
		if !loggedrwmutex.DisableLogging {
			c.provider.mux.Lock()
			if c.provider.ConnPool != nil {
				c.provider.mux.PrintStatus(cfg.opt.Debug)          // prints mutex status for the provider
				c.provider.ConnPool.mux.PrintStatus(cfg.opt.Debug) // prints mutex status for the providers connpool
			}
			c.provider.mux.Unlock()
		}
		//globalmux.PrintStatus(cfg.opt.Debug)               // prints global mutex status for the whole app
		//memlim.mux.PrintStatus(cfg.opt.Debug)              // prints memory limit mutex status
		// capture this moment, might by stale instantly
		c.mux.RLock()
		openConns := c.openConns
		poolConns := len(c.pool)
		c.mux.RUnlock()
		/*
			if openConns <= 0 {
				dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: no open connections for '%s' ... try and open some for the pool", c.provider.Name)
				for i := 1; i <= c.provider.MaxConns; i++ {
					connitem, err, retry := c.NewConn()
					if err != nil || connitem == nil {
						dlog(cfg.opt.DebugConnPool, "NOTICE in WatchOpenConnsThread: NewConn failed '%s' err='%v' retry=%t", c.provider.Name, err, retry)
						continue forever
					}
					c.ParkConn(0, connitem, "WatchOpenConnsThread:prefill") // park the connection in the pool
				}
			}
		*/
		if openConns == c.provider.MaxConns && len(c.pool) == 0 {
			// all conns are possibly in use or in shared channels
			dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: all %d conns are possibly in shared chan for '%s' ... continue", openConns, c.provider.Name)
			continue forever
		} else {
			dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: openConns=%d idle=%d '%s'", openConns, poolConns, c.provider.Name)
		}
		/*
				// check all connections in the pool

				var alive, checked uint64
				//start := time.Now()
				//tA := time.After(250 * time.Microsecond)
			checkPool:
				for {
					select {
					case connitem := <-c.pool:
						if connitem != nil && connitem.conn != nil {
							if !IsConnExpired(connitem.parktime, DefaultConnCloser) {
								tempItems = append(tempItems, connitem)
								alive++
								continue checkPool
							}
						}

						dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: closing idle conn '%s' parktime='%v' idleSince=(%d ms)", c.provider.Name, connitem.parktime, time.Since(connitem.parktime).Milliseconds())
						c.CloseConn(connitem, nil)          // close the connection
						connitem, err, retry := c.NewConn() // get a new connection from the pool
						if err != nil || connitem == nil {
							dlog(cfg.opt.DebugConnPool, "NOTICE in WatchOpenConnsThread: closed idle conn but NewConn failed '%s' err='%v' retry=%t", c.provider.Name, err, retry)
							c.CloseConn(connitem, nil)
							continue checkPool
						}
						tempItems = append(tempItems, connitem)
						checked++

					case stopSignal, ok := <-c.s.stopChan:
						if !ok {
							dlog(cfg.opt.Debug, "WatchOpenConnsThread: stopChan !ok for '%s' exiting", c.provider.Name)
							return
						}
						c.s.stopChan <- stopSignal // signal to stop any other listeners
						dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: got stopSignal for '%s'", c.provider.Name)
						break forever // exit the forever loop

					case <-tA:
						// no more items in the pool, break the checkPool loop
						dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: (tA) '%s': ALIVE=%d CHECKED=%d OPEN=%d POOL=%d took='%v'", c.provider.Name, alive, checked, openConns, poolConns, time.Since(start))
						break checkPool

					default:
						// no more items in the pool, break the checkPool loop
						dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: (ch) '%s': ALIVE=%d CHECKED=%d OPEN=%d POOL=%d took='%v'", c.provider.Name, alive, checked, openConns, poolConns, time.Since(start))
						break checkPool
					}
				} // end for checkPool
				// put all items back into the pool
				for _, connitem := range tempItems {
					c.ParkConn(0, connitem, "WatchOpenConnsThread:put") // park the connection in the pool
				}
				tempItems = []*ConnItem{}
		*/
		continue forever // continue the forever loop to check again
	} // end forever
} // end func WatchOpenConnsThread

// isNetConnClosedErr checks if the error is a network connection closed error.
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

func IsConnExpired(input time.Time, timeout time.Duration) bool {
	return input.Add(timeout).Before(time.Now())
} // end func IsConnExpired

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
	sharedCC = make(chan *ConnItem, 1)           // buffered channel with size 1
	connitem, err := provider.ConnPool.GetConn() // get an idle or new connection from the pool
	if err != nil {
		dlog(always, "ERROR a GoWorker (%d) failed to connect '%s' err='%v'", wid, provider.Name, err)
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
	// goWorker should have put a nil item first into the sharedCC channel or a already established connection
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
			dlog(always, "ERROR SharedConnGet: .conn is nil aconnitem='%#v' provider='%s'", aconnitem, provider.Name)
			needNewConn = true
		}
	}
	if !needNewConn && aconnitem != nil && aconnitem.conn != nil {
		// we have a valid connection, use it
		if cfg.opt.BUG {
			dlog(cfg.opt.DebugConnPool, "SharedConnGet: got shared connection '%s' aconnitem='%#v'", provider.Name, aconnitem)
		}
		provider.ConnPool.ExtendConn(connitem)
		return aconnitem, nil
	}
	//provider.ConnPool.CloseConn(aconnitem, sharedCC) // close the nil connection item to reduce counter if needed and put nil back into sharedCC

	// no valid connection available, try to get one from the pool
	dlog(cfg.opt.DebugConnPool, "SharedConnGet: dead shared conn... try to get one from pool '%s' aconnitem='%#v'", provider.Name, aconnitem)
	newconnitem, err := provider.ConnPool.GetConn()
	if err != nil || newconnitem == nil || newconnitem.conn == nil {
		// unable to get a new connection, put nil back into sharedCC
		dlog(always, "ERROR SharedConnGet connect failed Provider '%s' err='%v'", provider.Name, err)
		sharedCC <- nil // put nil back into sharedCC
		return
	}
	connitem = newconnitem // use the new connection item
	provider.ConnPool.ExtendConn(connitem)
	return
} // end func SharedConnGet

// SharedConnReturn puts a connection back into the sharedCC channel.
// This is used to share the connection with other goroutines.
// It does not close the connection, just parks it back into the channel.
// This function is used to return a connection to the shared channel for reuse.
// It is used by the goroutines which are working on the same item.
// It is important to return the connection to the shared channel so that other goroutines can use it.
func SharedConnReturn(sharedCC chan *ConnItem, connitem *ConnItem, provider *Provider) {
	provider.ConnPool.ExtendConn(connitem) // SharedConnReturn
	sharedCC <- connitem                   // put the connection back into the channel to share it with other goroutines
} // end func SharedConnReturn

// ReturnSharedConnToPool returns a shared connection to the pool.
// This is used when the connection is no longer needed but still valid.
// It does not close the connection, just parks it back into the pool.
// This function is used to return a connection to the pool for reuse.
func ReturnSharedConnToPool(wid int, sharedCC chan *ConnItem, provider *Provider, connitem *ConnItem) {
	dlog(cfg.opt.DebugConnPool, "ReturnSharedConnToPool: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	if connitem == nil || connitem.conn == nil {
		provider.ConnPool.CloseConn(connitem, sharedCC) // close the connection to reduce the counter of open connections
		dlog(cfg.opt.DebugConnPool, "WARN ReturnSharedConnToPool: connitem is nil or conn.conn is nil! provider='%s'", provider.Name)
		return
	}
	provider.ConnPool.ParkConn(wid, connitem, "ReturnSharedConnToPool") // put the connection back into the pool
}

// ReturnSharedConnToPoolAndClose returns a shared connection to the pool and closes it.
// This is used when the connection is no longer needed or has expired.
func ReturnSharedConnToPoolAndClose(sharedCC chan *ConnItem, provider *Provider, connitem *ConnItem) {
	dlog(cfg.opt.DebugConnPool, "ReturnSharedConnToPoolAndClose: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	if connitem == nil || connitem.conn == nil {
		provider.ConnPool.CloseConn(connitem, sharedCC) // close the connection to reduce the counter of open connections
		dlog(cfg.opt.DebugConnPool, "WARN ReturnSharedConnToPoolAndClose: connitem is nil or conn.conn is nil! provider='%s'", provider.Name)
		return
	}
	provider.ConnPool.CloseConn(connitem, sharedCC) // close the connection and return it to the shared channel
}

func Speedmeter(byteSize int64, cp *ConnPool, cnt *Counter_uint64, workerWGconnReady *sync.WaitGroup) error {
	workerWGconnReady.Wait()

	if cfg.opt.PrintStats <= 0 {
		return nil
	}
	PrintStats := cfg.opt.PrintStats
	if PrintStats < 1 {
		PrintStats = 1
	}

	var name, group string
	var counter *Counter_uint64
	switch {
	case cp == nil && cnt == nil:
		name, group, counter = "Global", "Speedmeter", GCounter
	case cp != nil && cnt == nil:
		name, group, counter = cp.provider.Name, cp.provider.Group, cp.counter
	case cp == nil && cnt != nil:
		name, group, counter = "Global", "Speedmeter", cnt
	default:
		return fmt.Errorf("error in Speedmeter: supply cp or cnt or nothing to use global counter, but not both")
	}

	var totalTX, totalRX uint64
	ticker := time.NewTicker(time.Second * time.Duration(PrintStats))
	defer ticker.Stop()
	for range ticker.C {
		var tmpRX, tmpTX uint64
		globalmux.Lock()
		tmpRX, tmpTX = counter.GetReset("TMP_RXbytes"), counter.GetReset("TMP_TXbytes")
		totalRX += tmpRX
		totalTX += tmpTX
		globalmux.Unlock()

		// always print both RX and TX, even if one is zero (for consistency)
		rxSpeed, rxMbps := ConvertSpeed(int64(tmpRX), PrintStats)
		txSpeed, txMbps := ConvertSpeed(int64(tmpTX), PrintStats)
		dlPerc := int(float64(totalRX) / float64(byteSize) * 100)
		upPerc := int(float64(totalTX) / float64(byteSize) * 100)
		printSpeedTable(dlPerc, upPerc, totalRX, totalTX, byteSize, rxSpeed, txSpeed, rxMbps, txMbps, name, group)

	}
	return nil
}

func printSpeedTable(dlPerc, upPerc int, totalRX uint64, totalTX uint64, byteSize int64, rxSpeed int64, txSpeed int64, rxMbps float64, txMbps float64, provider string, group string) {
	// Clear, modern, minimalistic CLI style
	roooow := " │ %s │ %3d%% │  %5d MiB  │  %s%6d KiB/s  │  %6.1f Mbps  │  %-9s #%-6s \n"
	if txSpeed > 0 {
		log.Printf(roooow, "UL", upPerc, totalTX/1024/1024, "---> ", txSpeed, txMbps, provider, group)
	}
	if rxSpeed > 0 {
		log.Printf(roooow, "DL", dlPerc, totalRX/1024/1024, "<--- ", rxSpeed, rxMbps, provider, group)
	}
	//fmt.Println(footer)
}
