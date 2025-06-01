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

	"github.com/go-while/go-loggedrwmutex"
)

// TODO FIXME temporary debug flags
const WatchOpenConns = false

const (
	// if conn was parked more than DefaultConnExpire:
	// try read from conn which takes some µs
	// no idea if this actually works! REVIEW
	DefaultConnExpire = 20 * time.Second

	// if conn was parked more than DefaultConnCloser:
	// we close it and get a new one...
	// some provider close idle connections early without telling you...
	DefaultConnCloser = 30 * time.Second
)

var (
	//noDeadLine   time.Time                 // used as zero value!
	//readDeadConn = time.Unix(1, 0)         // no syscall needed. value is 1 second after "0" (1970-01-01 00:00:01 UTC)
	ConnPools = make(map[int]*ConnPool) // used in KillConnPool
	PoolsLock sync.RWMutex
)

// holds an active connection to share around
type ConnItem struct {
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
}

func NewConnPool(provider *Provider, workerWGconnEstablish *sync.WaitGroup) {
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
		pool:    make(chan *ConnItem, provider.MaxConns),
		poolmap: make(map[uint64]*ConnItem, provider.MaxConns), // use map
		rserver: rserver, wants_auth: wants_auth,
		provider: provider,
		mux:      &loggedrwmutex.LoggedSyncRWMutex{Name: fmt.Sprintf("ConnPool-%s", provider.Name)},
	}
	PoolsLock.Lock()
	ConnPools[provider.id] = provider.ConnPool
	PoolsLock.Unlock()
	go provider.ConnPool.WatchOpenConnsThread(workerWGconnEstablish)
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
func (c *ConnPool) connect(retry int) (connitem *ConnItem, err error) {

	if c.provider.MaxConnErrors > 0 && retry > c.provider.MaxConnErrors {
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
		dlog(cfg.opt.DebugConnPool, "ConnPool raw tcp.Dial took='%v' '%s' retried=%d", time.Since(start), c.provider.Name, retry)

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

		dlog(cfg.opt.DebugConnPool, "ConnPool raw tls.Dial took='%v' '%s' retried=%d", time.Since(start), c.provider.Name, retry)
	} // end switch

	if err != nil {
		defer conn.Close()
		if isNetworkUnreachable(err) {
			return nil, fmt.Errorf("error connect Unreachable network! '%s' @ '%s' err='%v'", c.provider.Host, c.provider.Name, err)
		}
		dlog(always, "ERROR connect Dial '%s' retry in %.0fs wants_ssl=%t err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), c.provider.SSL, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	srvtp := textproto.NewConn(conn)

	code, msg, err := srvtp.ReadCodeLine(20)

	if code < 200 || code > 201 {
		defer conn.Close()
		dlog(always, "ERROR connect welcome '%s' retry in %.0fs code=%d msg='%s' err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, msg, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	if !c.wants_auth {
		return &ConnItem{connid: c.newconnid(), created: time.Now(), srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
	}

	// send auth sequence
	id, err := srvtp.Cmd("AUTHINFO USER %s", c.provider.Username)
	if err != nil {
		defer conn.Close()
		dlog(always, "ERROR AUTH#1 Cmd(AUTHINFO USER ...) '%s' retry in %.0fs err='%v' ", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(381)
	srvtp.EndResponse(id)
	if err != nil {
		defer conn.Close()
		dlog(always, "ERROR AUTH#2 ReadCodeLine(381) step#2 '%s' retry in %.0fs code=%d err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}

	id, err = srvtp.Cmd("AUTHINFO PASS %s", c.provider.Password)
	if err != nil {
		defer conn.Close()
		dlog(always, "ERROR AUTH#3 Cmd(AUTHINFO PASS ...) '%s' retry in %.0fs err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(281)
	srvtp.EndResponse(id)
	if err != nil {
		defer conn.Close()
		dlog(always, "ERROR AUTH#4 ReadCodeLine(281) '%s' retry in %.0fs code=%d err='%v'", c.provider.Name, DefaultConnectErrSleep.Seconds(), code, err)
		time.Sleep(DefaultConnectErrSleep)
		retry++
		return c.connect(retry)
	}
	newConnItem := &ConnItem{connid: c.newconnid(), created: time.Now(), srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}
	dlog(cfg.opt.DebugConnPool, "ConnPool AUTH took=(%d ms) '%s' err='%v' newConnItem='%#v'", time.Since(start).Milliseconds(), c.provider.Name, err, newConnItem)
	return newConnItem, err
} // end func connect

func (c *ConnPool) NewConn() (connitem *ConnItem, err error, retry bool) {
	// try to open a new connection
	c.mux.Lock() // mutex c459
	if c.openConns == c.provider.MaxConns {
		// another routine was faster...
		dlog(cfg.opt.DebugConnPool, "ConnPool NewConn: another routine was faster! openConns=%d provider.MaxConns=%d '%s'", c.openConns, c.provider.MaxConns, c.provider.Name)
		c.mux.Unlock() // mutex c459
		err = fmt.Errorf("retry getConn, not newConn! all conns are already established")
		retry = true
		return
	}
	if c.openConns < c.provider.MaxConns {
		dlog(cfg.opt.DebugConnPool, "ConnPool NewConn: connect to '%s' openConns=%d provider.MaxConns=%d", c.provider.Name, c.openConns, c.provider.MaxConns)
		c.openConns++
		openConnsBefore := c.openConns
		c.mux.Unlock() // mutex c459
		start := time.Now()
		connitem, err = c.connect(0)
		if err != nil || connitem == nil || connitem.conn == nil {
			c.mux.Lock() // mutex c460
			c.openConns--
			dlog(always, "ERROR in ConnPool NewConn: connect failed '%s' openConns=%d connitem='%v' err='%v'", c.provider.Name, c.openConns, connitem, err)
			c.mux.Unlock()         // mutex c460
			return nil, err, false // connitem as nil and the error, false as to not retry with getConn
		}
		// we have a new connection!
		GCounter.Incr("TOTAL_NewConns")

		if cfg.opt.DebugConnPool {
			c.mux.RLock() // mutex c461
			dlog(always, "ConnPool NewConn: got new connid=%d '%s' openConns after=%d/%d before=%d connectTook=(%d ms) err='%v", connitem.connid, c.provider.Name, c.openConns, c.provider.MaxConns, openConnsBefore, time.Since(start).Milliseconds(), err)
			c.mux.RUnlock() // mutex c461
		}
		return // established new connection and returns connitem
	}
	c.mux.Unlock() // mutex c459
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

	endlessLoop := 10 // *10 ms wait between
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

			if connitem.parktime.Add(DefaultConnCloser).Before(time.Now()) {
				// but this conn is old and could be already closed from remote
				dlog(cfg.opt.DebugConnPool, "INFO ConnPool GetConn: conn old age=(%v) '%s' ... continue", time.Since(connitem.parktime), c.provider.Name)
				c.CloseConn(connitem, nil)
				continue getConnFromPool // until chan rans empty
			}
			if connitem.parktime.Add(DefaultConnExpire).Before(time.Now()) {
				/*
						buf := make([]byte, 1)
						// some provider have short timeout values.
						// try reading from conn. check takes some µs
						connitem.conn.SetReadDeadline(readDeadConn)
						if readBytes, rerr := connitem.conn.Read(buf); isNetConnClosedErr(rerr) || readBytes > 0 {
							dlog( "INFO ConnPool GetConn: dead idle '%s' readBytes=(%d != 0?) err='%v' ... continue", c.provider.Name, readBytes, rerr)
							go c.CloseConn(connitem, nil)
							continue getConnFromPool // until chan rans empty
						}
						connitem.conn.SetReadDeadline(noDeadLine)
						buf = nil
					dlog( "INFO ConnPool GetConn: got long idle=(%d ms) '%s', passed Read test", time.Since(connitem.parktime).Milliseconds(), c.provider.Name)
				*/
				dlog(cfg.opt.DebugConnPool, "INFO ConnPool GetConn: got long idle=(%d ms) '%s', close and get NewConn", time.Since(connitem.parktime).Milliseconds(), c.provider.Name)
				c.CloseConn(connitem, nil)
				connitem, err, _ = c.NewConn()
				if connitem == nil || err != nil {
					continue getConnFromPool
				}
			}

			connitem.parktime = time.Time{} // set parktime to zero value

			if cfg.opt.DebugConnPool {
				GCounter.Incr("TOTAL_GetConns")
				dlog(always, "GetConn got one from pool '%s' took='%v'", c.provider.Name, time.Since(start))
			}
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

				if cfg.opt.DebugConnPool {
					GCounter.Incr("TOTAL_GetConns")
					dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: released, got connid=%d age=(%v) %s: waited='%v'", connitem.connid, time.Since(connitem.parktime), c.provider.Name, time.Since(waiting))
				}
				return
			}
			dlog(cfg.opt.DebugConnPool, "ConnPool GetConn: try to open a new connection '%s' openConns=%d provider.MaxConns=%d", c.provider.Name, c.openConns, c.provider.MaxConns)
			c.mux.RUnlock() // mutex c458

			// try to open a new connection
			newconnitem, aerr, retry := c.NewConn()
			if err != nil {
				dlog(always, "ERROR in ConnPool GetConn: NewConn failed '%s' connitem='%v' err='%v'", c.provider.Name, connitem, err)
				if endlessLoop <= 0 || !retry {
					dlog(always, "ERROR in ConnPool GetConn: endlessLoop reached! wid=%d provider='%s' err='%v'", 0, c.provider.Name, err)
					return nil, fmt.Errorf("endlessLoop reached in ConnPool GetConn: wid=%d provider='%s' err='%v'", 0, c.provider.Name, err)
				}
				dlog(always, "ERROR in ConnPool GetConn: retrying to get a conn '%s' err='%v'", c.provider.Name, err)
				endlessLoop--
				time.Sleep(time.Millisecond * 10) // wait a bit before retrying
				continue getConnFromPool          // until chan rans empty
			}
			// we have a new connection!
			connitem, err = newconnitem, aerr
			return
		} // end select
	} // end for
	// vet says: unreachable code
	//return nil, fmt.Errorf("error in ConnPool GetConn: uncatched return! wid=%d", wid)
} // end func GetConn

func (c *ConnPool) newconnid() (newid uint64) {
	c.mux.Lock()
	c.nextconnId++
	newid = c.nextconnId
	c.mux.Unlock()
	return
}

func (c *ConnPool) ParkConn(wid int, connitem *ConnItem, src string) {

	connitem.parktime = time.Now()
	connitem.parkedCnt += 1
	select {
	case c.pool <- connitem:
		// parked in pool
		//if cfg.opt.DebugConnPool && !silent {
		GCounter.Incr("TOTAL_ParkedConns")
		if cfg.opt.BUG && cfg.opt.DebugConnPool {
			c.mux.RLock()
			dlog(cfg.opt.DebugConnPool, "ParkedConn connid=%d (wid=%d) '%s' inPool=%d cap=%d openConns=%d src='%s'", connitem.connid, wid, c.provider.Name, len(c.pool), cap(c.pool), c.openConns, src)
			c.mux.RUnlock()
		}
	default:
		// pool chan is full!!
		dlog(always, "ERROR in ConnPool ParkConn: connid=%d (wid=%d) chan is full! provider '%s'. forceClose conn src='%s'", connitem.connid, wid, c.provider.Name, src)
		c.CloseConn(connitem, nil)
	}
} // end func ParkConn

func (c *ConnPool) CloseConn(connitem *ConnItem, sharedConnChan chan *ConnItem) {

	if cfg.opt.DebugConnPool {
		GCounter.Incr("TOTAL_DisConns")
	}
	if connitem.conn != nil {
		connitem.conn.Close()
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

func (c *ConnPool) GetStats() (openconns int, idle int) {
	c.mux.RLock()
	openconns, idle = c.openConns, len(c.pool)
	c.mux.RUnlock()
	return
}

func (c *ConnPool) WatchOpenConnsThread(workerWGconnEstablish *sync.WaitGroup) {
	/*
		if !WatchOpenConns {
			return
		}
	*/
	// this is a thread which watches the open connections and closes them if they are idle for too long
	// it will close the connection if it is idle for more than DefaultConnCloser
	workerWGconnEstablish.Wait()
	dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread started for '%s'", c.provider.Name)
	defer dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread for '%s' defer returned", c.provider.Name)
	tempItems := []*ConnItem{} // temporary slice to hold ConnItems
forever:
	for {
		time.Sleep(time.Millisecond * 5000) // check every N milliseconds
		c.provider.mux.Lock()
		if c.provider.ConnPool != nil {
			c.provider.mux.PrintStatus(cfg.opt.Debug)          // prints mutex status for the provider
			c.provider.ConnPool.mux.PrintStatus(cfg.opt.Debug) // prints mutex status for the providers connpool
		}
		c.provider.mux.Unlock()
		//globalmux.PrintStatus(cfg.opt.Debug)               // prints global mutex status for the whole app
		//memlim.mux.PrintStatus(cfg.opt.Debug)              // prints memory limit mutex status
		// capture this moment, might by stale instantly
		c.mux.RLock()
		openConns := c.openConns
		poolConns := len(c.pool)
		c.mux.RUnlock()

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
		if openConns == c.provider.MaxConns && len(c.pool) == 0 {
			// all conns are possibly in use or in shared channels
			//dlog( "WatchOpenConnsThread: all %d conns are possibly in shared chan for '%s' ... continue", openConns, c.provider.Name)
			continue forever
		} else {
			dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: openConns=%d idle=%d '%s'", openConns, poolConns, c.provider.Name)
		}
		// check all connections in the pool

		var alive, checked uint64
		start := time.Now()
		tA := time.After(250 * time.Microsecond)
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

			case <-stop_chan:
				stop_chan <- struct{}{} // signal to stop any other listeners
				dlog(cfg.opt.DebugConnPool, "WatchOpenConnsThread: stopped for '%s'", c.provider.Name)
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
		continue forever // continue the forever loop to check again
	} // end forever
} // end func WatchOpenConnsThread

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

	return
} // end func SharedConnGet

func SharedConnReturn(sharedCC chan *ConnItem, connitem *ConnItem) {
	sharedCC <- connitem // put the connection back into the channel to share it with other goroutines
} // end func SharedConnReturn

// ReturnSharedConnToPool returns a shared connection to the pool.
// This is used when the connection is no longer needed but still valid.
// It does not close the connection, just parks it back into the pool.
// This function is used to return a connection to the pool for reuse.
func ReturnSharedConnToPool(wid int, sharedCC chan *ConnItem, provider *Provider, connitem *ConnItem) {
	if cfg.opt.DebugConnPool {
		dlog(cfg.opt.DebugConnPool, "ReturnSharedConnToPool: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	}
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
	if cfg.opt.DebugConnPool {
		dlog(cfg.opt.DebugConnPool, "ReturnSharedConnToPoolAndClose: returning connitem='%#v' to pool '%s'", connitem, provider.Name)
	}
	if connitem == nil || connitem.conn == nil {
		provider.ConnPool.CloseConn(connitem, sharedCC) // close the connection to reduce the counter of open connections
		dlog(cfg.opt.DebugConnPool, "WARN ReturnSharedConnToPoolAndClose: connitem is nil or conn.conn is nil! provider='%s'", provider.Name)
		return
	}
	provider.ConnPool.CloseConn(connitem, sharedCC) // close the connection and return it to the shared channel
}
