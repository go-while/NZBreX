package main

import (
	"flag"
	"fmt"
	"log"
	"runtime"
)

func ParseFlags() {
	flag.BoolVar(&version, "version", false, "prints app version")
	flag.BoolVar(&testproc, "testproc", false, "testing watchdir processor code")
	// essentials
	flag.StringVar(&cfg.opt.NZBfilepath, "nzb", "test.nzb", "/path/file.nzb")
	flag.StringVar(&cfg.opt.ProvFile, "provider", "provider.json", "/path/provider.json")
	flag.BoolVar(&cfg.opt.CheckOnly, "checkonly", false, "[true|false] check online status only: no downs/reups (default: false)")
	flag.BoolVar(&cfg.opt.CheckFirst, "checkfirst", false, "[true|false] if false: starts downs/reups asap as segments are checked (default: false)")
	//flag.BoolVar(&cfg.opt.UploadLater, "uploadlater", false, "[true|false] if true: starts upload if everything (available) has been downloaded to cache (default: false) !not implemented: TODO!")
	//flag.BoolVar(&cfg.opt.Verify, "verify", false, "[true|false] waits and tries to verify/recheck all reups (default: false) !not implemented: TODO!")
	// cache and mem
	flag.IntVar(&cfg.opt.MemMax, "mem", 0, "limit memory usage to N segments in RAM ( 0 defaults to number of total provider connections*2 or what you set but usually there is no need for more. if your 'MEM' is full: your upload is just slow. giving more mem will NOT help!)")
	flag.IntVar(&cfg.opt.ChanSize, "chansize", DefaultChanSize, "sets internal size of channels to queue items for check,down,reup. default should be fine.")
	flag.StringVar(&cfg.opt.Cachedir, "cd", "", "/path/to/cache/dir")
	flag.BoolVar(&cfg.opt.CheckCacheOnBoot, "cc", false, "[true|false] checks nzb vs cache on boot (default: false)")
	flag.IntVar(&cfg.opt.CRW, "crw", DefaultCacheRW, "sets number of Cache Reader and Writer routines to equal amount")
	// header and yenc
	flag.BoolVar(&cfg.opt.CleanHeaders, "cleanhdr", true, "[true|false] removes unwanted headers. only change this if you know why! (default: true) ")
	flag.StringVar(&cfg.opt.CleanHeadersFile, "cleanhdrfile", "", "loads unwanted headers to cleanup from /path/to/cleanHeaders.txt")
	flag.BoolVar(&cfg.opt.YencCRC, "crc32", false, "[true|false] checks crc32 of articles on the fly while downloading (default: false) !TODO: external source code needs review!")
	flag.IntVar(&cfg.opt.YencTest, "yenctest", 2, "select mode 1 (bytes) or 2 (lines) to use in -crc32. (experimental/testing) mode 2 should use less mem.")
	flag.IntVar(&cfg.opt.YencCpu, "yenccpu", 4, fmt.Sprintf("limits parallel decoding with -crc32=true. 0 defaults to runtime.NumCPU() here=%d (experimental/testing)", runtime.NumCPU()))
	flag.BoolVar(&cfg.opt.YencWrite, "yencout", false, "[true|false] writes yenc body parts to cache (experimental/testing)")
	// debug output flags
	flag.BoolVar(&runProf, "prof", false, "starts profiler @mem: waits 20sec and runs 120 sec @cpu: waits 20 sec and captures to end")
	flag.StringVar(&webProf, "webprof", "", "start profiling webserver at: '[::]:61234' or '127.0.0.1:61234' or 'IP4_ADDR:PORT' or '[IP6_ADDR]:PORT'")
	flag.BoolVar(&cfg.opt.Verbose, "verbose", true, "[true|false] a little more output than nothing (default: false)")
	flag.BoolVar(&cfg.opt.Discard, "discard", false, "[true|false] reduce console output to minimum (default: false)")
	flag.Int64Var(&cfg.opt.LogPrintEvery, "print", DefaultLogPrintEvery, "prints stats every N seconds. 0 is spammy and -1 disables output. a very high number will print only once it is finished")
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

	if cfg.opt.YencCRC {
		if cfg.opt.YencTest <= 0 || cfg.opt.YencTest > 2 {
			cfg.opt.YencTest = 2
		}
		log.Printf("cfg.opt.YencTest=%d", cfg.opt.YencTest)
	}
} // end func ParseFlags
