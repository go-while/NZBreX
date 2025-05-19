package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"time"
)

type (
	ConnItem struct {
		conn      net.Conn
		srvtp     *textproto.Conn
		writer *bufio.Writer
	}
	ProviderConns struct {
		openConns int
		mux       sync.Mutex
		pool      chan *ConnItem
		wait      []chan *ConnItem
		rserver   string
		insecure  bool
	}
) // end type

func NewConnPool(provider *Provider) {
	provider.mux.Lock()         // NewConnPool mutex #d028
	defer provider.mux.Unlock() // NewConnPool mutex #d028

	srvuri := ""
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

	if provider.SSL {
		switch provider.TCPMode {
		case "tcp4":
			srvuri = "ssl4"
		case "tcp6":
			srvuri = "ssl6"
		case "tcp":
			srvuri = "ssl"
		default:
			srvuri = "ssl"
		}
	} else {
		switch provider.TCPMode {
		case "tcp4":
			srvuri = "tcp4"
		case "tcp6":
			srvuri = "tcp6"
		case "tcp":
			srvuri = "tcp"
		default:
			srvuri = "tcp"
		}
	}
	if provider.Username != "" && provider.Password != "" {
		srvuri = fmt.Sprintf("%s://%s:%s@%s:%d", srvuri, provider.Username, provider.Password, provider.Host, provider.Port)
	} else {
		srvuri = fmt.Sprintf("%s://%s:%d", srvuri, provider.Host, provider.Port)
	}

	provider.Conns = &ProviderConns{
		pool:    make(chan *ConnItem, provider.MaxConns),
		rserver: srvuri, insecure: provider.SkipSslCheck,
	}

} // end func NewConnPool

func (c *ProviderConns) connect(wid int, rserver string, insecure bool, provider *Provider, retry uint32) (*ConnItem, error) {
	wants_ssl, wants_auth, user, pass, tcp_mode := false, false, "", "", "tcp"

	if retry > provider.MaxConnErrors {
		return nil, fmt.Errorf("ERROR connect MaxConnErrors '%s'", provider.Name)
	}
	if strings.HasPrefix(rserver, "tcp://") {
		rserver = strings.Replace(rserver, "tcp://", "", 1)
	} else if strings.HasPrefix(rserver, "tcp4://") {
		tcp_mode = "tcp4"
		rserver = strings.Replace(rserver, "tcp4://", "", 1)
	} else if strings.HasPrefix(rserver, "tcp6://") {
		tcp_mode = "tcp6"
		rserver = strings.Replace(rserver, "tcp6://", "", 1)
	} else if strings.HasPrefix(rserver, "ssl://") {
		rserver = strings.Replace(rserver, "ssl://", "", 1)
		wants_ssl = true
	} else if strings.HasPrefix(rserver, "ssl4://") {
		tcp_mode = "tcp4"
		rserver = strings.Replace(rserver, "ssl4://", "", 1)
		wants_ssl = true
	} else if strings.HasPrefix(rserver, "ssl6://") {
		tcp_mode = "tcp4"
		rserver = strings.Replace(rserver, "ssl6://", "", 1)
		wants_ssl = true
		tcp_mode = "tcp6"
	}

	if strings.Contains(rserver, "@") {
		// [tcp(4|6)|ssl(4|6)]://user:pass@host:port ==> [0]=user:pass [1]=host:port
		adata0 := strings.Split(rserver, "@")[0]
		rserver = strings.Split(rserver, "@")[1]
		authdata := strings.Split(adata0, ":")

		if len(authdata) == 2 {
			user = authdata[0]
			pass = authdata[1]
			if user != "" && pass != "" {
				wants_auth = true
			}

		} else {
			fmt.Print("ERROR connect rserver authdata format\n")
			os.Exit(1)
		}
	}

	var conn net.Conn
	var err error
	if !wants_ssl {
		conn, err = net.Dial(tcp_mode, rserver)
	} else {
		conf := &tls.Config{}
		if insecure {
			conf.InsecureSkipVerify = true
		}
		conn, err = tls.Dial(tcp_mode, rserver, conf)
	}

	if err != nil || conn == nil {
		log.Printf("error Dial rserver=%s wants_ssl=%t err='%v' retry in 3s", rserver, wants_ssl, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, rserver, insecure, provider, retry)
	}

	srvtp := textproto.NewConn(conn)

	code, msg, err := srvtp.ReadCodeLine(20)

	if code < 200 || code > 201 {
		log.Printf("ERROR connect code=%d msg='%s' err='%v' retry in 3s", code, msg, err)
		time.Sleep(3 * time.Second)
		retry++
		return c.connect(wid, rserver, insecure, provider, retry)
	}

	switch wants_auth {

	case false:
		return &ConnItem{srvtp: srvtp, conn: conn, writer: bufio.NewWriter(conn)}, err

	case true:
		id, err := srvtp.Cmd("AUTHINFO USER %s", user)
		if err != nil {
			log.Printf("rserver=%s AUTH1 FAILED err='%v' retry in 3s", rserver, err)
			time.Sleep(3 * time.Second)
			retry++
			return c.connect(wid, rserver, insecure, provider, retry)
		}
		srvtp.StartResponse(id)
		code, _, err = srvtp.ReadCodeLine(381)
		srvtp.EndResponse(id)
		if err != nil || code != 381 {

			log.Printf("rserver=%s AUTH2 FAILED code != 381 err='%v' retry in 3s", rserver, err)
			time.Sleep(3 * time.Second)
			retry++
			return c.connect(wid, rserver, insecure, provider, retry)

		} else {

			id, err := srvtp.Cmd("AUTHINFO PASS %s", pass)
			if err != nil {
				log.Printf("rserver=%s AUTH3 FAILED err='%v' retry in 3s", rserver, err)
				time.Sleep(3 * time.Second)
				retry++
				return c.connect(wid, rserver, insecure, provider, retry)
			}
			srvtp.StartResponse(id)
			code, _, err = srvtp.ReadCodeLine(281)
			srvtp.EndResponse(id)
			if err != nil {
				log.Printf("rserver=%s AUTH4 FAILED err='%v' retry in 3s", rserver, err)
				time.Sleep(3 * time.Second)
				retry++
				return c.connect(wid, rserver, insecure, provider, retry)
			}
		}
	} // end switch wants_auth
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

		connitem, err = c.connect(wid, c.rserver, c.insecure, provider, 0)
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
	connitem.conn.Close()
	c.mux.Lock()
	c.openConns--
	if cfg.opt.Debug {
		log.Printf("DisConn '%s' inPool=%d open=%d", provider.Name, len(c.pool), c.openConns)
	}
	c.mux.Unlock()
} // end func CloseConn
