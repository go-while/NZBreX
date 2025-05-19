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
	"strings"
	"sync"
	"time"
)

type File struct {
	path string
	open bool
}

type PROCESSOR struct {
	cfg         *Config
	mux         sync.RWMutex
	sessIds     uint64               // counts up
	sessMap     map[uint64]*SESSION  // map with sessions
	nzbDir      string               // watch this dir for nzb files
	seenFiles   map[string]bool      // map to keep track of already seen/added files
	nzbFiles    []string             // watched and added from nzbDir
	refresh     time.Duration        // update list of nzb dir files every N seconds
	limitA      int                  // limits amount of open sessions
	stop_chan   chan struct{}
} // end type PROCESSOR struct

// SESSION holds a (loaded) nzb file
type SESSION struct {
	mux       sync.RWMutex
	proc      *PROCESSOR     // the parent where we belong to
	sessId    uint64         // session ID
	nzbName   string         // test.nzb
	nzbPath   string         // /path/to/nzbDir/test.nzb(.gz)
	nzbFile   *nzbparser.Nzb // the parsed NZB file structure
	nzbSize   int            // the size of the nzb
	nzbGroups []string       // informative
} // end type SESSION struct

// processor := &PROCESSOR{}
// if err := processor.NewProcessor(nzbdir); err != nil { /* handle error */ }
func (p *PROCESSOR) NewProcessor(nzbDir string, refresh int64) (error) {
	p.mux.Lock()
	defer p.mux.Unlock()
	if p.nzbDir != "" {
		return fmt.Errorf("Error NewProcessor: is already setup!")
	}
	if nzbDir == "" {
		return fmt.Errorf("Error NewProcessor: nzbdir empty")
	}
	p.nzbDir = nzbDir
	p.sessMap = make(map[uint64]*SESSION, 128)
	p.seenFiles = make(map[string]bool, 128)
	p.stop_chan = make(chan struct{}, 1)
	if refresh > 0 {
		p.refresh = time.Duration(refresh) * time.Second
	} else {
		p.refresh = time.Duration(60 * time.Second)
	}
	go p.thread()
	go p.watchDir()
	log.Printf("NewProcessor: nzbDir='%s'", p.nzbDir)
	return nil
} // end func NewProcessor

func (p *PROCESSOR) SetWatchDirRefresh(newValue time.Duration) {
	p.mux.Lock()
	p.refresh = newValue
	p.mux.Unlock()
} // end func SetWatchDirRefresh


// private PROCESSOR functions

func (p *PROCESSOR) newSession(nzbName string) {
	s := &SESSION{
		sessId:  p.newssid(),
		nzbName: nzbName,
		nzbPath: filepath.Join(p.nzbDir, nzbName),
	}
	p.mux.Lock()
	p.sessMap[s.sessId] = s
	p.mux.Unlock()
	log.Printf("PROCESSOR.newSession: sessId=%d nzbName='%s'", s.sessId, nzbName)
} // end func newSession

func (p *PROCESSOR) delSession(sessId uint64) {
	p.mux.Lock()
	defer p.mux.Unlock()

	if p.sessMap[sessId].nzbFile != nil {
		p.sessMap[sessId].nzbFile = nil
		delete(p.sessMap, sessId)
		log.Printf("PROCESSOR.delSession: %d", sessId)
	}
} // end func p.deleteSession

func (p *PROCESSOR) newssid() uint64 {
	p.mux.Lock()
	p.sessIds++
	newSessId := p.sessIds
	p.mux.Unlock()
	//log.Printf("PROCESSOR.newssid: %d", newSessId)
	return newSessId
} // end func p.newssid

func (p *PROCESSOR) thread() {
	// TODO: watch nzbdir, load nzbs and distribute work here
	for {
		select {
			case nul := <-p.stop_chan:
				p.stop_chan <- nul
				return
		}
	}
	log.Printf("Quit PROCESSOR '%s'", p.nzbDir)
} // end func p.thread

// private PROCESSOR/SESSION functions

func (p *PROCESSOR) refreshDir() {
	fs, err := os.ReadDir(p.nzbDir)
	if err != nil {
		log.Fatal(err)
	}
	for _, file := range fs {
		isNZB := (strings.HasSuffix(file.Name(), ".nzb") || strings.HasSuffix(file.Name(), "nzb.gz"))
		if isNZB {
			p.mux.Lock()
			if !p.seenFiles[file.Name()] {
				// Add the file to the slice and mark it as seen
				p.seenFiles[file.Name()] = true
				p.nzbFiles = append(p.nzbFiles, file.Name())
				log.Printf("PROCESSOR.watchDir New file added: '%s'", file.Name())
				go p.newSession(file.Name())
			}
			p.mux.Unlock()
		}
	}
} // end func p.refreshDir

func (p *PROCESSOR) watchDir() {
	tAchan := time.After(1 * time.Second)
	log.Printf("Starting watchDir: '%s'", p.nzbDir)
	for {
		select {
		case nul := <-p.stop_chan:
			p.stop_chan <- nul
			return

		case <-tAchan:
			//log.Printf("watchDir checking '%s'", p.nzbDir)
			p.refreshDir()

			p.mux.RLock()
			tAchan = time.After(p.refresh)
			p.mux.RUnlock()
		}
	} // end for
	log.Printf("Processor watchDir '%s' quit", p.nzbDir)
} // end func watchDir
