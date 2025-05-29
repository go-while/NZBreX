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

	//"github.com/Tensai75/cmpb"
	//"github.com/fatih/color"

	"log"
	"os"

	prof "github.com/go-while/go-cpu-mem-profiler"

	//"path/filepath"
	//"runtime"

	//"slices"
	//"strings"
	"sync"
	"time"
)

var (
	appName    = "NZBreX"
	appVersion = "-" // Github tag or built date
	Prof       *prof.Profiler
	cfg        *Config
	globalmux  sync.RWMutex
	stop_chan  chan struct{} // push a single 'struct{}{}' into this chan and all readers will re-push it and return itsef to quit
	core_chan  chan struct{} // limits cpu usage
	memlim     *MemLimiter
	cache      *Cache
	cacheON    bool
	GCounter   *Counter_uint64 // a global counter
	/*
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

	version  bool   // flag
	runProf  bool   // flag
	webProf  string // flag
	testproc bool   // flag
	nzbfile  string // flag
	//booted   time.Time // not a flag
)

func init() {
	stop_chan = make(chan struct{}, 1)
	setupSigusr1Dump()
	GCounter = NewCounter(10)
	cfg = &Config{opt: &CFG{}}
	//booted = time.Now()
} // end func init

func main() {
	//colors := new(cmpb.BarColors)
	ParseFlags()

	/*
		if cfg.opt.Bar && cfg.opt.Colors {
			colors.Post, colors.KeyDiv, colors.LBracket, colors.RBracket =
				color.HiCyanString, color.HiCyanString, color.HiCyanString, color.HiCyanString
			colors.Key = color.HiWhiteString
			colors.Msg, colors.Empty = color.HiYellowString, color.HiYellowString
			colors.Full = color.HiGreenString
			colors.Curr = color.GreenString
			colors.PreBar, colors.PostBar = color.HiYellowString, color.HiMagentaString
		}
	*/

	/*
		if cfg.opt.Bar {
			// segment check progressbar
			segmentBar = progressBars.NewBar("STAT", len(s.segmentList))
			segmentBar.SetPreBar(cmpb.CalcSteps)
			segmentBar.SetPostBar(cmpb.CalcTime)
			if cfg.opt.Colors {
				progressBars.SetColors(colors)
			}
			// start progressbar
			progressBars.Start()
			//progressBars.Stop("STAT", "done")
		}
	*/

	/* SESSION start
	// run the go routines
	waitDivider.Add(1)
	waitDividerDone.Add(1)
	workerWGconnEstablish.Add(1)
	GoBootWorkers(&waitDivider, &workerWGconnEstablish, &waitWorker, &waitPool, nzbfile.Bytes)

	if cfg.opt.Debug {
		log.Print("main: workerWGconnEstablish.Wait()")
	}
	workerWGconnEstablish.Wait()
	if cfg.opt.Debug {
		log.Print("main: workerWGconnEstablish.Wait() released: segmentCheckStartTime=now")
	}

	setTimerNow(&segmentCheckStartTime)
	// booting work divider
	go GoWorkDivider(&waitDivider, &waitDividerDone)
	if cfg.opt.Debug {
		log.Print("main: waitDividerDone.Wait()")
	}
	waitDividerDone.Wait()

	if cfg.opt.Debug {
		log.Print("main: waitDividerDone.Wait() released")
	}

	StopRoutines()

	if cfg.opt.Debug {
		log.Print("main: waitWorker.Wait()")
	}
	waitWorker.Wait()
	if cfg.opt.Debug {
		log.Print("main: waitWorker.Wait() released, waiting on waitPool.Wait()")
	}
	waitPool.Wait()

	SESSION END */

	/*
		if cfg.opt.Bar {
			progressBars.Wait()
		}
	*/

	/*
		if cfg.opt.Debug {
			log.Print("main: waitPool.Wait() released")
		}

		result, runtime_info := Results(preparationStartTime)

		YencMerge(nzbhashname, &result)

		log.Print(runtime_info + "\n> ###" + result + "\n\n> ###\n\n:end")
		//writeCsvFile()
	*/
	// SESSION ENDS HERE
	//log.Printf("// infinite wait!")
	//select {} // infinite wait!

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
		time.Sleep(time.Second)

	}

	log.Printf("NZBreX done!")
	//log.Printf("// infinite wait!")
	//select {} // infinite wait!

	if runProf {
		log.Printf("Prof stop capturing cpu profile")
		Prof.StopCPUProfile()
		//time.Sleep(time.Second)
	}
	os.Exit(0)
} // end func main
