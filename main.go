package main

/*
 * NZBrefreshX or NZBreX
 *
 *    Code based on:
 *     github.com/Tensai75/nzbrefresh#commit:cc2a8b7  (MIT License)
 *
 * Includes Code:
 *     github.com/go-while/go-cpu-mem-profiler        (MIT License)
 *
 * Foreign Includes:
 *     github.com/Tensai75/nzbparser#commit:a1e0d80   (MIT License)
 *     github.com/Tensai75/cmpb#commit:16fb79f        (MIT License)
 *     github.com/fatih/color#commit:4c0661           (MIT License)
 *     github.com/go-yenc/yenc ( gopkg.in/yenc.v0 )   (MIT License)
 *
 */

import (

	//"github.com/Tensai75/cmpb" // TODO:FIXME progressbars cfg.opt.Bar, segmentBar, upBar, dlBar
	//"github.com/fatih/color" // TODO:FIXME progressbars cfg.opt.Bar, segmentBar, upBar, dlBar

	"fmt"
	"log"
	"os"

	prof "github.com/go-while/go-cpu-mem-profiler"

	"sync"
	"time"
)

var (
	appName    = "NZBreX"      // application name
	appVersion = "-"           // Github tag or built date
	Prof       *prof.Profiler  // cpu/mem profiler
	cfg        *Config         // global config object
	globalmux  sync.RWMutex    // global mutex for all routines to use
	stop_chan  chan struct{}   // push a single 'struct{}{}' into this chan and all readers will re-push it and return itsef to quit
	core_chan  chan struct{}   // limits cpu usage
	memlim     *MemLimiter     // limits number of objects in ram
	cache      *Cache          // cache object
	cacheON    bool            // will be true if cache is enabled
	GCounter   *Counter_uint64 // a global counter

	/* // TODO:FIXME progressbars
	segmentBar   *cmpb.Bar
	upBar        *cmpb.Bar
	dlBar        *cmpb.Bar
	upBarStarted bool
	dlBarStarted bool
	BarMutex     sync.Mutex
	progressBars = cmpb.NewWithParam(&cmpb.Param{
		Interval:     5000 * time.Millisecond,
		Out:          color.Output,
		ScrollUp:     cmpb.AnsiScrollUp,
		PrePad:       1,
		KeyWidth:     8,
		MsgWidth:     8,
		PreBarWidth:  12,
		BarWidth:     42,
		PostBarWidth: 4,
		Post:         "...",
		KeyDiv:       ':',
		LBracket:     '[',
		RBracket:     ']',
		Empty:        '-',
		Full:         '=',
		Curr:         '>',
	})
	*/

	booted   time.Time // not a flag
	version  bool      // flag
	runProf  bool      // flag
	webProf  string    // flag
	nzbfile  string    // flag
	testmode bool      // flag: used to test compilation

)

func init() {
	stop_chan = make(chan struct{}, 1)
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

	if err := thisProcessor.NewProcessor(); err != nil {
		log.Printf("ERROR NewProcessor: err='%v'", err)
		os.Exit(1)
	} else {
		if cfg.opt.NzbDir != "" {
			thisProcessor.SetDirRefresh(cfg.opt.DirRefresh)
			log.Printf("// infinite wait!")
			select {} // infinite wait!
		}
	}

	if cfg.opt.NzbDir == "" && nzbfile != "" {
		wg.Add(1)
		go func(nzbfile string, wg *sync.WaitGroup) {
			log.Printf("pre:thisProcessor.LaunchSession: nzbfile='%s'", nzbfile)
			if err := thisProcessor.LaunchSession(nil, nzbfile, wg); err != nil {
				log.Printf("ERROR NewProcessor.LaunchSession: nzbfile='%s' err='%v'", nzbfile, err)
				os.Exit(1)
			}
		}(nzbfile, wg)
		log.Printf("main: wg.Wait()")
		wg.Wait()
	}

	if runProf {
		log.Printf("Prof stop capturing cpu profile")
		Prof.StopCPUProfile()
		time.Sleep(time.Second)
	}
	fmt.Printf("%s runtime: %.0f sec (booted: %s)\n", os.Args[0], time.Since(booted).Seconds(), booted.Format(time.RFC3339))
	/* TODO: print total processed segments, uploaded segments, downloaded segments, etc. */
	os.Exit(0)
} // end func main
