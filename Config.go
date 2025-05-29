package main

import (
	"sync"
	"time"

	"github.com/Tensai75/nzbparser"
)

const (
	DOT                           = "."
	CR                            = "\r"
	LF                            = "\n"
	CRLF                          = CR + LF
	DefaultCacheRW                = 8
	DefaultChanSize               = 1000
	DefaultPrintStats       int64 = 5
	DefaultCacheWriteBuffer       = 256 * 1024
	DefaultYencWriteBuffer        = 256 * 1024
	DefaultMaxArticleSize         = 1 * 1024 * 1024
	DefaultConnectTimeout         = 9 * time.Second
	DefaultConnectErrSleep        = 9 * time.Second
)

type Config struct {
	providers []Provider
	opt       *CFG
}

type CFG struct {
	//NZBfilepath      string  // only supplied from cmd line
	NzbDir           string `json:"NzbDir"`
	DirRefresh       int64  `json:"DirRefresh"`
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
	s           *SESSION // links to where it belongs
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

type fileStatistic struct {
	available     providerStatistic
	missing       providerStatistic
	totalSegments uint64
}
type filesStatistic map[string]*fileStatistic
type providerStatistic map[string]uint64

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
