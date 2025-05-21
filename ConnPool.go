package main

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"time"
)

type (
	ConnItem struct {
		conn   net.Conn
		srvtp  *textproto.Conn
		writer *bufio.Writer
	}
	ProviderConns struct {
		openConns  int
		mux        sync.Mutex
		pool       chan *ConnItem
		wait       []chan *ConnItem
		provider   *Provider
		rserver    string
		wants_auth bool
	}
) // end type

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

func (c *ProviderConns) connect(wid int, provider *Provider, retry uint32) (*ConnItem, error) {

	if retry > provider.MaxConnErrors {
		return nil, fmt.Errorf("ERROR connect MaxConnErrors '%s'", provider.Name)
	}

	var conn net.Conn
	var err error

	if !provider.SSL {
		conn, err = net.Dial(provider.TCPMode, c.rserver)
	} else {
		conf := &tls.Config{}
		if provider.SkipSslCheck {
			conf.InsecureSkipVerify = true
		}
		conn, err = tls.Dial(provider.TCPMode, c.rserver, conf)
	}

	if err != nil || conn == nil {
		if conn != nil {
			conn.Close()
		}
		if isNetworkUnreachable(err) {
			return nil, fmt.Errorf("ERROR connect Unreachable network '%s' @ '%s'", provider.Host, provider.Name)
		}
		log.Printf("error Dial rserver=%s wants_ssl=%t err='%v' retry in 3s", c.rserver, provider.SSL, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, provider, retry)
	}

	srvtp := textproto.NewConn(conn)

	code, msg, err := srvtp.ReadCodeLine(20)

	if code < 200 || code > 201 {
		if conn != nil {
			conn.Close()
		}
		log.Printf("ERROR connect code=%d msg='%s' err='%v' retry in 3s", code, msg, err)
		time.Sleep(3 * time.Second)
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
		log.Printf("rserver=%s AUTH1 FAILED err='%v' retry in 3s", c.rserver, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, provider, retry)
	}
	srvtp.StartResponse(id)
	code, _, err = srvtp.ReadCodeLine(381)
	srvtp.EndResponse(id)
	if err != nil || code != 381 {
		if conn != nil {
			conn.Close()
		}
		log.Printf("rserver=%s AUTH2 FAILED code != 381 err='%v' retry in 3s", c.rserver, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, provider, retry)
	}

	id, err = srvtp.Cmd("AUTHINFO PASS %s", provider.Password)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		log.Printf("rserver=%s AUTH3 FAILED err='%v' retry in 3s", c.rserver, err)
		time.Sleep(3 * time.Second)
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
		log.Printf("rserver=%s AUTH4 FAILED err='%v' retry in 3s", c.rserver, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, provider, retry)
	}

	return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err
} // end func connect

func (c *ProviderConns) GetConn(wid int, provider *Provider) (connitem *ConnItem, err error) {
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
		return
	}
	c.mux.Unlock()

	if cfg.opt.Debug {
		Counter.incr("WaitingGetConns")
	}
	select {
	case connitem = <-c.pool:
		if cfg.opt.Debug {
			Counter.decr("WaitingGetConns")
			Counter.incr("TOTAL_GetConns")
		}
	}
	return
} // end func GetConn

func (c *ProviderConns) ParkConn(provider *Provider, connitem *ConnItem) {
	c.pool <- connitem
	if cfg.opt.Debug {
		Counter.incr("TOTAL_ParkedConns")
		log.Printf("ParkConn '%s' inPool=%d", provider.Name, len(c.pool))
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
} // end func isNetworkUnreachable (written by AI: GPT-4o)
