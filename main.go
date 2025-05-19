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
	"flag"
	"fmt"
	"github.com/Tensai75/cmpb"
	"github.com/fatih/color"
	"github.com/go-while/go-cpu-mem-profiler"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	Prof         *prof.Profiler
	cfg          = &Config{opt: &CFG{}}
	appName      = "NZBreX"
	appVersion   = "-"       // Github tag or built date
	nzbgroups    []string    // informative
	providerList []*Provider // the parsed provider list structure

	globalmux         sync.RWMutex
	stop_chan         chan struct{} // push a single 'struct{}{}' into this chan and all readers will re-push it and return itsef to quit
	memlim            *MemLimiter
	cache             *Cache
	cacheON           bool
	segmentList       []*segmentChanItem
	segmentChansCheck map[string]chan *segmentChanItem
	segmentChansDowns map[string]chan *segmentChanItem
	segmentChansReups map[string]chan *segmentChanItem
	memDL map[string]chan *segmentChanItem // with -checkfirst queues items here
	memUP map[string]chan *segmentChanItem // TODO: process uploads after downloads (possible only with cacheON)
	Counter               *Counter_uint64
	postProviders         int    // counts Providers with IHAVE/POST/TAKETHIS capability
	segmentCheckStartTime time.Time
	segmentCheckEndTime   time.Time
	segmentCheckTook      time.Duration

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

	//fileStat     = make(filesStatistic)
	//fileStatLock sync.Mutex
	D = "3" // prints stats numbers zero (0003) padded or whitespace padded (  3). default to: 000
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
	// Set up signal handler
	go func() {
		// 'kill -SIGUSR1 $(pidof nzbrex)' to dump running goroutines
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGUSR1)
		<-sigs
		dumpGoroutines()
	}()
	Counter = NewCounter()
	Prof = prof.NewProf()
} // end func init

func main() {
	colors := new(cmpb.BarColors)
	var err error
	var version bool
	var runProf bool
	var testproc bool

	flag.BoolVar(&version, "version", false, "prints app version")
	flag.BoolVar(&testproc, "testproc", false, "testing watchdir processor code")
	// essentials
	flag.StringVar(&cfg.opt.NZBfilepath, "nzb", "test.nzb", "/path/file.nzb")
	flag.StringVar(&cfg.opt.ProvFile, "provider", "provider.json", "/path/provider.json")
	flag.BoolVar(&cfg.opt.CheckOnly, "checkonly", false, "[true|false] check online status only: no downs/reups (default: false)")
	flag.BoolVar(&cfg.opt.CheckFirst, "checkfirst", false, "[true|false] if false: starts downs/reups asap as segments are checked (default: false)")
	flag.BoolVar(&cfg.opt.Verify, "verify", false, "[true|false] waits and tries to verify/recheck all reups (default: false) !not implemented: TODO!")
	// cache and mem
	flag.IntVar(&cfg.opt.MemMax, "mem", 0, "limit memory usage to N segments in RAM ( 0 defaults to number of total provider connections*2 or what you set but usually there is no need for more. if your 'MEM' is full: your upload is just slow. giving more mem will NOT help!)")
	flag.StringVar(&cfg.opt.Cachedir, "cd", "", "/path/to/cache/dir")
	flag.BoolVar(&cfg.opt.CheckCacheOnBoot, "cc", false, "[true|false] checks nzb vs cache on boot (default: false)")
	flag.IntVar(&cfg.opt.CRW, "crw", 100, "sets number of Cache Reader and Writer routines to equal amount")
	// header and yenc
	flag.BoolVar(&cfg.opt.CleanHeaders, "cleanhdr", true, "[true|false] removes unwanted headers. only change this if you know why! (default: true) ")
	flag.BoolVar(&cfg.opt.YencCRC, "crc32", false, "[true|false] checks crc32 of articles on the fly while downloading (default: false)")
	// debug output flags
	flag.BoolVar(&runProf, "prof", false, "starts cpu+mem profiler: waits 20sec and runs 120sec")
	flag.BoolVar(&cfg.opt.Verbose, "verbose", true, "[true|false] a little more output than nothing (default: false)")
	flag.BoolVar(&cfg.opt.Discard, "discard", false, "[true|false] reduce console output to minimum (default: false)")
	flag.Int64Var(&cfg.opt.LogPrintEvery, "print", 5, "prints stats every N seconds. 0 is spammy and -1 disables output. a very high number will print only once it is finished")
	flag.BoolVar(&cfg.opt.Log, "log", false, "[true|false] logs to file (default: false)")
	flag.BoolVar(&cfg.opt.BUG, "bug", false, "[true|false] full debug (default: false)")
	flag.BoolVar(&cfg.opt.Debug, "debug", false, "[true|false] part debug (default: false)")
	flag.BoolVar(&cfg.opt.DebugCache, "debugcache", false, "[true|false] (default: false)")
	// rate limiter
	flag.IntVar(&cfg.opt.SloMoC, "slomoc", 0, "SloMo'C' limiter sleeps N milliseconds before checking")
	flag.IntVar(&cfg.opt.SloMoD, "slomod", 0, "SloMo'D' limiter sleeps N milliseconds before downloading")
	flag.IntVar(&cfg.opt.SloMoU, "slomou", 0, "SloMo'U' limiter sleeps N milliseconds before uploading")
	// no need to change this
	flag.IntVar(&cfg.opt.MaxArtSize, "maxartsize", 1*1024*1024, "limits article size to 1M (mostly articles have ~700K only)")

	// cosmetics: segmentBar needs fixing: only when everything else works!
	//flag.BoolVar(&cfg.opt.Bar, "bar", false, "show progress bars")  // FIXME TODO
	//flag.BoolVar(&cfg.opt.Colors, "colors", false, "adds colors to s")  // FIXME TODO
	//flag.StringVar(&configfile, "configfile", "config.json", "use this config file") // FIXME TODO
	flag.Parse()
	if version {
		fmt.Printf("%s version: [%s]\n", appName, appVersion)
		os.Exit(0)
	}
	if runProf {
		go Prof.PprofWeb("[::]:61234")
		log.Printf("Started PprofWeb @ Port :61234")
		go func() {
			time.Sleep(time.Second * 20)
			log.Printf("Prof start capturing cpu/mem profiles")
			if _, err := Prof.StartCPUProfile(); err != nil {
				log.Printf("ERROR Prof.StartCPUProfile err='%v'", err)
				return
			}
			time.Sleep(time.Second * 120)
			Prof.StopCPUProfile()
			log.Printf("Prof stop capturing cpu/mem profiles")
		}()
		go func() {
			if err := Prof.StartMemProfile(120*time.Second, 20*time.Second); err != nil {
				log.Printf("ERROR Prof.StartMemProfile err='%v'", err)
			}
		}()
	} // end bootProf

	// experimental test of processor/sessions

	if testproc {
		processor := &PROCESSOR{}
		nzbdir := "nzbs"
		var refreshEvery int64 = 15 // seconds
		if DirExists(nzbdir) {
			if err := processor.NewProcessor(nzbdir, refreshEvery); err != nil {
				log.Printf("Error NewProcessor err='%v'", err)
				os.Exit(1)
			}
			time.Sleep(3 * time.Second)
			log.Printf("that's it! you hit an infinite wait! nothing more will happen!")
			log.Printf("put new files in the folder and see them coming up...")
			log.Printf("connecting this to the rest of the source ... ")
			select {} // infinite wait!
		} else {
			log.Printf("watchDir not found: '%s'", nzbdir)
		}
	}


	if cfg.opt.Debug {
		log.Printf("loadedConfig flag.Parse cfg.opt='%#v'", cfg.opt)
	}

	cacheON = (cfg.opt.Cachedir != "" && cfg.opt.CRW > 0)

	if cfg.opt.Bar && cfg.opt.Colors {
		colors.Post, colors.KeyDiv, colors.LBracket, colors.RBracket =
			color.HiCyanString, color.HiCyanString, color.HiCyanString, color.HiCyanString
		colors.Key = color.HiWhiteString
		colors.Msg, colors.Empty = color.HiYellowString, color.HiYellowString
		colors.Full = color.HiGreenString
		colors.Curr = color.GreenString
		colors.PreBar, colors.PostBar = color.HiYellowString, color.HiMagentaString
	}

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
		if cfg.opt.Log {
			logFileName := strings.TrimSuffix(filepath.Base(cfg.opt.NZBfilepath), filepath.Ext(filepath.Base(cfg.opt.NZBfilepath))) + ".log"
			f, err := os.Create(logFileName)
			if err != nil {
				log.Printf("unable to open debug log file: %v", err)
				os.Exit(1)
			}
			log.SetOutput(f) //DEBUG
		}
	} else {
		if cfg.opt.Discard {
			log.SetOutput(io.Discard) // DEBUG
		}
	} // end debugs

	preparationStartTime := time.Now()
	if cfg.opt.Debug {
		log.Printf("Loading NZB: '%s'", cfg.opt.NZBfilepath)
	}
	nzbfile, err := loadNzbFile(cfg.opt.NZBfilepath)
	if err != nil {
		log.Printf("unable to load NZB file '%s': %v'", cfg.opt.NZBfilepath, err)
		os.Exit(1)
	}
	nzbhashname := SHA256str(filepath.Base(cfg.opt.NZBfilepath))
	if len(nzbfile.Files) <= 0 {
		log.Printf("error in NZB file '%s': nzbfile.Files=0'", cfg.opt.NZBfilepath)
		os.Exit(1)
	}

	// loop through all file tags within the NZB file
	for _, file := range nzbfile.Files {
		// loop through all segment tags within each file tag
		if cfg.opt.Debug {
			for _, agroup := range file.Groups {
				if agroup != "" && !slices.Contains(nzbgroups, agroup) {
					nzbgroups = append(nzbgroups, agroup)
				}
			}
			log.Printf("NewsGroups: %v", nzbgroups)
		}
		// filling segmentList
		for _, segment := range file.Segments {
			if cfg.opt.BUG {
				log.Printf("reading nzb: Id='%s' file='%s'", segment.Id, file.Filename)
			}
			// if you add more variables to 'segmentChanItem struct': compiler always fails here!
			item := &segmentChanItem{
				segment,
				make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)), make(map[int]bool, len(providerList)),
				sync.RWMutex{}, nil, false, false, false, false, false, false, 0, SHA256str("<" + segment.Id + ">"), false, make(chan int, 1), make(chan bool, 1), file.Number, 0, 0, 0, 0, 0, &nzbhashname}
			segmentList = append(segmentList, item)
		}
	}
	mibsize := float64(nzbfile.Bytes) / 1024 / 1024
	artsize := mibsize / float64(len(segmentList)) * 1000
	log.Printf("%s [%s] loaded NZB: '%s' [%d/%d] ( %.02f MiB | ~%.0f KiB/art )", appName, appVersion, cfg.opt.NZBfilepath, len(segmentList), nzbfile.TotalSegments, mibsize, artsize)

	// cosmetics
	if cfg.opt.Bar {
		cfg.opt.Verbose = false
	}
	// left-padding for log output
	digStr := fmt.Sprintf("%d", len(segmentList))
	D = fmt.Sprintf("%d", len(digStr))

	// load the provider list
	if err := loadProviderList(cfg.opt.ProvFile); err != nil {
		log.Printf("unable to load providerfile '%s' list: %v", cfg.opt.ProvFile, err)
		os.Exit(1)
	}
	totalMaxConns := 0
	for _, provider := range providerList {
		totalMaxConns += provider.MaxConns
	}

	if cfg.opt.MemMax <= 0 {
		cfg.opt.MemMax = totalMaxConns
	}
	memlim = NewMemLimiter(cfg.opt.MemMax)

	if cfg.opt.Debug {
		log.Printf("Loaded providerList: %d ... preparation took %v ms cfg.opt.MemMax=%d", len(providerList), time.Since(preparationStartTime).Milliseconds(), cfg.opt.MemMax)
	}

	var waitWorker sync.WaitGroup
	var workerWGconnEstablish sync.WaitGroup
	var waitDivider sync.WaitGroup
	var waitDividerDone sync.WaitGroup

	// boot cache routines
	if cacheON {
		cache = NewCache(
			cfg.opt.Cachedir,
			cfg.opt.CRW,
			cfg.opt.CheckOnly,
			cfg.opt.MaxArtSize,
			cfg.opt.DebugCache)

		if cache == nil {
			log.Printf("ERROR Cache failed... is nil!")
			os.Exit(1)
		}
		if !cache.MkSubDir(nzbhashname) {
			os.Exit(1)
		}
	}

	if cfg.opt.Bar {
		// segment check progressbar
		segmentBar = progressBars.NewBar("STAT", len(segmentList))
		segmentBar.SetPreBar(cmpb.CalcSteps)
		segmentBar.SetPostBar(cmpb.CalcTime)
		if cfg.opt.Colors {
			progressBars.SetColors(colors)
		}
		// start progressbar
		progressBars.Start()
		//progressBars.Stop("STAT", "done")
	}

	// run the go routines
	waitDivider.Add(1)
	waitDividerDone.Add(1)
	workerWGconnEstablish.Add(1)
	GoBootWorkers(&waitDivider, &workerWGconnEstablish, &waitWorker, nzbfile.Bytes)

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

	if cfg.opt.Bar {
		progressBars.Wait()
	}

	if cfg.opt.Debug {
		log.Print("main: waitWorker.Wait() released")
	}

	//endTime := time.Now()
	transferTook := time.Since(segmentCheckStartTime)
	if cfg.opt.CheckFirst {
		transferTook = time.Since(segmentCheckEndTime)
	}
	dlSpeed := int(float64(Counter.get("TOTAL_RXbytes")) / transferTook.Seconds() / 1024)
	upSpeed := int(float64(Counter.get("TOTAL_TXbytes")) / transferTook.Seconds() / 1024)

	totalruntime := fmt.Sprintf(" | QUIT |\n\n> NZB: '%s'\n> Total Runtime: %.0f sec (%v)", filepath.Base(cfg.opt.NZBfilepath), time.Since(preparationStartTime).Seconds(), time.Since(preparationStartTime))
	segchecktook := fmt.Sprintf("\n> SegCheck: %.0f sec (%v) ~%.0f ms/seg", segmentCheckTook.Seconds(), segmentCheckTook, float32(segmentCheckTook.Milliseconds())/float32(nzbfile.Segments))
	transfertook := fmt.Sprintf("\n> Transfer: %.0f sec (%v) ~%.0f ms/seg", transferTook.Seconds(), transferTook, float32(transferTook.Milliseconds())/float32(nzbfile.Segments))
	avgUpDlspeed := fmt.Sprintf("\n> DL %d KiB/s  (Total: %.2f MiB)\n> UL %d KiB/s  (Total: %.2f MiB)", dlSpeed, float64(Counter.get("TOTAL_RXbytes"))/float64(1024)/float64(1024), upSpeed, float64(Counter.get("TOTAL_TXbytes"))/float64(1024)/float64(1024))
	runtime_info := totalruntime + segchecktook + transfertook + avgUpDlspeed

	result := ""

	for id, _ := range providerList {
		providerList[id].mux.RLock()

		if !providerList[id].Enabled {
			providerList[id].mux.RUnlock()
			continue
		}
		result = result + fmt.Sprintf("\n> Results | checked: %"+D+"d | avail: %"+D+"d | miss: %"+D+"d | dl: %"+D+"d | up: %"+D+"d | @ '%s'",
			providerList[id].articles.checked,
			providerList[id].articles.available,
			providerList[id].articles.missing,
			providerList[id].articles.downloaded,
			providerList[id].articles.refreshed,
			providerList[id].Name,
		)
		providerList[id].mux.RUnlock()
	} // end for providerList

	log.Print(runtime_info + "\n> ###" + result + "\n> ###\n\n:end")
	//writeCsvFile()

	os.Exit(0)
} // end func main
