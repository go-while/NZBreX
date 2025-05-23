package main

import (
	"bufio"
	"encoding/json"
	"github.com/Tensai75/nzbparser"
	"log"
	"os"
	"strings"
	"sync"
)

const (
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF
	DefaultCacheRW = 100
	DefaultChanSize = 1000
	DefaultLogPrintEvery int64 = 5
	DefaultBufferSize = 256 * 1024
)

type (
	Config struct {
		providers []Provider
		opt       *CFG
	}

	//
	CFG struct {
		YencCpu          int
		NZBfilepath      string
		ProvFile         string
		Cachedir         string
		CheckCacheOnBoot bool
		CRW              int
		MemMax           int
		ChanSize         int
		CheckOnly        bool
		CheckFirst       bool
		UploadLater      bool
		Verify           bool
		YencCRC          bool
		YencTest         int
		YencWrite        bool
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
		segment     *nzbparser.NzbSegment
		file        *nzbparser.NzbFile
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
		flaginYenc  bool
		flagisYenc  bool
		checkedAt   int
		hashedId    string
		cached      bool
		readChan    chan int  // to notify, cache has loaded the file to item.lines
		checkChan   chan bool // to notify if item exists in cache
		size        int
		dlcnt       int
		badcrc      int
		fails       int
		retryIn     int64
		nzbhashname *string
	} // end segmentChanItem struct
) // end type

var (
	needHeaders = []string{
		"From:",
		"Subject:",
		"Newsgroups:",
		"Message-Id:",
	}
	cleanHeader = []string{
		"X-",
		"Date:",
		"Nntp-",
		"Path:",
		"Xref:",
		"Cancel-",
		"Injection-",
		"User-Agent:",
		"Organization:",
	}
)

// TODO!
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

func ReadHeadersFromFile(path string) ([]string, error) {
	if path == "" {
		// ignore silenty because flag is empty / not set
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	hasDate := false
	for _, line := range lines {
		for _, hdr := range needHeaders {
			if strings.HasPrefix(line, hdr) {
				log.Printf("ERROR can not load header '%s' to cleanup!", hdr)
				os.Exit(1)
			}
		}
		if strings.HasPrefix(line, "Date:") {
			hasDate = true
		}
	}
	if !hasDate {
		// we have to cleanup the Date header because we supply a new one!
		lines = append(lines, "Date:")
	}
	return lines, nil
} // end func ReadHeadersFromFile
