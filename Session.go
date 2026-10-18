package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/go-while/go-loggedrwmutex"
)

// PROCESSOR manages NZB file processing.
// It monitors a directory for NZB files, processes them, and maintains:
// - a map of active sessions (each representing an NZB file and its segments)
// - a record of seen files to prevent duplicate processing
// - the same PROCESSOR instance can handle multiple NZB files concurrently or a single file via cmd line
// - it provides methods to start new sessions, refresh the directory, and manage the processing state
// - it is designed to be run as a long-running service, processing files as they appear in the watched directory
type PROCESSOR struct {
	IsRunning bool
	//cfg       *Config // TODO: remove from global scope?
	//mux       sync.RWMutex
	mux       *loggedrwmutex.LoggedSyncRWMutex
	sessIds   uint64              // counts up
	sessMap   map[uint64]*SESSION // map with sessions
	nzbDir    string              // watch this dir for nzb files
	seenFiles map[string]bool     // map to keep track of already seen/added files
	nzbFiles  []string            // watched and added from nzbDir
	refresh   time.Duration       // update list of nzb dir files every N seconds
	//limitA    int                 // limits amount of open sessions
	stopChan chan struct{}
	//waitSessionMap     map[uint64]*sync.WaitGroup
} // end type PROCESSOR struct

// SESSION represents a single NZB file processing session.
// It contains information about the NZB file, its segments, and the providers involved.
// Each session has its own ID, parsed NZB file, and a list of segments to process.
// It also manages channels for segment processing and holds statistics about the file.
// The SESSION struct is used to track the state of processing for each NZB file.
// It is created by the PROCESSOR when a new NZB file is detected and processed.
type SESSION struct {
	//mux       sync.RWMutex
	// stopChan is closed exactly once, by stop(), when the session's work is over.
	// It is created with the session and never replaced: readers may copy it into a local.
	stopChan       chan struct{}
	stopOnce       sync.Once // guards close(stopChan)
	closeChansOnce sync.Once // guards closing segmentChansDowns/Reups (see closeWorkChans)
	mux            *loggedrwmutex.LoggedSyncRWMutex
	counter        *Counter_uint64 // private counter for every session // TODO check all GCounter if they should be private
	proc           *PROCESSOR      // the parent where we belong to
	sessId         uint64          // session ID
	nzbName        string          // test.nzb
	nzbHashedname  string          // the hashed name of the full filename with .extension
	nzbPath        string          // /path/to/nzbDir/test.nzb(.gz)
	nzbFile        *nzbparser.Nzb  // the parsed NZB file structure
	nzbSize        int64           // the byte size of all the files in the nzb. will be > 0 if nzbFile is loaded or has been loaded before
	nzbGroups      []string        // informative
	//lastRun               time.Time                  	   // lastRun is the time when the session was last run
	//nextRun               time.Time                      // nextRun is the time when the session is scheduled to run next
	segmentList []*segmentChanItem // list holds the segments from parsed nzbFile
	// One channel per provider group, built once by GoBootWorkers before any worker starts,
	// read-only while the session runs. Close owners: the group's feeder closes its check
	// channel, closeWorkChans closes the downs/reups channels after the divider has quit.
	segmentChansCheck map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansDowns map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansReups map[string]chan *segmentChanItem // map[provider.Group]chan
	checkers          map[string]*checkerGroup         // live check routines per provider.Group, built with the channels
	workDone          atomic.Uint64                    // items finished by any check/downs/reups routine, ok or not (divider watchdog)
	progress          sessionProgress                  // counts published by GoWorkDivider for the web UI
	ready             chan struct{}                    // proxy/web base sessions: closed once the providers are loaded (nil: not used)
	//memDL                map[string]map[*string]*segmentChanItem // (map[provider.group]map[ptr to segment.Id]ptr to Item) --- with -checkfirst queues items here. map will be create in GoWorkDivider()
	//memUP                 map[string]map[*string]*segmentChanItem // TODO: (map[provider.group]map[ptr to segment.Id]ptr to Item) --- with -uploadlater process uploads after downloads (possible only with cacheON) map will be create in GoWorkDivider()
	preBoot               bool              // true from LaunchSession until GoBootWorkers starts the workers; guarded by mux
	active                bool              // true while the workers run (GoBootWorkers until closeSession); guarded by mux
	preparationStartTime  time.Time         // flag // time when the session was started
	segmentCheckStartTime time.Time         // flag // time when the segment check started
	segmentCheckEndTime   time.Time         // flag // time when the segment check ended
	segmentCheckTook      time.Duration     // time it took to check all segments
	providerList          []*Provider       // list of providers to use for this session
	providerGroups        []string          // contains the provider groups from the providerList
	fileStat              filesStatistic    // fileStat is a map of file statistics for each file in the NZB
	fileStatLock          sync.Mutex        // fileStatLock is used to lock the fileStat map for concurrent access
	results               []string          // stores the results from Results()
	digStr                string            // cosmetics for results print
	D                     string            // cosmetics for results print
	closed                time.Time         // closed is the time when the session was closed
	WorkDividerChan       chan *WrappedItem // channel to send items to the work divider
	checkFeedDone         bool              // checkDone is set to true when the segment feeder has finished feeding to channel, check may still be activly running!
	segcheckdone          bool              // segcheckdone is set to true when the segment check is done
	proxy                 bool              // flag to signal proxy is used in LaunchSession
	bootedWorkers         int               // counts how many workers have been booted
} // end type SESSION struct

func (p *PROCESSOR) NewProcessor() error {
	p.mux = &loggedrwmutex.LoggedSyncRWMutex{Name: "PROCESSOR"} // use a logged sync mutex to log locking and unlocking
	p.mux.Lock()
	defer p.mux.Unlock()
	if p.IsRunning {
		return fmt.Errorf("error This Processor: is already setup")
	}

	if cfg.opt.NzbDir != "" {
		retries := 3
		for {
			if DirExists(cfg.opt.NzbDir) {
				break
			}
			if retries <= 0 {
				return fmt.Errorf("error NewProcessor: watchDir not found: '%s'", cfg.opt.NzbDir)
			}
			dlog(always, "WARN NewProcessor: watchDir not found: '%s' ... retry in 9s (retries %d)", cfg.opt.NzbDir, retries)
			time.Sleep(9 * time.Second) // watchdir not found, retry in
			retries--
		}
		//p.refresh = time.Duration(0 * time.Second) // default. call processor.SetDirRefresh after NewProcessor
		p.nzbDir = cfg.opt.NzbDir
		p.seenFiles = make(map[string]bool)
		go p.watchDirThread()
	}
	p.sessMap = make(map[uint64]*SESSION)
	p.stopChan = make(chan struct{}, 1)
	go p.processorThread()
	p.IsRunning = true
	dlog(always, "NewProcessor: nzbDir='%s' refresh=%d proxy=%t", p.nzbDir, p.refresh, proxy)
	return nil
} // end func NewProcessor

func (p *PROCESSOR) LaunchSession(s *SESSION, nzbfilepath string, waitSession *sync.WaitGroup) (err error) {
	// LaunchSession starts processing an NZB file in a session.
	// - If nzbfilepath is provided and s is nil, a new session is created for the file.
	// - If s is provided and nzbfilepath is empty, the existing session is used.
	// - Only one session can be active at a time.
	// - If waitSession is not nil, it will be marked done when processing finishes.
	// This function blocks until the session completes. To run in the background, call it as a goroutine and use waitSession.

	if waitSession != nil {
		defer waitSession.Done()
	}

	if !proxy { // the normal case, we are processing an nzb file. don't use the session var here!
		// not booting proxy, no session supplied, create a new session
		// checks for the correct inputs
		if nzbfilepath == "" && s == nil {
			// we have no nzbfilepath and no session
			// this is a bug in the code, we can't handle both at the same time!
			return fmt.Errorf("error LaunchSession: need nzbfilepath or session")

		} else if nzbfilepath != "" && s != nil {
			// we have a nzbfilepath and a session
			// this is a bug in the code, we can't handle both at the same time!
			return fmt.Errorf("error LaunchSession: nzbfilepath and session supplied! can only take one")

		} else if nzbfilepath != "" && s == nil {
			// we have a nzbfilepath but no session
			// no session supplied, create one!
			if sessId, newsession := p.newSession(nzbfilepath); sessId <= 0 || newsession == nil {
				return fmt.Errorf("error LaunchSession: sessId <= 0 ?! nzbfilepath='%s' newsession='%#v' err='%v'", nzbfilepath, newsession, err)
			} else {
				// we have a new session
				dlog(cfg.opt.Verbose, "LaunchSession: created new session with sessId=%d nzbfilepath='%s'", sessId, nzbfilepath)
				s = newsession
			}

		} else if nzbfilepath == "" && s != nil {
			// we have no nzbfilepath but a session
			// pass: we have "s" as session!
		} else {
			return fmt.Errorf("error LaunchSession: uncatched bug in launch checks: nzbfilepath='%s' s='%#v'", nzbfilepath, s)
		}
	} // end if !proxy

	if s == nil {
		return fmt.Errorf("error LaunchSession: session is nil, can't start")
	}
	if s.stopped() {
		// sessions are single-use: their stopChan and channels are closed for good
		return fmt.Errorf("error LaunchSession: session %d has already run, create a new session", s.sessId)
	}
	s.mux.Lock()
	if s.preBoot || s.active {
		// we are already booting or active
		s.mux.Unlock()
		return fmt.Errorf("error LaunchSession: session is already booting or active, can't start again")
	}
	// passed input checks and we should have a session now!
	s.preBoot = true
	s.mux.Unlock()

	// create log file: /path/nzb.log. Only for NZB sessions: the proxy and web
	// sessions have no NZB (they wrote to logs/.log) and never return to restore it.
	// The log is process-wide: while a web job runs, the web server logs there too.
	if cfg.opt.Log && !s.proxy && s.nzbPath != "" {
		logFileName := strings.TrimSuffix(filepath.Base(s.nzbPath), filepath.Ext(filepath.Base(s.nzbPath))) + ".log"
		logPath := filepath.Join(cfg.opt.LogDir, logFileName)
		if cfg.opt.LogDir != "" && cfg.opt.LogDir != "." && !DirExists(cfg.opt.LogDir) {
			if !Mkdir(cfg.opt.LogDir) {
				return fmt.Errorf("error LaunchSession: unable to create log dir '%s': %v", cfg.opt.LogDir, err)
			}
		}

		RotateLogFiles(logPath, cfg.opt.LogOld)
		SetLogToTerminal() // set log output to stdout first
		dlog(always, "LaunchSession: Writing log to: '%s'", logPath)
		err := LogToFile(logPath, cfg.opt.LogAppend) // set log output to the log file
		if err != nil {
			return fmt.Errorf("error LaunchSession: unable to open log file: %v", err)
		}
		defer SetLogToTerminal() // reset log output to stdout after the session is done
	}

	if !s.proxy {
		dlog(cfg.opt.Verbose, "LaunchSession Settings: '%#v'", *cfg.opt) //
		s.preparationStartTime = time.Now()
		dlog(cfg.opt.Verbose, "LaunchSession Loading NZB: '%s'", s.nzbPath)

		if s.nzbFile != nil {
			// nzbfile is still open and loaded, pass

		} else if s.nzbFile == nil && s.nzbPath != "" {
			// we have a nzbfilepath and no nzbFile loaded yet
			// nzbfile has not been opened and parsed before (or has been closed)
			nzbfile, err := loadNzbFile(s.nzbPath)
			if err != nil || nzbfile == nil {
				return fmt.Errorf("error unable to load file s.nzbPath='%s' err=%v'", s.nzbPath, err)
			}
			s.nzbFile = nzbfile

			dlog(cfg.opt.Debug, "nzbfile='%#v'", s.nzbFile)

			// build the segment list locally and publish it once under s.mux below
			segmentList := make([]*segmentChanItem, 0, nzbfile.TotalSegments)
			// loop through all file tags within the NZB file
			for _, file := range nzbfile.Files {
				if cfg.opt.Debug {
					fmt.Printf(">> nzbfile file='%#v'\n\n", file)
				}
				s.fileStatLock.Lock()
				s.fileStat[file.Filename] = new(fileStatistic)
				s.fileStat[file.Filename].available = make(providerStatistic)
				s.fileStat[file.Filename].totalSegments = uint64(file.TotalSegments)
				s.fileStatLock.Unlock()
				// loop through all segment tags within each file tag
				if cfg.opt.Debug {
					for _, agroup := range file.Groups {
						if agroup != "" && !slices.Contains(s.nzbGroups, agroup) {
							s.nzbGroups = append(s.nzbGroups, agroup)
						}
					}
					dlog(always, "NewsGroups: %v", s.nzbGroups)
				}
				// filling s.segmentList
				for _, segment := range file.Segments {
					dlog(cfg.opt.BUG, "append nzb to segmentList: Id='%s' file='%s'", segment.Id, file.Filename)
					// If you add more fields to the 'segmentChanItem' struct, the compiler will catch missing initializations here and crash on compilation.
					// mux := new(sync.RWMutex)
					segmux := &loggedrwmutex.LoggedSyncRWMutex{Name: "segment-" + segment.Id} // use a logged sync mutex to log locking and unlocking
					// create a new segmentChanItem for each segment
					item := &segmentChanItem{
						segmux, s, &segment, &file, // sync.RWMutex, *SESSION, *nzbparser.Segment, *nzbparser.File
						"",                                    //  string fields: 1. hashedId will be filled below
						&s.nzbHashedname,                      // *string fields
						make(chan int, 1), make(chan bool, 1), // chan fields
						// map fields for segment status
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						make(map[int]bool, len(s.providerList)),
						[]string{}, []string{}, []string{}, // []string fields
						false, false, false, false, false, false, false, false, false, // bool fields
						0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0} // int fields
					if len(segment.Id) < 3 { // a@a
						dlog(always, "ERROR LaunchSession: segment.Id='%s' is too short! file='%s'", segment.Id, file.Filename)
						continue
					}
					if !strings.HasPrefix(segment.Id, "<") && !strings.HasSuffix(segment.Id, ">") {
						item.segment.Id = "<" + segment.Id + ">" // ensure segment.Id is always in the format "<Id>" for consistency
					}
					item.hashedId = SHA256str(item.segment.Id)
					segmentList = append(segmentList, item)
				}
			}
			s.mux.Lock()
			s.segmentList = segmentList
			s.mux.Unlock()
			s.progress.total.Store(uint64(len(segmentList)))

			mibsize := float64(nzbfile.Bytes) / 1024 / 1024
			artsize := mibsize / float64(len(segmentList)) * 1024
			s.nzbSize = nzbfile.Bytes // store the size of the nzb file in bytes
			dlog(always, "%s [%s] loaded NZB: '%s' [%d/%d] ( %.02f MiB | ~%.0f KiB/segment )", appName, appVersion, s.nzbName, len(segmentList), nzbfile.TotalSegments, mibsize, artsize)
			// left-padding for log output
			s.digStr = fmt.Sprintf("%d", len(segmentList))
			s.D = fmt.Sprintf("%d", len(s.digStr))
		} // end if s.nzbFile
	} // end if !s.proxy

	// re-load the provider list
	var workerWGconnReady sync.WaitGroup // workerWGconnReady is used to wait for all connections to be established before starting the work
	// loadProviderList replaces s.providerList and s.providerGroups under s.mux
	if err := cfg.loadProviderList(s, &workerWGconnReady); err != nil {
		dlog(always, "ERROR unable to load providerfile '%s' err='%v'", cfg.opt.ProvFile, err)
		return err
	}
	totalMaxConns := 0
	for _, provider := range s.providerList {
		totalMaxConns += provider.MaxConns
	}

	dlog(cfg.opt.Debug, "(Re-)Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(s.preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)

	globalmux.Lock()
	// setup mem limiter
	if memlim == nil {
		memMax := cfg.opt.MemMax
		// sets memory slots once globally, sized by the first session's providers
		// (for -web that is the web session at startup; later jobs share it)
		if memMax <= 0 && totalMaxConns > 0 {
			if memMax != totalMaxConns*2 {
				memMax = totalMaxConns * 2
			}
		}
		memlim = NewMemLimiter(memMax)
	}

	// core_chan / async_core_chan are set up once by ParseFlags (setupCoreLimiters):
	// resizing them here while another session holds tokens lost or blocked tokens.
	globalmux.Unlock()

	if s.proxy {
		// proxy/web sessions never boot workers: release the pool goroutines
		// waiting on workerWGconnReady (one Add per provider in loadProviderList)
		for range s.providerList {
			workerWGconnReady.Done()
		}
		if s.ready != nil {
			close(s.ready) // providers and pools are up: the proxy may start its listeners
		}
		// infinite block here for proxy server. if a wg is supplied it will never be released
		_, ok := <-s.stopChan
		if !ok {
			dlog(always, "LaunchSession: proxy server stopChan closed, exiting LaunchSession")
			return
		}
	}
	dlog(cfg.opt.Debug, "Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(s.preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)

	// setup wait groups
	var waitWorker sync.WaitGroup      // waitWorker is used to wait for all workers to finish
	var waitDivider sync.WaitGroup     // waitDivider is used to block GoWorkDivider() until all workers are ready
	var waitDividerDone sync.WaitGroup // waitDividerDone is used to signal that GoWorkDivider() has quit
	var waitPool sync.WaitGroup        // waitPool waits until all workers closed their connections and routines

	// prepare wait groups
	waitDivider.Add(1)
	waitDividerDone.Add(1)
	for _, provider := range s.providerList {
		//waitWorker.Add(1)        // this 1 releases in GoWorker when routines have booted
		// workerWGconnReady: #1 for every provider was already added in loadProviderList
		for i := 1; i <= provider.MaxConns; i++ {
			//workerWGconnReady.Add(1) // #2 for every connection per provider
			waitWorker.Add(3) // for the 3 routines per worker in go func will release when routines stop
			waitPool.Add(1)   // for GoWorker() itself
		}
	}
	// run the go routines
	s.GoBootWorkers(&waitDivider, &workerWGconnReady, &waitWorker, &waitPool, s.nzbFile.Bytes)
	workerWGconnReady.Wait() // waits for all connections to be established before starting the work

	s.segmentCheckStartTime = time.Now()
	// booting work divider
	go s.GoWorkDivider(&waitDivider, &waitDividerDone)
	dlog(always, "sess: waiting on waitDividerDone.Wait()")
	waitDividerDone.Wait()
	dlog(always, "sess: released waitDividerDone.Wait()")

	s.StopRoutines()

	dlog(always, "sess: waiting on waitWorker.Wait()")
	waitWorker.Wait()
	dlog(always, "sess: released waitWorker.Wait(), waiting on waitPool.Wait()")
	waitPool.Wait()
	s.releaseMemSlots() // no worker is left; don't let leaked slots shrink memlim for the next session
	dlog(always, "sess: released waitPool.Wait() closing all provider connections")
	for _, provider := range s.providerList {
		KillConnPool(provider) // close the connection pool for this provider
	}

	result, runtime_info := s.Results(s.preparationStartTime)

	s.YencMerge(&result)

	result = runtime_info + "\n> ###" + result + "\n\n> ###\n\n:end"

	s.results = append(s.results, result)
	log.Print(result)

	s.writeCsvFile()

	s.clearSession() // goes to remove session if threshold is reached, otherwise park it which keeps some vars loaded
	return
} // end func LaunchSession

func (p *PROCESSOR) SetDirRefresh(seconds int64) {
	if seconds < 0 {
		seconds = 0
	} else if seconds > 0 && seconds < 15 {
		seconds = 15 // hardcoded minimum scan interval is 15 seconds!
	}
	p.mux.Lock()
	p.refresh = time.Duration(seconds) * time.Second
	p.mux.Unlock()
	dlog(always, "SetWatchDirRefresh: seconds=%d nzbDir='%s' ", seconds, p.nzbDir)
} // end func SetWatchDirRefresh

// private PROCESSOR/SESSION functions

// sessionProgress holds the counts of GoWorkDivider's last pass over the segment
// list. The web UI reads them instead of walking segmentList under locks (plan 4.3).
type sessionProgress struct {
	total, checked, done, dead, cached, downloaded, uploaded, notarget atomic.Uint64
}

func (p *PROCESSOR) newSession(nzbName string) (uint64, *SESSION) {
	// create session, every nzbfile will have its own session
	// this is called from LaunchSession and watchDirThread
	// nzbName is the name of the nzb file, e.g. "test.nzb" or "test.nzb.gz"
	//
	s := &SESSION{
		proc:                 p,                                                            // the parent processor
		mux:                  &loggedrwmutex.LoggedSyncRWMutex{Name: "SESSION-" + nzbName}, // use a logged sync mutex to log locking and unlocking
		preparationStartTime: time.Now(),
		providerList:         make([]*Provider, 0, 8), // will be filled later
		sessId:               p.newssid(),
		counter:              NewCounter(10),
		nzbName:              filepath.Base(nzbName),
		nzbHashedname:        SHA256str(filepath.Base(nzbName)),
		nzbPath:              filepath.Join(p.nzbDir, nzbName),
		fileStat:             make(filesStatistic),
		WorkDividerChan:      make(chan *WrappedItem, max(cfg.opt.ChanSize, 0)), // -chansize may be <= 0
		stopChan:             make(chan struct{}, 1),                            // stopChan is used to stop the session/routines
	}

	// add session to processor map
	p.mux.Lock()
	/*
		if p.sessMap == nil {
			// needs this check here again or we can into a nil ptr here
			// if we call only launchsessions for a single nzb
			p.sessMap = make(map[uint64]*SESSION, 1)
		}
	*/
	p.sessMap[s.sessId] = s
	sessId := s.sessId
	p.mux.Unlock()
	dlog(cfg.opt.Debug, "newSession: created sessId=%d nzbName='%s' nzbPath='%s'", sessId, s.nzbName, s.nzbPath)
	return sessId, s
} // end func newSession

func (p *PROCESSOR) newssid() uint64 {
	p.mux.Lock()
	p.sessIds++
	newSessId := p.sessIds
	p.mux.Unlock()
	dlog(cfg.opt.Debug, "PROCESSOR.newssid: %d", newSessId)
	return newSessId
} // end func p.newssid

// If the session map exceeds the threshold, it removes the session from the processor map.
// If the session is below the threshold, it closes the session partly.
func (s *SESSION) clearSession() {
	s.proc.mux.RLock()
	sessions := len(s.proc.sessMap) // the web job queue adds and removes sessions concurrently
	s.proc.mux.RUnlock()
	if sessions > cfg.opt.SessThreshold {
		s.proc.removeSession(s.sessId, s) // remove session from processor map
		return
	}
	s.closeSession()
} // end func clearSession

func (s *SESSION) closeSession() {
	// close session partly, keeps the session in the processor map
	// and some vars loaded
	// since processing the watchDir is not working yet this is just a idea for the future
	// we always have only 1 session running at a time and then quit... stop here!

	dlog(cfg.opt.Debug, "closeSession: sessId=%d nzbName='%s' nzbPath='%s'", s.sessId, s.nzbName, s.nzbPath)

	// The segment channels are not closed here: each has a single close owner
	// (the group's feeder, or closeWorkChans via StopRoutines), see the SESSION struct.
	// Closing them again here panicked with "close of closed channel".
	// The maps are dropped below under s.mux.

	// todo cleanup memDL and memUP
	/*
		for n, ch := range s.memDL {
			close(ch)
			s.memDL[n] = nil // remove from map
		}

		for n, ch := range s.memUP {
			close(ch)
			s.memUP[n] = nil // remove from map
		}
	*/

	dlog(cfg.opt.Debug, "closeSession: cleaning up sessId=%d nzbName='%s' nzbPath='%s'", s.sessId, s.nzbName, s.nzbPath)

	s.mux.Lock()            // lock the session for cleanup
	s.active = false        // set active to false, we are no longer processing
	s.checkFeedDone = false // reset checkFeedDone to false
	s.segcheckdone = false  // reset segcheckdone to false
	s.preBoot = false       // reset preBoot to false
	s.nzbFile = nil         // reset nzbFile to nil
	//s.nzbGroups = nil // reset nzbGroups to nil
	//s.memDL = nil
	//s.memUP = nil
	s.providerGroups = nil    // reset providerGroups to nil
	s.segmentList = nil       // reset segmentList to nil
	s.segmentChansCheck = nil // reset segmentChansCheck to nil
	s.segmentChansDowns = nil // reset segmentChansDowns to nil
	s.segmentChansReups = nil // reset segmentChansReups to nil

	s.preparationStartTime = time.Time{} // reset preparationStartTime to zero value
	s.fileStat = make(filesStatistic)    // reset fileStat to empty map
	s.closed = time.Now()                // set end time to now
	//s.sessId = s.proc.newssid()        // reset sessId to next one
	dlog(cfg.opt.Verbose, "Session cleaned up: sessId=%d nzbName='%s' nzbPath='%s' runtime=[%v]", s.sessId, s.nzbName, s.nzbPath, time.Since(s.preparationStartTime).Seconds())
	s.mux.Unlock()
} // end func cleanupSession

// removeSession removes a session from the PROCESSOR's session map.
// It is called when a session is no longer needed, e.g., after processing is complete.
func (p *PROCESSOR) removeSession(sessId uint64, s *SESSION) {
	if s == nil {
		dlog(cfg.opt.Debug, "removeSession: sessId=%d s is nil, nothing to remove", sessId)
		return
	}
	// threshold reached, we removed the session from the processor map
	p.mux.Lock()
	delete(p.sessMap, sessId)
	p.mux.Unlock()

	s.mux.Lock()        // lock the session for cleanup
	s.nzbGroups = nil   // reset nzbGroups
	s.fileStat = nil    // reset fileStat
	s.segmentList = nil // reset segmentList
	s.mux.Unlock()      // unlock the session
	s = nil             // set session to nil to free memory
	dlog(cfg.opt.Debug, "PROCESSOR.removeSession: sessId=%d", sessId)
} // end func p.removeSession

func (p *PROCESSOR) processorThread() {
	// watchDir is running concurrently
	// TODO: load nzbs and distribute work here
forever:
	for {
		select {
		case stopSignal, ok := <-p.stopChan:
			if !ok {
				dlog(always, "ERROR PROCESSOR: stopChan closed, exiting processorThread")
				return
			}
			p.stopChan <- stopSignal
			break forever
		case <-time.After(15 * time.Second):
			// every 15 seconds check if we have new files in the nzbDir
		}
	}
	dlog(always, "Quit PROCESSOR: dir='%s'", p.nzbDir)
} // end func p.processorThread

func (p *PROCESSOR) refreshDir() {
	fs, err := os.ReadDir(p.nzbDir)
	if err != nil {
		log.Fatal(err)
	}
	var newfiles []*string
	for _, file := range fs {
		filename := file.Name()
		isNZB := (strings.HasSuffix(filename, ".nzb") || strings.HasSuffix(filename, "nzb.gz"))
		if isNZB {
			p.mux.RLock()
			if p.seenFiles[filename] {
				p.mux.RUnlock()
				continue
			}
			p.mux.RUnlock()

			p.mux.Lock()
			// Add the file to the slice and mark it as seen
			p.seenFiles[filename] = true
			p.nzbFiles = append(p.nzbFiles, filename)
			p.mux.Unlock()

			newfiles = append(newfiles, &filename)

			if sessId, _ := p.newSession(filename); err != nil || sessId <= 0 {
				dlog(always, "ERROR refreshDir sessId=%d <= 0? err='%v'", sessId, err)
			}

		}
	} // end for file fs

	if len(newfiles) == 0 {
		dlog(cfg.opt.Verbose, "refreshDir: no new files found...")
		return
	}

	if cfg.opt.Verbose {
		dlog(always, "refreshDir loaded new files: %d dir='%s'", len(newfiles), p.nzbDir)
		for _, fn := range newfiles {
			dlog(always, "refreshDir: '%s' New: '%s'", p.nzbDir, *fn)
		}
	}

} // end func p.refreshDir

func (p *PROCESSOR) watchDirThread() {
	tAchan := time.After(5 * time.Second)
	dlog(always, "Starting watchDir: '%s' first lookup in 5 sec", p.nzbDir)
forever:
	for {
		select {
		case nul := <-p.stopChan:
			p.stopChan <- nul
			break forever

		case <-tAchan:
			//dlog( "watchDir checking '%s'", p.nzbDir)
			// read p.refresh under a short lock only:
			// refreshDir() and newSession() take p.mux themselves (not reentrant).
			p.mux.RLock()
			refresh := p.refresh
			p.mux.RUnlock()
			if refresh > 0 {
				p.refreshDir()
				tAchan = time.After(refresh)
			} else {
				// if p.refresh set to <= 0 don't run refreshDir
				// but recheck every 15 seconds if value has been updated
				tAchan = time.After(15 * time.Second)
			}
		}
	} // end for
	dlog(always, "Processor watchDir '%s' quit", p.nzbDir)
} // end func watchDir
