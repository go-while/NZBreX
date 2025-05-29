package main

/*
 *   still all TODO !
 */

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tensai75/nzbparser"
)

/*
	type File struct {
		path string
		open bool
		mux  sync.RWMutex
	}
*/

// PROCESSOR manages NZB file processing.
// It monitors a directory for NZB files, processes them, and maintains:
// - a map of active sessions (each representing an NZB file and its segments)
// - a record of seen files to prevent duplicate processing
type PROCESSOR struct {
	IsRunning bool
	//cfg       *Config // TODO: remove from global scope?
	mux       sync.RWMutex
	sessIds   uint64              // counts up
	sessMap   map[uint64]*SESSION // map with sessions
	nzbDir    string              // watch this dir for nzb files
	seenFiles map[string]bool     // map to keep track of already seen/added files
	nzbFiles  []string            // watched and added from nzbDir
	refresh   time.Duration       // update list of nzb dir files every N seconds
	//limitA    int                 // limits amount of open sessions
	stop_chan chan struct{}
	//waitSessionMap     map[uint64]*sync.WaitGroup
} // end type PROCESSOR struct

// SESSION represents a single NZB file processing session.
// It contains information about the NZB file, its segments, and the providers involved.
// Each session has its own ID, parsed NZB file, and a list of segments to process.
// It also manages channels for segment processing and holds statistics about the file.
// The SESSION struct is used to track the state of processing for each NZB file.
// It is created by the PROCESSOR when a new NZB file is detected and processed.
type SESSION struct {
	mux       sync.RWMutex
	counter   *Counter_uint64 // private counter for every session // TODO check all GCounter if they should be private
	proc      *PROCESSOR      // the parent where we belong to
	sessId    uint64          // session ID
	nzbName   string          // test.nzb
	nzbHash   string          // the hashed name of the full filename with .extension
	nzbPath   string          // /path/to/nzbDir/test.nzb(.gz)
	nzbFile   *nzbparser.Nzb  // the parsed NZB file structure
	nzbSize   int64           // the byte size of all the files in the nzb. will be > 0 if nzbFile is loaded or has been loaded before
	nzbGroups []string        // informative
	//lastRun               int64                            // time.Now().Unix()
	//nextRun               int64                            // time.Now().Unix()
	results               []string                         // stores the results from Results()
	digStr                string                           // cosmetics for results print
	D                     string                           // cosmetics for results print
	segmentList           []*segmentChanItem               // list holds the segments from parsed nzbFile
	segmentChansCheck     map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansDowns     map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansReups     map[string]chan *segmentChanItem // map[provider.Group]chan
	memDL                 map[string]chan *segmentChanItem // with -checkfirst queues items here
	memUP                 map[string]chan *segmentChanItem // TODO: process uploads after downloads (possible only with cacheON)
	preBoot               bool                             // will be set to false before workers start
	active                bool                             // will be set to true when processing starts // REVIEW!TODO!FIXME: set to false is missing!!
	preparationStartTime  time.Time
	segmentCheckStartTime time.Time
	segmentCheckEndTime   time.Time
	segmentCheckTook      time.Duration
	providerList          []*Provider
	fileStat              filesStatistic
	fileStatLock          sync.Mutex

	//waitSession        *sync.WaitGroup // this links to p.waitSessionMap // TODO?REVIEW
} // end type SESSION struct

func (p *PROCESSOR) NewProcessor() error {
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
			log.Printf("WARN NewProcessor: watchDir not found: '%s' ... retry in 9s (retries %d)", cfg.opt.NzbDir, retries)
			time.Sleep(9 * time.Second)
			retries--
		}
		//p.refresh = time.Duration(0 * time.Second) // default. call processor.SetDirRefresh after NewProcessor
		p.nzbDir = cfg.opt.NzbDir
		p.seenFiles = make(map[string]bool, 128)
		go p.watchDirThread()
	}
	p.sessMap = make(map[uint64]*SESSION, 128)
	p.stop_chan = make(chan struct{}, 1)
	go p.processorThread()
	p.IsRunning = true
	log.Printf("NewProcessor: nzbDir='%s' refresh=%d", p.nzbDir, p.refresh)
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
			if cfg.opt.Debug {
				log.Printf("LaunchSession: created new session with sessId=%d nzbfilepath='%s'", sessId, nzbfilepath)
			}
			s = newsession
		}

	} else if nzbfilepath == "" && s != nil {
		// we have no nzbfilepath but a session
		// pass: we have "s" as session!
	} else {
		return fmt.Errorf("error LaunchSession: uncatched bug in launch checks: nzbfilepath='%s' s='%#v'", nzbfilepath, s)
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

	// create log file: /path/nzb.log
	if cfg.opt.Log {
		logFileName := strings.TrimSuffix(filepath.Base(s.nzbPath), filepath.Ext(filepath.Base(s.nzbPath))) + ".log"
		logFile, err := os.Create(logFileName)
		if err != nil {
			return fmt.Errorf("error LaunchSession: unable to open log file: %v", err)
		}
		log.SetOutput(os.Stdout)
		log.Printf("ERROR LaunchSession: Writing log to: '%s'", logFileName)
		log.SetOutput(logFile)
		defer log.SetOutput(os.Stdout)
	}

	/*
		if cfg.opt.Verbose {
			log.Printf("LaunchSession Settings: '%#v'", *cfg.opt) // TODO should be in main() once on boot!
		}
	*/

	s.preparationStartTime = time.Now()
	if cfg.opt.Debug {
		log.Printf("LaunchSession Loading NZB: '%s'", s.nzbPath)
	}

	if s.nzbFile != nil {
		// nzbfile is still open and loaded, pass

	} else if s.nzbFile == nil && nzbfilepath != "" {
		// we have a nzbfilepath and no nzbFile loaded yet
		// nzbfile has not been opened and parsed before (or has been closed)
		nzbfile, err := loadNzbFile(s.nzbPath)
		if err != nil || nzbfile == nil {
			return fmt.Errorf("error unable to load file s.nzbPath='%s' err=%v'", s.nzbPath, err)
		}
		s.nzbFile = nzbfile

		if cfg.opt.Debug {
			log.Printf("nzbfile='%#v'", s.nzbFile)
		}

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
				log.Printf("NewsGroups: %v", s.nzbGroups)
			}
			// filling s.segmentList
			for _, segment := range file.Segments {
				if cfg.opt.BUG {
					log.Printf("append nzb to segmentList: Id='%s' file='%s'", segment.Id, file.Filename)
				}
				// If you add more fields to the 'segmentChanItem' struct, the compiler will catch missing initializations here and crash on compilation.
				item := &segmentChanItem{
					sync.RWMutex{}, s, &segment, &file,
					make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)),
					nil, false, false, false, false, false, false, false, false,
					0, SHA256str("<" + segment.Id + ">"), false, make(chan int, 1), make(chan bool, 1), 0, 0, 0, 0, 0, &s.nzbHash}
				s.segmentList = append(s.segmentList, item)
			}
		}

		mibsize := float64(nzbfile.Bytes) / 1024 / 1024
		artsize := mibsize / float64(len(s.segmentList)) * 1024
		s.nzbSize = nzbfile.Bytes // store the size of the nzb file in bytes
		log.Printf("%s [%s] loaded NZB: '%s' [%d/%d] ( %.02f MiB | ~%.0f KiB/segment )", appName, appVersion, s.nzbName, len(s.segmentList), nzbfile.TotalSegments, mibsize, artsize)

	} // end if s.nzbFile

	// left-padding for log output
	s.digStr = fmt.Sprintf("%d", len(s.segmentList))
	s.D = fmt.Sprintf("%d", len(s.digStr))

	// re-load the provider list
	s.providerList = nil
	if err := cfg.loadProviderList(s); err != nil {
		log.Printf("ERROR unable to load providerfile '%s' err='%v'", cfg.opt.ProvFile, err)
		return err
	}
	totalMaxConns := 0
	for _, provider := range s.providerList {
		totalMaxConns += provider.MaxConns
	}

	if cfg.opt.Debug {
		log.Printf("(Re-)Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(s.preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)
	}

	globalmux.Lock()
	// setup mem limiter
	if memlim == nil {
		memlim = NewMemLimiter(cfg.opt.MemMax)
	}

	// set memory slots or update maxmem on reload
	if cfg.opt.MemMax <= 0 && totalMaxConns > 0 {
		if cfg.opt.MemMax != totalMaxConns*2 {
			cfg.opt.MemMax = totalMaxConns * 2
			if memlim != nil {
				memlim.SetMaxMem(totalMaxConns * 2)
			}
		}
	}

	// limits crc32 and yenc processing
	if cfg.opt.YencCpu <= 0 {
		cfg.opt.YencCpu = runtime.NumCPU()
	}
	if core_chan == nil || cap(core_chan) != cfg.opt.YencCpu {
		core_chan = make(chan struct{}, cfg.opt.YencCpu)
		for i := 1; i <= cfg.opt.YencCpu; i++ {
			core_chan <- struct{}{} // fill chan with empty structs to suck out and return
		}
	}
	globalmux.Unlock()

	if cfg.opt.Debug {
		log.Printf("Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(s.preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)
	}

	// setup wait groups
	var waitWorker sync.WaitGroup            // waitWorker is used to wait for all workers to finish
	var workerWGconnEstablish sync.WaitGroup // workerWGconnEstablish is used to wait for all connections to be established before starting the work
	var waitDivider sync.WaitGroup           // waitDivider is used to block GoWorkDivider() until all workers are ready
	var waitDividerDone sync.WaitGroup       // waitDividerDone is used to signal that GoWorkDivider() has quit
	var waitPool sync.WaitGroup              // waitPool waits until all workers closed their connections and routines

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

	// run the go routines
	waitDivider.Add(1)
	waitDividerDone.Add(1)
	workerWGconnEstablish.Add(1)
	s.GoBootWorkers(&waitDivider, &workerWGconnEstablish, &waitWorker, &waitPool, s.nzbFile.Bytes)

	if cfg.opt.Debug {
		log.Print("sess: workerWGconnEstablish.Wait()")
	}
	workerWGconnEstablish.Wait()
	if cfg.opt.Debug {
		log.Print("sess: workerWGconnEstablish.Wait() released: segmentCheckStartTime=now")
	}

	s.segmentCheckStartTime = time.Now()
	// booting work divider
	go s.GoWorkDivider(&waitDivider, &waitDividerDone)
	if cfg.opt.Debug {
		log.Print("sess: waitDividerDone.Wait()")
	}
	waitDividerDone.Wait()

	if cfg.opt.Debug {
		log.Print("sess: waitDividerDone.Wait() released")
	}

	s.StopRoutines()

	if cfg.opt.Debug {
		log.Print("sess: waitWorker.Wait()")
	}
	waitWorker.Wait()
	if cfg.opt.Debug {
		log.Print("sess: waitWorker.Wait() released, waiting on waitPool.Wait()")
	}
	waitPool.Wait()
	/*
		if cfg.opt.Bar {
			progressBars.Wait()
		}
	*/
	if cfg.opt.Debug {
		log.Print("sess: waitPool.Wait() released")
	}

	result, runtime_info := s.Results(s.preparationStartTime)

	s.YencMerge(&result)

	result = result + runtime_info + "\n> ###" + result + "\n\n> ###\n\n:end"

	s.results = append(s.results, result)

	s.writeCsvFile()

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
	log.Printf("SetWatchDirRefresh: seconds=%d nzbDir='%s' ", seconds, p.nzbDir)
} // end func SetWatchDirRefresh

// private PROCESSOR/SESSION functions

func (p *PROCESSOR) newSession(nzbName string) (uint64, *SESSION) {
	// create session, every nzbfile will have its own session
	// this is called from LaunchSession and watchDirThread
	// nzbName is the name of the nzb file, e.g. "test.nzb" or "test.nzb.gz"
	//
	s := &SESSION{
		proc:                 p,                                         // the parent processor
		segmentChansCheck:    make(map[string]chan *segmentChanItem, 8), // items to be checked will be sent to these channels. the key string is provider.Group
		segmentChansDowns:    make(map[string]chan *segmentChanItem, 8), // items to be downloaded will be sent to these channels. the key string is provider.Group
		segmentChansReups:    make(map[string]chan *segmentChanItem, 8), // items to be re-uploaded will be sent to these channels. the key string is provider.Group
		memDL:                make(map[string]chan *segmentChanItem, 8), // with -checkfirst queues items here
		memUP:                make(map[string]chan *segmentChanItem, 8), // TODO: process uploads after downloads (possible only with cacheON)
		preparationStartTime: time.Now(),
		providerList:         make([]*Provider, 0, 8), // will be filled later
		sessId:               p.newssid(),
		counter:              NewCounter(10),
		nzbName:              filepath.Base(nzbName),
		nzbHash:              SHA256str(filepath.Base(nzbName)),
		nzbPath:              filepath.Join(p.nzbDir, nzbName),
		fileStat:             make(filesStatistic),
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
	if cfg.opt.Debug {
		log.Printf("newSession: created sessId=%d nzbName='%s' nzbPath='%s'", sessId, s.nzbName, s.nzbPath)
	}
	return sessId, s
} // end func newSession

func (p *PROCESSOR) newssid() uint64 {
	p.mux.Lock()
	p.sessIds++
	newSessId := p.sessIds
	p.mux.Unlock()
	log.Printf("PROCESSOR.newssid: %d", newSessId)
	return newSessId
} // end func p.newssid

/*
func (p *PROCESSOR) delSession(sessId uint64) {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.sessMap[sessId].nzbFile != nil {
		p.sessMap[sessId].nzbFile = nil
		delete(p.sessMap, sessId)
		log.Printf("PROCESSOR.delSession: %d", sessId)
	}
} // end func p.deleteSession
*/

func (p *PROCESSOR) processorThread() {
	// watchDir is running concurrently
	// TODO: load nzbs and distribute work here
forever:
	for {
		stopSignal := <-p.stop_chan
		p.stop_chan <- stopSignal
		break forever
	}
	log.Printf("Quit PROCESSOR: dir='%s'", p.nzbDir)
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
				log.Printf("ERROR refreshDir sessId=%d <= 0? err='%v'", sessId, err)
			}

		}
	} // end for file fs

	if len(newfiles) == 0 && cfg.opt.Verbose {
		log.Printf("refreshDir: no new files found...")
		return
	}

	if cfg.opt.Verbose {
		log.Printf("refreshDir loaded new files: %d dir='%s'", len(newfiles), p.nzbDir)
		for _, fn := range newfiles {
			log.Printf("refreshDir: '%s' New: '%s'", p.nzbDir, *fn)
		}
	}

} // end func p.refreshDir

func (p *PROCESSOR) watchDirThread() {
	tAchan := time.After(5 * time.Second)
	log.Printf("Starting watchDir: '%s' first lookup in 5 sec", p.nzbDir)
forever:
	for {
		select {
		case nul := <-p.stop_chan:
			p.stop_chan <- nul
			break forever

		case <-tAchan:
			//log.Printf("watchDir checking '%s'", p.nzbDir)
			p.mux.RLock()
			if p.refresh > 0 {
				p.refreshDir()
				tAchan = time.After(p.refresh)
			} else {
				// if p.refresh set to <= 0 don't run refreshDir
				// but recheck every 15 seconds if value has been updated
				tAchan = time.After(15 * time.Second)
			}
			p.mux.RUnlock()
		}
	} // end for
	log.Printf("Processor watchDir '%s' quit", p.nzbDir)
} // end func watchDir
