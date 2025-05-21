package main

import (
	"encoding/json"
	"github.com/Tensai75/nzbparser"
	"os"
	"sync"
)

const (
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF
)

type (
	Config struct {
		providers []Provider
		opt       *CFG
	}

	//
	CFG struct {
		NZBfilepath      string
		ProvFile         string
		Cachedir         string
		CheckCacheOnBoot bool
		CRW              int
		MemMax           int
		CheckOnly        bool
		CheckFirst       bool
		UploadLater      bool
		Verify           bool
		YencCRC          bool
		Csv              bool
		Log              bool
		LogPrintEvery    int64
		Discard          bool
		CleanHeaders     bool
		CleanHeadersFile string
		BUG              bool // shows it all
		Debug            bool
		DebugCache       bool
		Verbose          bool
		Bar              bool
		Colors           bool
		MaxArtSize       int
		SloMoC           int
		SloMoD           int
		SloMoU           int
	} // end CFG struct

	//
	Provider struct {
		Enabled       bool
		NoUpload      bool
		Group         string
		Name          string
		Host          string
		Port          uint32
		SSL           bool
		SkipSslCheck  bool
		Username      string
		Password      string
		MaxConns      int
		TCPMode       string
		PreferIHAVE   bool
		MaxConnErrors uint32
		Conns         *ProviderConns
		mux           sync.RWMutex
		id            int // will be set in loadProviderList
		capabilities  struct {
			check  bool
			ihave  bool
			post   bool
			stream bool
		}
		articles struct {
			tocheck    uint64
			checked    uint64
			available  uint64
			downloaded uint64
			missing    uint64
			refreshed  uint64
			verified   uint64
		}
	} // end Provider struct

	//
	segmentChanItem struct {
		segment     nzbparser.NzbSegment
		missingOn   map[int]bool // [provider.id]bool
		availableOn map[int]bool // [provider.id]bool
		unwantedOn  map[int]bool // [provider.id]bool
		retryOn     map[int]bool // [provider.id]bool
		errorOn     map[int]bool // [provider.id]bool
		dmcaOn      map[int]bool // [provider.id]bool
		mux         sync.RWMutex
		lines       []string // contains the downloaded segment/article
		flaginDL    bool
		flagisDL    bool
		flaginUP    bool
		flagisUP    bool
		flaginDLMEM bool
		flaginUPMEM bool
		checkedAt   int
		hashedId    string
		cached      bool
		readChan    chan int  // to notify, cache has loaded the file to item.lines
		checkChan   chan bool // to notify if item exists in cache
		segnum      int
		size        int
		dlcnt       int
		badcrc      int
		fails       int
		retryIn     int64
		nzbhashname *string
	} // end segmentChanItem struct
) // end type

func loadConfigFile(path string) (*CFG, error) {
	if file, err := os.ReadFile(path); err != nil {
		return nil, err
	} else {
		var loadedconfig CFG
		if err := json.Unmarshal(file, &loadedconfig); err != nil {
			return nil, err
		} else {
			return &loadedconfig, nil
		}
	}
} // end func loadConfigFile
