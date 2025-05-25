package main

import (
	"bufio"
	"encoding/json"
	"github.com/Tensai75/nzbparser"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	DOT                          = "."
	CR                           = "\r"
	LF                           = "\n"
	CRLF                         = CR + LF
	DefaultCacheRW               = 8
	DefaultChanSize              = 1000
	DefaultPrintStats      int64 = 5
	DefaultCacheWriteBuffer      = 256 * 1024
	DefaultYencWriteBuffer       = 256 * 1024
	DefaultMaxArticleSize        = 1 * 1024 * 1024
	DefaultConnectTimeout        = 9 * time.Second
	DefaultConnectErrSleep       = 9 * time.Second
)

type Config struct {
	providers []Provider
	opt       *CFG
}

type CFG struct {
	NZBfilepath      string
	ProvFile         string `json:"Provider"`
	Cachedir         string `json:"Cachedir"`
	CheckCacheOnBoot bool   `json:"CheckCacheOnBoot"`
	ByPassSTAT       bool   `json:"ByPassSTAT"`
	CRW              int    `json:"CRW"`
	MemMax           int    `json:"MemMax"`
	ChanSize         int    `json:"ChanSize"`
	CheckOnly        bool   `json:"CheckOnly"`
	CheckFirst       bool   `json:"CheckFirst"`
	UploadLater      bool   `json:"UploadLater"`
	Verify           bool   `json:"Verify"`
	YencCRC          bool   `json:"YencCRC"`
	YencCpu          int    `json:"YencCpu"`
	YencTest         int    `json:"YencTest"`
	YencWrite        bool   `json:"YencWrite"`
	YencMerge        bool   `json:"YencMerge"`
	YencDelParts     bool   `json:"YencDelParts"`
	Csv              bool   `json:"Csv"`
	Log              bool   `json:"Log"`
	PrintStats       int64  `json:"PrintStats"`
	Print430         bool   `json:"Print430"`
	Discard          bool   `json:"Discard"`
	CleanHeaders     bool   `json:"CleanHeaders"`
	CleanHeadersFile string `json:"CleanHeadersFile"`
	BUG              bool   `json:"BUG"`
	Debug            bool   `json:"Debug"`
	DebugCache       bool   `json:"DebugCache"`
	Verbose          bool   `json:"Verbose"`
	Bar              bool   `json:"Bar"`
	Colors           bool   `json:"Colors"`
	MaxArtSize       int    `json:"MaxArtSize"`
	SloMoC           int    `json:"SloMoC"`
	SloMoD           int    `json:"SloMoD"`
	SloMoU           int    `json:"SloMoU"`
} // end CFG struct

type Provider struct {
	Enabled       bool
	NoUpload      bool
	NoDownload    bool
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
	MaxConnErrors int // was uint32 before but need -1 for infinite retries...
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

type segmentChanItem struct {
	segment     *nzbparser.NzbSegment
	file        *nzbparser.NzbFile
	missingOn   map[int]bool // [provider.id]bool
	availableOn map[int]bool // [provider.id]bool
	ignoreDlOn  map[int]bool // [provider.id]bool
	unwantedOn  map[int]bool // [provider.id]bool
	retryOn     map[int]bool // [provider.id]bool
	errorOn     map[int]bool // [provider.id]bool // not in use ... ?!
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
