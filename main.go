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

	"github.com/go-while/NZBreX/rapidyenc"
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
	booted   time.Time // not a flag
	version  bool      // flag
	runProf  bool      // flag
	webProf  string    // flag
	nzbfile  string    // flag
	testmode bool      // flag: used to test compilation
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

	// test rapidyenc decoder
	decoder := rapidyenc.AcquireDecoder()
	rapidyenc.ReleaseDecoder(decoder)
	decoder = nil // release the decoder
	dlog(always, "rapidyenc decoder initialized")
} // end func init

func main() {
	//colors := new(cmpb.BarColors)
	ParseFlags()
	wg := new(sync.WaitGroup)
	thisProcessor := &PROCESSOR{}
	go GoMutexStatus()

	if err := thisProcessor.NewProcessor(); err != nil {
		dlog(always, "ERROR NewProcessor: err='%v'", err)
		os.Exit(1)
	} else {
		if cfg.opt.NzbDir != "" {
			thisProcessor.SetDirRefresh(cfg.opt.DirRefresh)
			dlog(cfg.opt.Debug, "// infinite wait!")
			select {} // infinite wait!
		}
	}

	if cfg.opt.NzbDir == "" && nzbfile != "" {
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
