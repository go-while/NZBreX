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
	"fmt"
	//"github.com/Tensai75/cmpb"
	//"github.com/fatih/color"
	"io"
	"log"
	"os"

	prof "github.com/go-while/go-cpu-mem-profiler"

	//"path/filepath"
	//"runtime"
	"runtime/pprof"
	//"slices"
	//"strings"
	"sync"
	"time"
)

var (
	appName    = "NZBreX"
	appVersion = "-" // Github tag or built date
	Prof       *prof.Profiler
	cfg        = &Config{opt: &CFG{}}
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

	version  bool      // flag
	runProf  bool      // flag
	webProf  string    // flag
	testproc bool      // flag
	nzbfile  string    // flag
	booted   time.Time // not a flag
)

func dumpGoroutines() {
	f, err := os.Create("goroutines.prof")
	if err != nil {
		fmt.Println("Could not create file:", err)
		return
	}
	defer f.Close()

	pprof.Lookup("goroutine").WriteTo(f, 2)
	fmt.Println("Goroutines dumped to goroutines.prof")
}

func init() {
	stop_chan = make(chan struct{}, 1)
	setupSigusr1Dump()
	GCounter = NewCounter(10)
	booted = time.Now()
} // end func init

func getUptime(what string) (uptime float64) {
	switch what {
	case "Seconds":
		uptime = time.Since(booted).Seconds()
	case "Minutes":
		uptime = time.Since(booted).Minutes()
	case "Hours":
		uptime = time.Since(booted).Hours()
	default:
		uptime = -1
	}
	return
} // end func getUptime

func main() {
	booted = time.Now()

	//colors := new(cmpb.BarColors)
	ParseFlags()

	if cfg.opt.Debug {
		log.Printf("loadedConfig flag.Parse cfg.opt='%#v'", cfg.opt)
	}

	cacheON = (cfg.opt.Cachedir != "" && cfg.opt.CRW > 0)

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

	// setup debug modes
	if cfg.opt.Debug || cfg.opt.BUG {
		if !cfg.opt.Debug {
			cfg.opt.Debug = true
		}
		if !cfg.opt.Verbose {
			cfg.opt.Verbose = true
		}
		if !cfg.opt.DebugCache {
			cfg.opt.DebugCache = true
		}
	} else {
		if cfg.opt.Discard {
			log.SetOutput(io.Discard) // DEBUG
		}
	} // end debugs

	if cfg.opt.Verbose {
		log.Printf("Settings: '%#v'", *cfg.opt)
	}

	// NEWSESSION starts here

	/* SESSION START
	// create log file: /path/nzb.log
	if cfg.opt.Log {
		logFileName := strings.TrimSuffix(filepath.Base(cfg.opt.NZBfilepath), filepath.Ext(filepath.Base(cfg.opt.NZBfilepath))) + ".log"
		f, err := os.Create(logFileName)
		if err != nil {
			log.Printf("unable to open log file: %v", err)
			os.Exit(1)
		}
		log.Printf("Writing log to: '%s'", logFileName)
		log.SetOutput(f) //DEBUG
	}




	preparationStartTime := time.Now()
	if cfg.opt.Debug {
		log.Printf("Loading NZB: '%s'", cfg.opt.NZBfilepath)
	}
	nzbfile, err := loadNzbFile(cfg.opt.NZBfilepath)
	if err != nil {
		log.Printf("unable to load NZB file '%s': %v'", cfg.opt.NZBfilepath, err)
		os.Exit(1)
	}
	if cfg.opt.Debug {
		log.Printf("nzbfile='%#v'", nzbfile)
		//os.Exit(1)
	}
	nzbhashname := SHA256str(filepath.Base(cfg.opt.NZBfilepath)) // fixme TODO processor/sessions
	if len(nzbfile.Files) <= 0 {
		log.Printf("error in NZB file '%s': nzbfile.Files=0'", cfg.opt.NZBfilepath)

	}

	// loop through all file tags within the NZB file
	for _, file := range nzbfile.Files {
		if cfg.opt.Debug {
			fmt.Printf(">> nzbfile file='%#v'\n\n", file)
		}
		// loop through all segment tags within each file tag
		if cfg.opt.Debug {
			for _, agroup := range file.Groups {
				if agroup != "" && !slices.Contains(nzbgroups, agroup) {
					nzbgroups = append(nzbgroups, agroup)
				}
			}
			log.Printf("NewsGroups: %v", nzbgroups)
		}
		// filling s.segmentList
		for _, segment := range file.Segments {
			if cfg.opt.BUG {
				log.Printf("reading nzb: Id='%s' file='%s'", segment.Id, file.Filename)
			}
			// if you add more variables to 'segmentChanItem struct': compiler always fails here!
			// we could supply neatly named vars but then we will forget one if updating the struct and app will crash...
			item := &segmentChanItem{
				&segment, &file,
				make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)),
				sync.RWMutex{}, nil, false, false, false, false, false, false, false, false, 0, SHA256str("<" + segment.Id + ">"), false, make(chan int, 1), make(chan bool, 1), 0, 0, 0, 0, 0, &nzbhashname} // fixme TODO processor/sessions
			s.segmentList = append(s.segmentList, item)
		}
	}

	mibsize := float64(nzbfile.Bytes) / 1024 / 1024
	artsize := mibsize / float64(len(s.segmentList)) * 1024

	log.Printf("%s [%s] loaded NZB: '%s' [%d/%d] ( %.02f MiB | ~%.0f KiB/segment )", appName, appVersion, cfg.opt.NZBfilepath, len(s.segmentList), nzbfile.TotalSegments, mibsize, artsize)
	SESSION END */

	if headers, err := ReadHeadersFromFile(cfg.opt.CleanHeadersFile); headers != nil {
		cleanHeader = headers
		log.Printf("Loaded %d headers from '%s' ==> cleanHeader='%#v'", len(headers), cfg.opt.CleanHeadersFile, cleanHeader)
	} else if err != nil {
		log.Printf("ERROR loading headers failed file='%s' err='%v'", cfg.opt.CleanHeadersFile, err)
		os.Exit(1)
	}

	// cosmetics
	//if cfg.opt.Bar {
	//	cfg.opt.Verbose = false
	//}
	// left-padding for log output
	//digStr := fmt.Sprintf("%d", len(s.segmentList))
	//D = fmt.Sprintf("%d", len(digStr))
	/*
		// load the provider list
		if err := loadProviderList(cfg.opt.ProvFile); err != nil {
			log.Printf("ERROR unable to load providerfile '%s' err='%v'", cfg.opt.ProvFile, err)
			os.Exit(1)
		}
		totalMaxConns := 0
		for _, provider := range providerList {
			totalMaxConns += provider.MaxConns
		}
	*/

	/*
		// set memory slots
		if cfg.opt.MemMax <= 0 && totalMaxConns > 0 {
			cfg.opt.MemMax = totalMaxConns * 2
		}
		memlim = NewMemLimiter(cfg.opt.MemMax)


		// limits crc32 and yenc processing
		if cfg.opt.YencCpu <= 0 {
			cfg.opt.YencCpu = runtime.NumCPU()
		}
		core_chan = make(chan struct{}, cfg.opt.YencCpu)
		for i := 1; i <= cfg.opt.YencCpu; i++ {
			core_chan <- struct{}{} // fill chan with empty structs to suck out and return
		}


		if cfg.opt.Debug {
			log.Printf("Loaded providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(providerList), time.Since(preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)
		}
	*/

	/*
		var waitWorker sync.WaitGroup
		var workerWGconnEstablish sync.WaitGroup
		var waitDivider sync.WaitGroup
		var waitDividerDone sync.WaitGroup
		var waitPool sync.WaitGroup
	*/
	// boot cache routines
	if cacheON {
		cache = NewCache(
			cfg.opt.Cachedir,
			cfg.opt.CRW,
			cfg.opt.CheckOnly,
			cfg.opt.MaxArtSize,
			cfg.opt.YencWrite,
			cfg.opt.DebugCache)

		if cache == nil {
			log.Printf("ERROR Cache failed... is nil!")
			os.Exit(1)
		}
		if !cache.MkSubDir("test") {
			os.Exit(1)
		}
	}

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
