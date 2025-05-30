package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	prof "github.com/go-while/go-cpu-mem-profiler"
)

func ParseFlags() {
	flag.BoolVar(&version, "version", false, "prints app version")
	// essentials
	flag.StringVar(&nzbfile, "nzb", "nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz", "/path/file.nzb(.gz)")
	flag.StringVar(&cfg.opt.ProvFile, "provider", "provider.json", "/path/provider.json")
	flag.BoolVar(&cfg.opt.CheckOnly, "checkonly", false, "[true|false] check online status only: no downs/reups (default: false)")
	flag.BoolVar(&cfg.opt.CheckFirst, "checkfirst", false, "[true|false] if false: starts downs/reups asap as segments are checked (default: false)")
	//flag.BoolVar(&cfg.opt.ByPassSTAT, "bypassstat", false, "[true|false] true goes directly to download without checking availability with STAT first (default: false)")
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
	flag.BoolVar(&cfg.opt.YencCRC, "crc32", false, "[true|false] checks crc32 of articles on the fly while downloading (default: false)")
	flag.IntVar(&cfg.opt.YencTest, "yenctest", 2, "select mode 1 (bytes) or 2 (lines) to use in -crc32. (experimental/testing) mode 2 should use less mem. (default: 2)")
	flag.IntVar(&cfg.opt.YencCpu, "yenccpu", 8, fmt.Sprintf("limits parallel decoding with -crc32=true. 0 defaults to runtime.NumCPU() here=%d (experimental/testing)", runtime.NumCPU()))
	flag.BoolVar(&cfg.opt.YencWrite, "yencout", false, "[true|false] writes yenc parts to cache (needs -cd=/dir/) (experimental/testing) (default: false)")
	flag.BoolVar(&cfg.opt.YencMerge, "yencmerge", false, "[true|false] merge yenc parts into target files (experimental/testing) (default: false)")
	flag.BoolVar(&cfg.opt.YencDelParts, "yencdelparts", false, "[true|false] delete .part.N.yenc files after merge (deletes parts only with -yencmerge=true) (experimental/testing) (default: false)")
	// debug output flags
	flag.BoolVar(&runProf, "prof", false, "starts profiler (for debugging)\n       @mem: waits 20sec and runs 120 sec\n       @cpu: waits 20 sec and captures to end")
	flag.StringVar(&webProf, "profweb", "", "start profiling webserver at: '[::]:61234' or '127.0.0.1:61234' (default: empty = dont start websrv)")
	flag.BoolVar(&cfg.opt.Verbose, "verbose", true, "[true|false] a little more output than nothing (default: false)")
	flag.BoolVar(&cfg.opt.Discard, "discard", false, "[true|false] reduce console output to minimum (default: false)")
	flag.Int64Var(&cfg.opt.PrintStats, "printstats", DefaultPrintStats, "prints stats every N seconds. 0 is spammy and -1 disables output. a very high number will print only once it is finished")
	flag.BoolVar(&cfg.opt.Print430, "print430", false, "[true|false] prints notice about code 430 article not found")
	flag.BoolVar(&cfg.opt.Log, "log", false, "[true|false] logs to file (default: false)")
	flag.BoolVar(&cfg.opt.BUG, "bug", false, "[true|false] full debug (default: false)")
	flag.BoolVar(&cfg.opt.Debug, "debug", false, "[true|false] part debug (default: false)")
	flag.BoolVar(&cfg.opt.DebugCache, "debugcache", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugConnPool, "debugconnpool", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugSharedCC, "debugsharedcc", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugWorker, "debugworker", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugCR, "debugcr", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugDR, "debugdr", false, "[true|false] (default: false)")
	flag.BoolVar(&cfg.opt.DebugUR, "debugur", false, "[true|false] (default: false)")
	// rate limiter
	flag.IntVar(&cfg.opt.SloMoC, "slomoc", 0, "SloMo'C' limiter sleeps N milliseconds before checking")
	flag.IntVar(&cfg.opt.SloMoD, "slomod", 0, "SloMo'D' limiter sleeps N milliseconds before downloading")
	flag.IntVar(&cfg.opt.SloMoU, "slomou", 0, "SloMo'U' limiter sleeps N milliseconds before uploading")
	// no need to change this
	flag.IntVar(&cfg.opt.MaxArtSize, "maxartsize", DefaultMaxArticleSize, "limits article size to 1M (mostly articles have ~700K only)")
	flag.BoolVar(&testmode, "zzz-shr-testmode", false, "[true|false] only used to test compilation on self-hosted runners (default: false)")
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
		Prof = prof.NewProf()
		RunProf()
	}

	if cfg.opt.ByPassSTAT && (cfg.opt.CheckFirst || cfg.opt.CheckOnly) {
		log.Printf("ERROR: you can not use -bypassstat with -checkfirst and/or -checkonly because both check options use STAT cmd!")
		os.Exit(1)
	}

	if cfg.opt.ByPassSTAT && (cfg.opt.CheckFirst || cfg.opt.CheckOnly) {
		log.Printf("ERROR: you can not use -bypassstat with -checkfirst and/or -checkonly because both check options use STAT cmd!")
		os.Exit(1)
	}

	if cfg.opt.YencCRC {
		if cfg.opt.YencTest <= 0 || cfg.opt.YencTest > 2 {
			cfg.opt.YencTest = 2
		}
	}

	// setup debug modes
	if cfg.opt.Debug || cfg.opt.BUG {
		if !cfg.opt.Debug {
			cfg.opt.Debug = true
		}
		if !cfg.opt.Verbose {
			cfg.opt.Verbose = true
		}
	} else {
		if cfg.opt.Discard {
			log.SetOutput(io.Discard) // DEBUG
		}
	} // end debugs

	cacheON = (cfg.opt.Cachedir != "" && cfg.opt.CRW > 0)
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

	if headers, err := LoadHeadersFromFile(cfg.opt.CleanHeadersFile); headers != nil {
		cleanHeader = headers
		log.Printf("Loaded %d headers from '%s' ==> cleanHeader='%#v'", len(headers), cfg.opt.CleanHeadersFile, cleanHeader)
	} else if err != nil {
		log.Printf("ERROR loading headers failed file='%s' err='%v'", cfg.opt.CleanHeadersFile, err)
		os.Exit(1)
	}

	if cfg.opt.Verbose {
		log.Printf("Settings: '%#v'", *cfg.opt)
	}
} // end func ParseFlags
