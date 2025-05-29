package main

/*
 *   still all TODO !
 */

import (
	"fmt"

	"github.com/Tensai75/nzbparser"

	//"io/fs"
	//"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

type File struct {
	path string
	open bool
	mux  sync.RWMutex
}

type PROCESSOR struct {
	IsRunning bool
	cfg       *Config
	mux       sync.RWMutex
	sessIds   uint64              // counts up
	sessMap   map[uint64]*SESSION // map with sessions
	nzbDir    string              // watch this dir for nzb files
	seenFiles map[string]bool     // map to keep track of already seen/added files
	nzbFiles  []string            // watched and added from nzbDir
	refresh   time.Duration       // update list of nzb dir files every N seconds
	limitA    int                 // limits amount of open sessions
	stop_chan chan struct{}
	//waitSessionMap     map[uint64]*sync.WaitGroup
} // end type PROCESSOR struct

// SESSION holds a (loaded) nzb file
type SESSION struct {
	mux                   sync.RWMutex
	counter               *Counter_uint64                  // private counter for every session
	proc                  *PROCESSOR                       // the parent where we belong to
	sessId                uint64                           // session ID
	nzbName               string                           // test.nzb
	nzbHash               string                           // the hashed name of the full filename with .extension
	nzbPath               string                           // /path/to/nzbDir/test.nzb(.gz)
	nzbFile               *nzbparser.Nzb                   // the parsed NZB file structure
	nzbSize               int                              // the size of the nzb
	nzbGroups             []string                         // informative
	lastRun               int64                            // time.Now().Unix()
	nextRun               int64                            // time.Now().Unix()
	results               []string                         // stores the results from Results()
	digStr                string                           // cosmetics for results print
	D                     string                           // cosmetics for results print
	segmentList           []*segmentChanItem               // list holds the segments from parsed nzbFile
	segmentChansCheck     map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansDowns     map[string]chan *segmentChanItem // map[provider.Group]chan
	segmentChansReups     map[string]chan *segmentChanItem // map[provider.Group]chan
	memDL                 map[string]chan *segmentChanItem // with -checkfirst queues items here
	memUP                 map[string]chan *segmentChanItem // TODO: process uploads after downloads (possible only with cacheON)
	segCR                 map[string]chan *segmentChanItem // map[provider.Group]segmentChanReups
	active                bool                             // will be true when processing starts
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
		return fmt.Errorf("ERROR This Processor: is already setup!")
	}

	if cfg.opt.NzbDir != "" {
		retries := 3
		for {
			if DirExists(cfg.opt.NzbDir) {
				break
			}
			if retries <= 0 {
				return fmt.Errorf("ERROR NewProcessor: watchDir not found: '%s'", cfg.opt.NzbDir)
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
	// supply nzbfilepath and no session as nil to run a single file (from cmd line flags) and quit
	// this LaunchSession function will block as long as it runs this nzbfile!
	// to call LaunchSession with "go .." into the background: supply a waitgroup and wait!
	// but we can only have a single session active at any time!

	if waitSession != nil {
		defer waitSession.Done()
	}

	// checks for the correct inputs
	if nzbfilepath == "" && s == nil {
		return fmt.Errorf("ERROR LaunchSession: need nzbfilepath or session")
	} else if nzbfilepath != "" && s != nil {
		return fmt.Errorf("ERROR LaunchSession: nzbfilepath and session supplied! can only take one!")
	} else if nzbfilepath != "" && s == nil {
		// no session supplied, create one!
		if sessId, newsession := p.newSession(nzbfilepath); sessId <= 0 || newsession == nil {
			return fmt.Errorf("ERROR LaunchSession: sessId <= 0 ?! nzbfilepath='%s' newsession='%#v' err='%v'", nzbfilepath, newsession, err)
		} else {
			s = newsession
		}
	} else if nzbfilepath == "" && s != nil {
		// pass: we have "s" as session!
	} else {
		return fmt.Errorf("ERROR LaunchSession: uncatched bug in launch check!")
	}
	// passed input checks and we should have a session now!
	s.mux.Lock()
	s.active = true
	s.mux.Unlock()

	// create log file: /path/nzb.log
	if cfg.opt.Log {
		logFileName := strings.TrimSuffix(filepath.Base(s.nzbPath), filepath.Ext(filepath.Base(s.nzbPath))) + ".log"
		logFile, err := os.Create(logFileName)
		if err != nil {
			return fmt.Errorf("ERROR LaunchSession: unable to open log file: %v", err)
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

	preparationStartTime := time.Now()
	if cfg.opt.Debug {
		log.Printf("LaunchSession Loading NZB: '%s'", s.nzbPath)
	}

	if s.nzbFile == nil {
		// nzbfile has not been opened and parsed before (or has been closed)
		nzbfile, err := loadNzbFile(s.nzbPath)
		if err != nil || nzbfile == nil {
			return fmt.Errorf("ERROR unable to load file s.nzbPath='%s' err=%v'", s.nzbPath, err)
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
					log.Printf("reading nzb: Id='%s' file='%s'", segment.Id, file.Filename)
				}
				// if you add more variables to 'segmentChanItem struct': compiler always fails here!
				// we could supply neatly named vars but then we will forget one if updating the struct and app will crash...
				item := &segmentChanItem{
					s, &segment, &file,
					make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)), make(map[int]bool, len(s.providerList)),
					sync.RWMutex{}, nil, false, false, false, false, false, false, false, false,
					0, SHA256str("<" + segment.Id + ">"), false, make(chan int, 1), make(chan bool, 1), 0, 0, 0, 0, 0, &s.nzbHash}
				s.segmentList = append(s.segmentList, item)
			}
		}

		mibsize := float64(nzbfile.Bytes) / 1024 / 1024
		artsize := mibsize / float64(len(s.segmentList)) * 1024

		log.Printf("%s [%s] loaded NZB: '%s' [%d/%d] ( %.02f MiB | ~%.0f KiB/segment )", appName, appVersion, s.nzbName, len(s.segmentList), nzbfile.TotalSegments, mibsize, artsize)

	} // end if s.nzbFile == nil

	// left-padding for log output
	s.digStr = fmt.Sprintf("%d", len(s.segmentList))
	s.D = fmt.Sprintf("%d", len(s.digStr))

	// re-load the provider list
	s.providerList = nil
	if err := s.loadProviderList(); err != nil {
		log.Printf("ERROR unable to load providerfile '%s' err='%v'", cfg.opt.ProvFile, err)
		return err
	}
	totalMaxConns := 0
	for _, provider := range s.providerList {
		totalMaxConns += provider.MaxConns
	}

	if cfg.opt.Debug {
		log.Printf("(Re-)Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)
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
		log.Printf("Loaded s.providerList: %d ... preparation took '%v' | cfg.opt.MemMax=%d totalMaxConns=%d", len(s.providerList), time.Since(preparationStartTime).Milliseconds(), cfg.opt.MemMax, totalMaxConns)
	}

	var waitWorker sync.WaitGroup
	var workerWGconnEstablish sync.WaitGroup
	var waitDivider sync.WaitGroup
	var waitDividerDone sync.WaitGroup
	var waitPool sync.WaitGroup

	/* in *SESSION
	var segmentCheckStartTime time.Time
	var segmentCheckEndTime   time.Time
	var segmentCheckTook      time.Duration
	*/
	/* TODO: cache boots in main()
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
		if !cache.MkSubDir(s.nzbHash) {
			return fmt.Errorf("ERROR LaunchSession !cache.MkSubDir(s.nzbHash)")
		}
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

	// run the go routines
	waitDivider.Add(1)
	waitDividerDone.Add(1)
	workerWGconnEstablish.Add(1)
	s.GoBootWorkers(&waitDivider, &workerWGconnEstablish, &waitWorker, &waitPool, s.nzbFile.Bytes)

	if cfg.opt.Debug {
		log.Print("main: workerWGconnEstablish.Wait()")
	}
	workerWGconnEstablish.Wait()
	if cfg.opt.Debug {
		log.Print("main: workerWGconnEstablish.Wait() released: segmentCheckStartTime=now")
	}

	setTimerNow(&s.segmentCheckStartTime)
	// booting work divider
	go s.GoWorkDivider(&waitDivider, &waitDividerDone)
	if cfg.opt.Debug {
		log.Print("main: waitDividerDone.Wait()")
	}
	waitDividerDone.Wait()

	if cfg.opt.Debug {
		log.Print("main: waitDividerDone.Wait() released")
	}

	s.StopRoutines()

	if cfg.opt.Debug {
		log.Print("main: waitWorker.Wait()")
	}
	waitWorker.Wait()
	if cfg.opt.Debug {
		log.Print("main: waitWorker.Wait() released, waiting on waitPool.Wait()")
	}
	waitPool.Wait()
	/*
		if cfg.opt.Bar {
			progressBars.Wait()
		}
	*/
	if cfg.opt.Debug {
		log.Print("main: waitPool.Wait() released")
	}

	result, runtime_info := s.Results(preparationStartTime)

	s.YencMerge(&result)

	result = result + fmt.Sprintf(runtime_info+"\n> ###"+result+"\n\n> ###\n\n:end")

	s.results = append(s.results, result)
	//writeCsvFile()

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
	// create session
	s := &SESSION{
		sessId:   p.newssid(),
		counter:  NewCounter(10),
		nzbName:  filepath.Base(nzbName),
		nzbHash:  SHA256str(filepath.Base(nzbName)),
		nzbPath:  filepath.Join(p.nzbDir, nzbName),
		fileStat: make(filesStatistic),
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
	log.Printf("newSession: sessId=%d nzbName='%s' s='%#v'", sessId, nzbName, s)
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

func (p *PROCESSOR) delSession(sessId uint64) {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.sessMap[sessId].nzbFile != nil {
		p.sessMap[sessId].nzbFile = nil
		delete(p.sessMap, sessId)
		log.Printf("PROCESSOR.delSession: %d", sessId)
	}
} // end func p.deleteSession

func (p *PROCESSOR) processorThread() {
	p.mux.Lock()
	p.mux.Unlock()
	// watchDir is running concurrently
	// TODO: load nzbs and distribute work here
forever:
	for {
		select {
		case stopSignal := <-p.stop_chan:
			p.stop_chan <- stopSignal
			break forever
		}
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
				log.Printf("ERROR refreshDir sessId=%d <= 0? err='%v'", err)
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
