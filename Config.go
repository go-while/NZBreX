package main

import (
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/go-while/go-loggedrwmutex"
)

const UseSharedCC = false     // experimental devel flag, to test sharedConn between routines
const UseReadDeadConn = false // experimental devel flag, to test reading from dead connections
const UseNoDeadline = true    // experimental devel flag, to test reading from dead connections

const always = true // flag for dlog

const (
	DOT  = "."
	CR   = "\r"
	LF   = "\n"
	CRLF = CR + LF
	// DefaultCacheRW defines the number of goroutines for cache reader/writer
	DefaultCacheRW = 8

	// DefaultChanSize defines the size (cap) of the channels for check, download, upload
	DefaultChanSize = 1000

	// DefaultPrintStats defines the interval in seconds for printing statistics
	// 0 = spammy output, -1 = no output, >0 = print every N seconds
	DefaultPrintStats int64 = 5

	// DefaultCacheWriteBuffer defines the size of the write buffer for cache files
	DefaultCacheWriteBuffer = 32 * 1024
	// DefaultYencWriteBuffer defines the size of the write buffer for yenc files
	DefaultYencWriteBuffer = 32 * 1024
	// DefaultMaxArticleSize defines the maximum size of an article in bytes
	// if an article is larger than this, it will be skipped
	DefaultMaxArticleSize = 1 * 1024 * 1024

	// DefaultConnectTimeout defines the timeout for connecting to a server
	DefaultConnectTimeout = 9 * time.Second
	// DefaultConnectErrSleep defines the time to wait before retrying a connection after an error
	DefaultConnectErrSleep = 9 * time.Second
	// DefaultRequeueDelay defines the delay before requeuing an item in the segment channel
	DefaultRequeueDelay = 9 * time.Second
)

type Config struct {
	providers []Provider
	opt       *CFG
}

type CFG struct {
	//NZBfilepath      string  // only supplied from cmd line
	NzbDir           string `json:"NzbDir"`           // directory where nzb files are stored
	DirRefresh       int64  `json:"DirRefresh"`       // seconds to wait for directory refresh
	ProvFile         string `json:"Provider"`         // file with provider list
	Cachedir         string `json:"Cachedir"`         // directory where cache files are stored
	CheckCacheOnBoot bool   `json:"CheckCacheOnBoot"` // if true, check cache for existing articles on startup
	ByPassSTAT       bool   `json:"ByPassSTAT"`       // if true, no STAT will be sent to server
	CRW              int    `json:"CRW"`              // cache reader/writer, number of goroutines
	MemMax           int    `json:"MemMax"`           // max number of objects in ram, 0 = unlimited
	ChanSize         int    `json:"ChanSize"`         // size of channels for reading/writing cache and yenc
	CheckOnly        bool   `json:"CheckOnly"`        // if true, no download or upload will be done
	CheckFirst       bool   `json:"CheckFirst"`       // if true, check for article existence before download
	UploadLater      bool   `json:"UploadLater"`      // if true, upload will be done after all articles are downloaded
	Verify           bool   `json:"Verify"`           // if true, verify articles after upload
	YencCRC          bool   `json:"YencCRC"`          // if true, crc32 will be checked while downloading yenc articles
	YencCpu          int    `json:"YencCpu"`          // number of cpu cores to use for yenc decoding, 0 = runtime.NumCPU()
	YencAsyncCpu     int    `json:"YencAsyncCpu"`     // number of cpu cores to use for async yenc decoding, 0 = runtime.NumCPU()
	YencTest         int    `json:"YencTest"`         // 1 = bytes, 2 = lines, used with YencCRC
	YencWrite        bool   `json:"YencWrite"`        // if true, yenc parts will be written to cache
	YencMerge        bool   `json:"YencMerge"`        // if true, yenc parts will be merged into target files
	YencDelParts     bool   `json:"YencDelParts"`     // if true, yenc parts will be deleted after merge
	Csv              bool   `json:"Csv"`              // if true, write a csv file for every nzb file
	Log              bool   `json:"Log"`              // if true, log to file
	LogAppend        bool   `json:"LogAppend"`        // if true, append to log file instead of overwriting
	LogDir           string `json:"LogDir"`           // directory where log files are stored
	LogOld           int    `json:"LogOld"`           // if true, rotate old log files up to .N
	Prof             bool   `json:"Prof"`             // if true, start profiler
	PrintStats       int64  `json:"PrintStats"`       // seconds to print stats, 0 = spammy, -1 = no output, >0 = print every N seconds
	Print430         bool   `json:"Print430"`         // if true, print notice about code 430 article not found
	Discard          bool   `json:"Discard"`          // if true, reduce console output to zero
	CleanHeaders     bool   `json:"CleanHeaders"`     // if true, clean headers from articles
	CleanHeadersFile string `json:"CleanHeadersFile"` // load strings of headers to cleanup from this file (1 header per line! checks only via prefix!)
	BUG              bool   `json:"BUG"`              // if true, enable bug reporting
	Debug            bool   `json:"Debug"`            // if true, enable debug output
	DebugCache       bool   `json:"DebugCache"`       // if true, enable cache debug output
	DebugConnPool    bool   `json:"DebugConnPool"`    // if true, enable connpool debug output
	DebugSharedCC    bool   `json:"DebugSharedCC"`    // if true, enable sharedConn debug output
	DebugWorker      bool   `json:"DebugWorker"`      // if true, enable workers debug output
	DebugMemlim      bool   `json:"DebugMemlim"`      // if true, enable memlim debug output
	DebugCR          bool   `json:"DebugCR"`          // if true, enable check routine debug output
	DebugDR          bool   `json:"DebugDR"`          // if true, enable downs routine debug output
	DebugUR          bool   `json:"DebugUR"`          // if true, enable reups routine debug output
	DebugSTAT        bool   `json:"DebugSTAT"`        // if true, enable STAT debug output
	DebugARTICLE     bool   `json:"DebugARTICLE"`     // if true, enable ARTICLE debug output
	DebugIHAVE       bool   `json:"DebugIHAVE"`       // if true, enable IHAVE debug output
	DebugPOST        bool   `json:"DebugPOST"`        // if true, enable POST debug output
	DebugFlags       bool   `json:"DebugFlags"`       // if true, enable printing item flags
	//DebugSTREAM       bool   `json:"DebugSTREAM"`      // if true, enable STREAM debug output
	Verbose       bool `json:"Verbose"`       // if true, enable verbose output
	Bar           bool `json:"Bar"`           // if true, show progress bar
	Colors        bool `json:"Colors"`        // if true, enable colored output
	MaxArtSize    int  `json:"MaxArtSize"`    // maximum article size in bytes
	SloMoC        int  `json:"SloMoC"`        // slow motion for checking articles
	SloMoD        int  `json:"SloMoD"`        // slow motion for downloading articles
	SloMoU        int  `json:"SloMoU"`        // slow motion for uploading articles
	SessThreshold int  `json:"SessThreshold"` // max number of sessions a processor keeps open
} // end CFG struct

type Provider struct {
	Enabled       bool      // if false, provider will be skipped
	NoCheck       bool      // if true, no check will be done for this provider
	NoDownload    bool      // if true, no download will be done for this provider
	NoUpload      bool      // if true, no upload will be done for this provider
	Group         string    // group name is used internally to divide providers accounts into groups
	Name          string    // provider name, used for logging and identification
	Host          string    // provider host, used for connecting to the server
	Port          uint32    // provider port, used for connecting to the server
	Timeout       int64     // timeout in seconds for connecting to the server
	SSL           bool      // if true, use SSL for connecting to the server
	SkipSslCheck  bool      // if true, skip SSL certificate verification
	Username      string    // username for authentication
	Password      string    // password for authentication
	MaxConns      int       // maximum number of connections to the provider
	TCPMode       string    // TCP mode to use (tcp, tcp4, tcp6)
	PreferIHAVE   bool      // if true, prefer IHAVE over POST method
	MaxConnErrors int       // maximum number of errors before giving up on a connection
	ConnPool      *ConnPool // the pool of connections for this provider
	//mux           sync.RWMutex    // mutex to protect the provider struct
	mux *loggedrwmutex.LoggedSyncRWMutex // debug mutex
	id  int                              // will be set in loadProviderList
	// flags
	capabilities struct {
		check  bool // if true, provider supports checking articles
		ihave  bool // if true, provider supports IHAVE method
		post   bool // if true, provider supports POST method
		stream bool // if true, provider supports streaming
	}
	// counters
	articles struct {
		tocheck    uint64 // number of articles to check
		checked    uint64 // number of articles checked
		available  uint64 // number of articles available
		downloaded uint64 // number of articles downloaded
		missing    uint64 // number of articles missing
		refreshed  uint64 // number of articles refreshed
		verified   uint64 // number of articles verified
	}
} // end Provider struct

type segmentChanItem struct {
	// segmentChanItem is used to store information about a segment/article
	// that is being processed in the segment channel.
	// It contains information about the segment, file, providers,
	// and various flags to indicate the state of the segment/article.
	// It is used to communicate between the segment channel and the
	// cache, download, and upload routines.
	// segmentChanItem is used to store information about a segment/article
	// that is being processed in the segment channel.
	//
	// !! DEVELOPER NOTE !!
	// When adding new fields to this item struct, make sure to initialize them properly in the LaunchSession function.
	// You find the item creation in Sessions.go around maybe line 250 (at time of writing this comment).
	// You may have noticed, this struct organizes the variables by types
	// When adding new fields, please keep the order.
	// Then you can easily find the fields you have to add in the initialization code.
	//mux         sync.RWMutex
	mux         *loggedrwmutex.LoggedSyncRWMutex // mutex to protect the segmentChanItem struct
	s           *SESSION                         // links to where it belongs
	segment     *nzbparser.NzbSegment            // segment/article to download
	file        *nzbparser.NzbFile               // file to which the segment belongs
	hashedId    string                           // hashed segment id, used for cache filename
	nzbhashname *string                          // pointer to the nzb hash name, used for cache directory
	readChan    chan int                         // to notify, cache has loaded the file to item.lines
	checkChan   chan bool                        // to notify if item exists in cache
	missingOn   map[int]bool                     // [provider.id]bool
	availableOn map[int]bool                     // [provider.id]bool
	ignoreDlOn  map[int]bool                     // [provider.id]bool
	ignoreUlOn  map[int]bool                     // [provider.id]bool
	unwantedOn  map[int]bool                     // [provider.id]bool
	uploadedTo  map[int]bool                     // [provider.id]bool
	retryOn     map[int]bool                     // [provider.id]bool
	errorOn     map[int]bool                     // [provider.id]bool // not in use ... ?!
	dmcaOn      map[int]bool                     // [provider.id]bool
	article     []string                         // contains the downloaded segment/article
	head        []string                         // contains the head
	body        []string                         // contains the body
	flaginDL    bool                             // if true, item is in download
	flagisDL    bool                             // if true, item has been downloaded
	flaginUP    bool                             // if true, item is in upload
	flagisUP    bool                             // if true, item has been uploaded
	flaginDLMEM bool                             // if true, item is in download memory and waits for the quaken
	flaginUPMEM bool                             // if true, item is in upload memory and waits for the quaken
	flaginYenc  bool                             // if true, item is in writing to yenc cache
	flagisYenc  bool                             // if true, item has been written to yenc cache
	flagCache   bool                             // if true, item is cached
	checkedOn   int                              // counts up if item has been checked on a provider
	pushedDL    int                              // a counter for debugging
	pushedUP    int                              // a counter for debugging
	size        int                              // size of the segment / full article in bytes (used with readDotLines(item,connitem,"ARTICLE"))
	headsize    int                              // size of the head in bytes (used with readDotLines(item,connitem,"HEAD"))
	bodysize    int                              // size of the body in bytes (used with readDotLines(item,connitem,"BODY"))
	dlcnt       int                              // download count, used for retrying
	badcrc      int                              // number of bad crc checks, used for retrying
	fails       int                              // number of fails, used for retrying
	retryIn     int64                            // retry in seconds, used for retrying
	rxb         uint64                           // total bytes received for this item, used for statistics
	txb         uint64                           // total bytes transferred for this item, used for statistics
} // end segmentChanItem struct

// fileStatistic is used to store statistics about a file
// it contains the number of available and missing segments for each provider
// and the total number of segments in the file
// it is used to generate statistics about the files in the nzb file
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

// PrintItemFlags prints the flags of the segmentChanItem
// it is used for debugging and logging purposes
// it prints the flags that are set for the item, such as inDL, isDL, inUP, isUP, inDLMEM, inUPMEM, inYenc, isYenc
// it will print "none" if no flags are set
// it is called from the segmentChanItem.PrintItemFlags() method
// src is the source of the log message
// print is a boolean to control if the flags should be printed or not
// doLock is a boolean to control if the mutex should be locked or not
func (item *segmentChanItem) PrintItemFlags(print bool, doLock bool, src string) {
	if !print {
		return
	}
	if doLock {
		item.mux.RLock()
		defer item.mux.RUnlock()
	}
	flags := []string{}
	if item.flaginDL {
		flags = append(flags, "inDL")
	}
	if item.flagisDL {
		flags = append(flags, "isDL")
	}
	if item.flaginUP {
		flags = append(flags, "inUP")
	}
	if item.flagisUP {
		flags = append(flags, "isUP")
	}
	if item.flaginDLMEM {
		flags = append(flags, "inDLMEM")
	}
	if item.flaginUPMEM {
		flags = append(flags, "inUPMEM")
	}

	if cfg.opt.BUG {
		if cfg.opt.YencWrite {
			if item.flaginYenc {
				flags = append(flags, "inYenc")
			}
			if item.flagisYenc {
				flags = append(flags, "isYenc")
			}
		}
		if cacheON {
			if item.flagCache {
				flags = append(flags, "isCached")
			}
		}
	}
	if len(flags) > 0 {
		dlog(always, "src='%s' ItemFlags: '%s' seg.Id='%s'", src, flags, item.segment.Id)
	} else {
		dlog(cfg.opt.BUG, "src='%s' ItemFlags: none seg.Id='%s'", src, item.segment.Id)
	}
} // end func segmentChanItem.PrintItemFlags

const (
	cmdARTICLE = "ARTICLE"
	cmdHEAD    = "HEAD"
	cmdBODY    = "BODY"
	cmdSTAT    = "STAT"
	cmdIHAVE   = "IHAVE"
	cmdPOST    = "POST"
)
