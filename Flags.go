package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-while/NZBreX/rapidyenc"
	prof "github.com/go-while/go-cpu-mem-profiler"
)

var AddUserDataToProxy string // AddUserToProxy is a flag to add user to proxy

func ParseFlags() {
	flag.BoolVar(&version, "version", false, "prints app version")
	// essentials
	flag.StringVar(&nzbfile, "nzb", "nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz", "/path/file.nzb(.gz)")
	//flag.StringVar(&cfg.opt.NzbDir, "nzbdir", "", "/path/to/watch/dir/for/nzbfiles")
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
	flag.StringVar(&cfg.opt.CleanHeadersFile, "cleanhdrfile", "", "loads unwanted headers to cleanup from /path/to/cleanHeaders.txt (default: empty = use default headers) !! only use this header file option if you know what you do and why !!")
	flag.BoolVar(&cfg.opt.YencCRC, "crc32", false, "[true|false] checks crc32 of articles on the fly while downloading (default: false) (experimental/testing)")
	flag.IntVar(&cfg.opt.YencTest, "yenctest", 4, "select mode 1 (bytes) or 2 (lines) or 3 (lines async) or 4 (rapidyenc async) to use in -crc32. mode 1 is oldschool and mode 2 should use less mem than mode 1. new mode 3+4 need testing (default: 4) (experimental/testing)")
	flag.IntVar(&cfg.opt.YencCpu, "yenccpu", 0, fmt.Sprintf("limits parallel decoding and final merging with -crc32=true. 0 defaults to runtime.NumCPU() here=%d (experimental/testing)", runtime.NumCPU()))
	flag.IntVar(&cfg.opt.YencAsyncCpu, "yencasync", 64, fmt.Sprintf("limits async parallel decoding with -crc32=true and -yenctest=(3 or 4). 0 defaults to runtime.NumCPU() here=%d (experimental/testing)", runtime.NumCPU()))
	flag.BoolVar(&cfg.opt.YencWrite, "yencout", false, "[true|false] writes yenc parts to cache (needs -cd=/dir/) (default: false) (experimental/testing) ")
	flag.BoolVar(&cfg.opt.YencMerge, "yencmerge", false, "[true|false] merge yenc parts into target files (default: false) (experimental/testing)")
	flag.BoolVar(&cfg.opt.YencDelParts, "yencdelparts", false, "[true|false] delete .part.N.yenc files after merge (deletes parts only with -yencmerge=true) (default: false) (experimental/testing)")
	flag.IntVar(&cfg.opt.RapidYencBufSize, "rapidyencbufsize", rapidyenc.DefaultBufSize, "set only if you know what you do! (default: 4K) (experimental/testing)")
	flag.BoolVar(&cfg.opt.DoubleCheckRapidYencCRC, "doublecheckrapidyenccrc", false, "(experimental/testing)")
	// debug output flags
	flag.BoolVar(&runProf, "prof", false, "starts profiler (for debugging)\n       @mem: waits 20sec and runs 120 sec\n       @cpu: waits 20 sec and captures to end")
	flag.StringVar(&webProf, "profweb", "", "start profiling webserver at: '[::]:61234' or '127.0.0.1:61234' (default: empty = dont start websrv)")
	flag.BoolVar(&cfg.opt.Verbose, "verbose", true, "[true|false] a little more output than nothing (default: false)")
	flag.BoolVar(&cfg.opt.Discard, "discard", false, "[true|false] reduce console output to minimum (default: false)")
	flag.Int64Var(&cfg.opt.PrintStats, "printstats", DefaultPrintStats, "prints stats every N seconds. 0 is spammy and -1 disables output. a very high number will print only once it is finished")
	flag.BoolVar(&cfg.opt.Print430, "print430", false, "[true|false] prints notice about code 430 article not found")
	flag.BoolVar(&cfg.opt.Log, "log", false, "[true|false] logs to file (default: false)")
	flag.BoolVar(&cfg.opt.LogAppend, "logappend", false, "[true|false] appends to logfile instead of rotating or overwriting old ones (default: false)")
	flag.StringVar(&cfg.opt.LogDir, "logdir", "logs", "defaults to logs/ in executable dir, an empty string puts logfile into the currentdir of executable")
	flag.IntVar(&cfg.opt.LogOld, "logold", 0, "rotates log files to .N, 0 disables rotation (default: 0)")
	flag.BoolVar(&cfg.opt.BUG, "debugBUG", false, "[true|false] bug is an extra flag for some very, very spammy code lines (default: false)")
	flag.BoolVar(&cfg.opt.Debug, "debug", false, "[true|false] general debug -does not print everything- (default: false)")
	flag.BoolVar(&cfg.opt.DebugCache, "debugcache", false, "[true|false] debug Cache (default: false)")
	flag.BoolVar(&cfg.opt.DebugConnPool, "debugconnpool", false, "[true|false] debug ConnPool (default: false)")
	flag.BoolVar(&cfg.opt.DebugSharedCC, "debugsharedcc", false, "[true|false] debug sharedConn Chan (default: false)")
	flag.BoolVar(&cfg.opt.DebugWorker, "debugworker", false, "[true|false] debug WORKER (default: false)")
	flag.BoolVar(&cfg.opt.DebugMemlim, "debugmemlim", false, "[true|false] debug MEMLIMIT (default: false)")
	flag.BoolVar(&cfg.opt.DebugSTAT, "debugstat", false, "[true|false] debug STAT (default: false)")
	flag.BoolVar(&cfg.opt.DebugARTICLE, "debugarticle", false, "[true|false] debug ARTICLE (default: false)")
	flag.BoolVar(&cfg.opt.DebugIHAVE, "debugihave", false, "[true|false] debug IHAVE (default: false)")
	flag.BoolVar(&cfg.opt.DebugPOST, "debugpost", false, "[true|false] debug POST (default: false)")
	flag.BoolVar(&cfg.opt.DebugFlags, "debugflags", false, "[true|false] debug item flags (default: false)")
	flag.BoolVar(&cfg.opt.DebugCR, "debugcr", false, "[true|false] debug check routine (default: false)")
	flag.BoolVar(&cfg.opt.DebugDR, "debugdr", false, "[true|false] debug downs routine (default: false)")
	flag.BoolVar(&cfg.opt.DebugUR, "debugur", false, "[true|false] debug reups routine (default: false)")
	flag.BoolVar(&cfg.opt.DebugRapidYenc, "debugrapidyenc", false, "[true|false] debug rapidyenc (default: false)")
	// rate limiter
	flag.IntVar(&cfg.opt.SloMoC, "slomoc", 0, "SloMo'C' limiter sleeps N milliseconds before checking")
	flag.IntVar(&cfg.opt.SloMoD, "slomod", 0, "SloMo'D' limiter sleeps N milliseconds before downloading")
	flag.IntVar(&cfg.opt.SloMoU, "slomou", 0, "SloMo'U' limiter sleeps N milliseconds before uploading")
	// no need to change this
	flag.IntVar(&cfg.opt.MaxArtSize, "maxartsize", DefaultMaxArticleSize, "limits article size to 1M (mostly articles have ~700K only)")
	flag.BoolVar(&testmode, "zzz-shr-testmode", false, "[true|false] only used to test compilation on self-hosted runners (default: false)")
	flag.BoolVar(&testrapidyenc, "testrapidyenc", false, "[true|false] will test rapidyenc testfiles on boot and exit (default: false)")
	flag.IntVar(&cfg.opt.ProxyTCP, "proxytcp", 0, "if set: use this port (e.g.: 1119) for TCP proxying (default: 0 = no proxy)")
	flag.IntVar(&cfg.opt.ProxyTLS, "proxytls", 0, "if set: use this port (e.g.: 1563) for TLS proxying (default: 0 = no proxy)")
	flag.StringVar(&AddUserDataToProxy, "proxyadduser", "", "if set: adds this user to proxy ( \ne.g.: -proxyadduser='username1234|password4567|maxconns|expires'\n ) \nPassword entered MUST be cleartext, hashing is done when writing to .passwd file!\n Expiration: you can use a 'number of days' with 'd' at the end (e.g. 7d, 30d, 365d) or 'h'  for hours (1h, 12h, ...) or 'm' minutes (1m, 30m, ...), it will be converted to a valid unixtimestamp expiring in the future from now on! (default: empty = no user added)\nExample -proxyadduser \"HelloWorld|NotAsecurePassword|5|42d\" creates a user with 5 conns and 42 days to expiration")
	flag.StringVar(&cfg.opt.ProxyPasswdFile, "proxypasswdfile", ".proxypasswd", "if set: use this file for proxy (default: .proxypasswd in current dir)")
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
	if cfg.opt.ProxyPasswdFile == "" {
		cfg.opt.ProxyPasswdFile = ".proxypasswd" // default passwd file for proxy
	}
	if AddUserDataToProxy != "" {
		// Format: 'user|password|maxconns|expires|(no)post' (expires is unix timestamp)
		parts := strings.SplitN(AddUserDataToProxy, "|", 4)
		if len(parts) != 5 {
			log.Fatalf("-proxyadduser expects format: user|password|maxconns|expires|(no)post (got: %q)", AddUserDataToProxy)
		}
		var username, password string
		Ausername := strings.TrimSpace(parts[0])
		Apassword := strings.TrimSpace(parts[1])
		if Ausername == "auto" && Apassword == "auto" {
			// create auto user with random password
			username, password, _ = generateRandomHexCredentials()
		} else {
			username = Ausername
			password = Apassword
		}
		if len(username) < 10 {
			log.Fatalf("-proxyadduser: username must be at least 10 characters long, got %d characters", len(username))
		}
		if len(password) < 10 {
			log.Fatalf("-proxyadduser: password must be at least 10 characters long, got %d characters", len(password))
		}
		maxconns, err1 := strconv.Atoi(parts[2])
		expiresStr := strings.TrimSpace(parts[3])
		var expires int64
		if strings.HasSuffix(expiresStr, "d") {
			days, err := strconv.ParseInt(strings.TrimSuffix(expiresStr, "d"), 10, 64)
			if err != nil {
				log.Fatalf("-adduser: invalid expires (days): %s", expiresStr)
			}
			expires = time.Now().Add(time.Duration(days) * 24 * time.Hour).Unix()
		} else if strings.HasSuffix(expiresStr, "h") {
			hours, err := strconv.ParseInt(strings.TrimSuffix(expiresStr, "h"), 10, 64)
			if err != nil {
				log.Fatalf("-adduser: invalid expires (hours): %s", expiresStr)
			}
			expires = time.Now().Add(time.Duration(hours) * time.Hour).Unix()
		} else if strings.HasSuffix(expiresStr, "m") {
			minutes, err := strconv.ParseInt(strings.TrimSuffix(expiresStr, "m"), 10, 64)
			if err != nil {
				log.Fatalf("-adduser: invalid expires (minutes): %s", expiresStr)
			}
			expires = time.Now().Add(time.Duration(minutes) * time.Minute).Unix()
		} else {
			expiresInt, err2 := strconv.ParseInt(parts[3], 10, 64)
			if err1 != nil || err2 != nil {
				log.Fatalf("-adduser: invalid maxconns or expires: maxconns='%s' expires='%s'", parts[2], parts[3])
			}
			expires = expiresInt
		}
		if username == "" || password == "" {
			log.Fatalf("-adduser: username and password must not be empty")
		}
		if expires < time.Now().Unix() {
			log.Fatalf("-adduser: expires must be a future timestamp, got %d", expires)
		}
		// Compose new passwd file name
		newPasswdFile := fmt.Sprintf("%s.new.%d.%d", cfg.opt.ProxyPasswdFile, time.Now().Unix(), mrand.Intn(65535))
		userData := &UserData{
			Username: username,
			Password: password,
			MaxConns: maxconns,
			ExpireAt: expires,
			Posting:  strings.HasPrefix(strings.ToLower(parts[4]), "post"),
		}
		if err := addUserToProxyPasswdFile(userData, newPasswdFile); err != nil {
			log.Fatalf("Failed to add user to %s: %v", newPasswdFile, err)
			os.Exit(1)
		}
		log.Printf("User '%s' added to new passwd file: '%s'. Next reload in <60s", username, newPasswdFile)
		os.Exit(0)
	}
	// test rapidyenc decoder
	if testrapidyenc {
		// this is only for testing rapidyenc decoder
		dlog(always, "Testing rapidyenc decoder...")
		decoder := rapidyenc.AcquireDecoder()
		decoder.SetDebug(true, true)
		segId := "any@thing.net"
		decoder.SetSegmentId(&segId)
		rapidyenc.ReleaseDecoder(decoder)   // release the decoder
		decoder = nil                       // clear memory
		errs := rapidyencDecoderFilesTest() // test rapidyenc decoder with files
		if len(errs) != 0 {
			dlog(always, "ERROR testing rapidyenc decoder: %v", errs)
			os.Exit(1)
		}
		dlog(always, "rapidyenc decoder successfully initialized! quitting now...")
		if runProf {
			Prof.StopCPUProfile() // stop cpu profiling
			time.Sleep(time.Second)
		}
		os.Exit(0) // exit after testing rapidyenc decoder
	}

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
			dlog(always, "ERROR Cache failed... is nil!")
			os.Exit(1)
		}
		if !cache.MkSubDir("test") {
			os.Exit(1)
		}
	}

	if cfg.opt.UploadLater && cfg.opt.Cachedir == "" {
		dlog(always, "ERROR: you can not use -uploadlater without -cd=/path/to/cache/dir because it needs a cache dir!")
		os.Exit(1)
	}

	if cfg.opt.LogAppend && cfg.opt.LogOld > 0 {
		dlog(always, "ERROR: you can not use -logappend with -logold > 0 because it will not rotate logs!")
		os.Exit(1)
	}

	if cfg.opt.ByPassSTAT && (cfg.opt.CheckFirst || cfg.opt.CheckOnly) {
		dlog(always, "ERROR: you can not use -bypassstat with -checkfirst and/or -checkonly because both check options use STAT cmd!")
		os.Exit(1)
	}

	if cfg.opt.YencCRC {

		if cfg.opt.YencTest <= 0 || cfg.opt.YencTest > 4 {
			dlog(always, "ERROR: you can not use -crc32 with -yenctest=%d because it must be 1-4 (default: 2)", cfg.opt.YencTest)
			os.Exit(1)
		}

		if cfg.opt.YencTest == 4 {
			dlog(always, "Using rapidyenc for yenc decoding!")
		}

		if cfg.opt.YencCpu < 0 {
			dlog(always, "ERROR: you can not use -yenccpu=%d because it must be >= 0 (default: 0)", cfg.opt.YencCpu)
			os.Exit(1)
		}

		if (cfg.opt.YencWrite || cfg.opt.YencMerge) && !cacheON {
			dlog(always, "ERROR: you can not use -yencout=true or -yencmerge=true without -cd=/path/to/cache/dir because it needs a cache dir!")
			os.Exit(1)
		}

		if cfg.opt.RapidYencBufSize != rapidyenc.DefaultBufSize {
			// yenc line+CRLF = 128+2 = 130 bytes minimum
			if cfg.opt.RapidYencBufSize < 130 {
				dlog(always, "ERROR: you can not use -rapidyencbufsize=%d because it must be at least 130 (default: %d)", cfg.opt.RapidYencBufSize, rapidyenc.DefaultBufSize)
				os.Exit(1)
			}
			rapidyenc.DefaultBufSize = cfg.opt.RapidYencBufSize
			dlog(always, "Using rapidyenc with bufsize=%d", rapidyenc.DefaultBufSize)
		}

	} else {
		cfg.opt.YencTest = 0      // reset to 0 if not using YencCRC
		cfg.opt.YencWrite = false // reset to false if not using YencCRC
		cfg.opt.YencMerge = false // reset to false if not using YencCRC
	} // end if YencCRC

	if cfg.opt.MaxArtSize < 1 {
		cfg.opt.MaxArtSize = 1 // set minimum article size to 1 byte who ever wants this!
	}

	if cfg.opt.LogOld < 0 {
		cfg.opt.LogOld = 0 // disable log rotation
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

	if headers, err := LoadHeadersFromFile(cfg.opt.CleanHeadersFile); headers != nil {
		cleanHeader = headers
		dlog(always, "Loaded %d headers from '%s' ==> cleanHeader='%#v'", len(headers), cfg.opt.CleanHeadersFile, cleanHeader)
	} else if err != nil {
		dlog(always, "ERROR loading headers failed file='%s' err='%v'", cfg.opt.CleanHeadersFile, err)
		os.Exit(1)
	}

	if cfg.opt.ProxyTCP > 0 || cfg.opt.ProxyTLS > 0 {
		proxy = true
	}

	dlog(cfg.opt.Verbose, "Settings: '%#v'", *cfg.opt)

} // end func ParseFlags

func rapidyencDecoderFilesTest() (errs []error) {
	files := []string{
		"yenc/multipart_test.yenc",
		"yenc/multipart_test_badcrc.yenc",
		"yenc/singlepart_test.yenc",
		"yenc/singlepart_test_badcrc.yenc",
	}
	for _, fname := range files {
		dlog(always, "\n=== Testing rapidyenc with file: %s ===\n", fname)
		f, err := os.Open(filepath.Clean(fname))
		if err != nil {
			dlog(always, "Failed to open %s: %v\n", fname, err)
			continue
		}

		pipeReader, pipeWriter := io.Pipe()
		decoder := rapidyenc.AcquireDecoderWithReader(pipeReader)
		decoder.SetDebug(true, true)
		segId := fname
		decoder.SetSegmentId(&segId)

		// Start goroutine to read decoded data
		var decodedData bytes.Buffer
		done := make(chan error, 1)
		go func() {
			buf := make([]byte, rapidyenc.DefaultBufSize)
			for {
				n, aerr := decoder.Read(buf)
				if n > 0 {
					decodedData.Write(buf[:n])
				}
				if aerr == io.EOF {
					done <- nil
					return
				}
				if aerr != nil {
					done <- aerr
					return
				}
			}
		}()

		// Write file lines to the pipeWriter
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if _, err := pipeWriter.Write([]byte(line + "\r\n")); err != nil {
				dlog(always, "Error writing to pipe: %v\n", err)
				pipeWriter.Close()
				rapidyenc.ReleaseDecoder(decoder)
				return
			}
		}
		if _, err := pipeWriter.Write([]byte(".\r\n")); err != nil { // NNTP end marker
			dlog(always, "Error writing end marker to pipe: %v\n", err)
			pipeWriter.Close()
			rapidyenc.ReleaseDecoder(decoder)
			return
		}
		pipeWriter.Close()
		f.Close()
		if aerr := <-done; aerr != nil {
			err = aerr
			var aBadCrc uint32
			meta := decoder.Meta()
			dlog(always, "DEBUG Decoder error: '%v' (maybe an expected error, check below)\n", err)
			expectedCrc := decoder.ExpectedCrc()
			if expectedCrc != 0 && expectedCrc != meta.Hash {

				// Set aBadCrc based on the file name
				switch fname {
				case "yenc/singlepart_test_badcrc.yenc":
					aBadCrc = 0x6d04a475
				case "yenc/multipart_test_badcrc.yenc":
					aBadCrc = 0xf6acc027
				}
				if aBadCrc > 0 && aBadCrc != meta.Hash {
					dlog(always, "WARNING1 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else if aBadCrc > 0 && aBadCrc == meta.Hash {
					dlog(always, "rapidyenc OK expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
				} else if expectedCrc != meta.Hash {
					dlog(always, "WARNING2 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else {
					dlog(always, "GOOD CRC matches! aBadCrc=%#08x Name: '%s' fname: '%s'\n", aBadCrc, meta.Name, fname)
				}

			} else if expectedCrc == 0 {
				dlog(always, "WARNING rapidyenc: No expected CRC set, cannot verify integrity. fname: '%s'\n", fname)
				errs = append(errs, aerr)
			}
		} else {
			meta := decoder.Meta()
			dlog(always, "OK Decoded %d bytes, CRC32: %#08x, Name: '%s' fname: '%s'\n", decodedData.Len(), meta.Hash, meta.Name, fname)
		}
		rapidyenc.ReleaseDecoder(decoder)
	} // end for range files
	return errs
} // end func rapidyencDecoderFilesTest
