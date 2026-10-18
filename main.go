package main

/*
 * NZBrefreshX or NZBreX
 *
 *    Code based on:
 *     github.com/Tensai75/nzbrefresh#commit:cc2a8b7  (MIT License)
 *
 * Includes Code:
 *     github.com/go-while/NZBreX/yenc				  (MIT License)
 *	   github.com/go-while/go-loggedrwmutex           (MIT License)
 *     github.com/go-while/go-cpu-mem-profiler        (MIT License)
 *
 * Foreign Includes:
 *     github.com/Tensai75/nzbparser#commit:a1e0d80   (MIT License)
 *     github.com/Tensai75/cmpb#commit:16fb79f        (MIT License)
 *     github.com/fatih/color#commit:4c0661           (MIT License)
 *
 */

import (

	//"github.com/Tensai75/cmpb" // TODO:FIXME progressbars cfg.opt.Bar, segmentBar, upBar, dlBar
	//"github.com/fatih/color" // TODO:FIXME progressbars cfg.opt.Bar, segmentBar, upBar, dlBar

	"fmt"
	"os"

	prof "github.com/go-while/go-cpu-mem-profiler"
	"github.com/go-while/go-loggedrwmutex"

	"sync"
	"time"
)

var (
	appName    = "NZBreX"     // application name
	appVersion = "-"          // Github tag or built date
	Prof       *prof.Profiler // cpu/mem profiler
	cfg        *Config        // global config object
	//globalmux  sync.RWMutex    // global mutex for all routines to use
	globalmux       *loggedrwmutex.LoggedSyncRWMutex // debug mutex
	core_chan       chan struct{}                    // limits cpu usage
	async_core_chan chan struct{}                    // limits cpu usage
	memlim          *MemLimiter                      // limits number of objects in ram
	cache           *Cache                           // cache object
	cacheON         bool                             // will be true if cache is enabled
	GCounter        *Counter_uint64                  // a global counter

	// flags
	booted        time.Time // not a flag
	version       bool      // flag
	runProf       bool      // flag
	webProf       string    // flag
	nzbfile       string    // flag
	testmode      bool      // flag: used to test compilation
	testrapidyenc bool      // flag: used to test rapidyenc decoder
	proxy         bool      // flag: used to enable proxy server
)

func init() {
	loggedrwmutex.GlobalDebug = (appVersion == "debug")
	loggedrwmutex.DisableLogging = true
	globalmux = &loggedrwmutex.LoggedSyncRWMutex{Name: "GLOBALMUX"}
	//stopChan = make(chan struct{}, 1)
	setupSigusr1Dump()
	GCounter = NewCounter(10)
	cfg = &Config{opt: &CFG{}}
	booted = time.Now()
} // end func init

func main() {
	//colors := new(cmpb.BarColors)
	ParseFlags()

	wg := new(sync.WaitGroup)
	thisProcessor := &PROCESSOR{}
	go GoMutexStatus()
	if err := thisProcessor.NewProcessor(); err != nil {
		dlog(always, "ERROR NewProcessor: proxy err='%v'", err)
		os.Exit(1)
	}
	// -nzbdir is refused in ParseFlags until directory processing works (D3)
	if proxy {
		// boot proxy server: a session that only loads the providers and runs their pools
		s := thisProcessor.newBaseSession("PROXYSESSION")
		s.ready = make(chan struct{})
		ProxyParent = s // written before any listener goroutine starts
		go GoCliRxTxCounter()
		launchErr := make(chan error, 1)
		wg.Add(1) // waitSession
		go func(wg *sync.WaitGroup) {
			launchErr <- thisProcessor.LaunchSession(s, "", wg)
		}(wg)
		// start listening only once providerList is loaded: handlers read it without a
		// lock, and an early client saw no providers (plan 5.2)
		select {
		case <-s.ready:
		case err := <-launchErr:
			dlog(always, "ERROR NewProcessor Boot Proxy Server: err='%v'", err)
			os.Exit(1)
		}
		if err := StartProxyServers(cfg.opt); err != nil {
			dlog(always, "ERROR StartProxyServers: %v", err)
			os.Exit(1)
		}
		proxyShutdownOnSignal()
		dlog(cfg.opt.Debug, "main: wg.Wait()")
		wg.Wait()
	} else if webServerEnabled {
		// boot web server: bind first, so a busy port or a bad cert exits right away (plan 4.1)
		if err := StartWebServer(webServerAddr, thisProcessor); err != nil {
			dlog(always, "ERROR StartWebServer: err='%v'", err)
			os.Exit(1)
		}
		webShutdownOnSignal()
		// a base session loads the providers for the web server, like the proxy's
		s := thisProcessor.newBaseSession("WEBSESSION")
		// /api/stats lists its providers while no job runs
		webBaseSession.Store(s)
		wg.Add(1) // waitSession
		go func(wg *sync.WaitGroup) {
			if err := thisProcessor.LaunchSession(s, "", wg); err != nil {
				dlog(always, "ERROR NewProcessor Boot Web Server: err='%v'", err)
				os.Exit(1)
			}
		}(wg)
		dlog(cfg.opt.Debug, "main: wg.Wait()")
		wg.Wait()
	} else if cfg.opt.NzbDir == "" && nzbfile != "" {
		wg.Add(1) // waitSession
		go func(nzbfile string, wg *sync.WaitGroup) {
			dlog(cfg.opt.Debug, "pre:thisProcessor.LaunchSession: nzbfile='%s'", nzbfile)
			if err := thisProcessor.LaunchSession(nil, nzbfile, wg); err != nil {
				dlog(always, "ERROR NewProcessor.LaunchSession: nzbfile='%s' err='%v'", nzbfile, err)
				os.Exit(1)
			}
		}(nzbfile, wg)
		dlog(cfg.opt.Debug, "main: wg.Wait()")
		wg.Wait()
	}

	if runProf {
		dlog(always, "Prof stop capturing cpu profile")
		Prof.StopCPUProfile()
		time.Sleep(time.Second) // pProf needs some time to write the profile file
	}
	fmt.Printf("%s runtime: %.0f sec (booted: %s)\n", os.Args[0], time.Since(booted).Seconds(), booted.Format(time.RFC3339))
	/* TODO: print total processed segments, uploaded segments, downloaded segments, etc. */
	os.Exit(0)
} // end func main
