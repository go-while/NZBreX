package main

import (
	//"bufio"
	//"bytes"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/go-while/go-loggedrwmutex"
)

func getCoreLimiter() {
	<-core_chan
}

func returnCoreLimiter() {
	core_chan <- struct{}{}
}

/*
nzbfile=&nzbparser.Nzb{Comment:"", Meta:map[string]string{},
	Files:nzbparser.NzbFiles{
	nzbparser.NzbFile{
		Groups:[]string{"alt.binaries.gougouland"},
		Segments:nzbparser.NzbSegments{
			nzbparser.NzbSegment{Bytes:729047, Number:1, Id:"b9c5260e93724cd7a76d1b951b2c8717@ngPost"},
			nzbparser.NzbSegment{Bytes:729047, Number:1, Id:"a2f343f81b514fecbc7fbcb88b0e67bb@ngPost"},
			nzbparser.NzbSegment{Bytes:729184, Number:2, Id:"9dbcd79527b449deb1f686b3ad795c49@ngPost"},
			nzbparser.NzbSegment{Bytes:737522, Number:567, Id:"e27a7c05651e4880ab62328445bdc40a@ngPost"}
			},
		Poster:"32V49ZAnrYOCS@ngPost.com",
		Date:1681138800,
		Subject:"[1/1] \"debian-11.6.0-amd64-netinst.iso\" (1/568)",
		Number:1,
		Filename:"debian-11.6.0-amd64-netinst.iso",
		Basefilename:"debian-11.6.0-amd64-netinst",
		TotalSegments:568, Bytes:429739466}
	}, TotalFiles:1, Segments:581, TotalSegments:568, Bytes:429739466}
	*
*/

func loadNzbFile(path string) (*nzbparser.Nzb, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var parsedReader io.Reader

	// Check if the file is gzipped
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gzReader, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gzReader.Close()
		parsedReader = gzReader
	} else {
		parsedReader = f
	}

	nzbfile, err := nzbparser.Parse(parsedReader)
	if err != nil {
		return nil, err
	}
	return nzbfile, nil
} // end func loadNzbFile

/*
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
*/

// loadProviderList loads the provider list from the configuration file.
// It reads the JSON file specified in cfg.opt.ProvFile, unmarshals it into the cfg.providers slice,
// and initializes each provider with a connection pool.
func (cfg *Config) loadProviderList(s *SESSION, workerWGconnEstablish *sync.WaitGroup) error {
	if file, err := os.ReadFile(cfg.opt.ProvFile); err != nil {
		return err
	} else {
		if err := json.Unmarshal(file, &cfg.providers); err != nil {
			return err
		}
		id := 0
		providernames := make(map[string]bool)          // tmp map to check unique/duped provider names
		providerACL := make(map[string]map[string]bool) // map to check for bad config combinations prevents bypass in pushDL/pushUP
		for n := range cfg.providers {
			if !cfg.providers[n].Enabled {
				dlog(cfg.opt.Debug, "CFG Skipped Provider (n=%d) '%s' because not enabled", n, cfg.providers[n].Name)
				continue
			}
			Name := cfg.providers[n].Name
			if providernames[Name] {
				// duplicate provider Name
				return fmt.Errorf("error in loadProviderList duplicate provider.Name='%s' (n=%d) in config", Name, n)
			}
			providernames[Name] = true
			Group := cfg.providers[n].Group
			if providerACL[Group] == nil {
				providerACL[Group] = make(map[string]bool)
			}

			// check for inconsistent configuration
			if !cfg.providers[n].NoDownload && providerACL[Group]["NoDownload"] {
				dlog(always, "ERROR loadProviderList provider '%s'. set 'NoDownload' to same value in group '%s'!", Name, Group)
				os.Exit(1)
			}
			if !cfg.providers[n].NoUpload && providerACL[Group]["NoUpload"] {
				dlog(always, "ERROR loadProviderList provider '%s'. set 'NoUpload' to same value in group '%s'!", Name, Group)
				os.Exit(1)
			}

			// read NoDownload / NoUpload values from provider and set once per group for all
			if cfg.providers[n].NoDownload {
				providerACL[Group]["NoDownload"] = true
			}
			if cfg.providers[n].NoUpload {
				providerACL[Group]["NoUpload"] = true
			}

			if !slices.Contains(s.providerGroups, Group) {
				// add group to providerGroups if not already present
				s.providerGroups = append(s.providerGroups, Group)
			}

			// link to this provider
			provider := cfg.providers[n]
			p := &provider
			// provider is ready to connect
			p.id = id
			p.mux = &loggedrwmutex.LoggedSyncRWMutex{Name: p.Name}
			NewConnPool(s, p, workerWGconnEstablish)
			s.providerList = append(s.providerList, p)
			dlog(cfg.opt.Debug, "CFG Loaded Provider (id=%d) '%s'", id, p.Name)
			id++
		}
	}
	time.Sleep(time.Second * 3)
	return nil
} // end func loadProviderList

// LoadHeadersFromFile loads headers from a file and returns them as a slice of strings.
// It ignores empty lines and checks for specific headers that should not be present.
// If the file does not exist or cannot be opened, it returns an error.
// If the file is empty or contains only empty lines, it returns nil.
// If the "Date:" header is not present, it adds it to the list of headers.
// If any of the headers in needHeaders are found, it logs an error and exits the program.
func LoadHeadersFromFile(path string) ([]string, error) {
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
				dlog(always, "ERROR can not load header '%s' to cleanup!", hdr)
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
} // end func LoadHeadersFromFile

// AppendFileBytes appends null bytes to the end of a file.
// It opens the file in append mode, creates it if it does not exist, and writes the specified number of null bytes.
// If nullbytes is 0, it does nothing.
// If nullbytes is negative, it returns an error.
// If the file does not exist, it creates a new file with the specified number of null bytes.
// If the file exists, it appends the specified number of null bytes to the end of the file.
func AppendFileBytes(nullbytes int, dstPath string) error {
	if nullbytes <= 0 {
		return fmt.Errorf("error AppendFileBytes nullbytes=%d must be greater than 0", nullbytes)
	}
	if dstPath == "" {
		return fmt.Errorf("error AppendFileBytes dstPath='%s' empty", dstPath)
	}

	// Open destination file in append mode, create if not exists
	dstFile, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	nul := make([]byte, nullbytes)
	for i := 0; i < nullbytes; i++ {
		nul = append(nul, 0x00)
	}
	if _, writeErr := dstFile.Write(nul); writeErr != nil {
		return writeErr
	}
	return nil
} // end func AppendFileBytes

// AppendFile appends (merges) the file contents of srcPath to dstPath.
// If delsrc is true, it deletes the source file after appending.
// If srcPath or dstPath is empty, it returns an error.
// It opens the source file for reading and the destination file in append mode, creating it if it does not exist.
// It reads the source file in chunks and writes them to the destination file.
// If the source file does not exist, it returns an error.
// If the destination file does not exist, it creates a new file.
// If the source file is empty, it does nothing.
func AppendFile(srcPath string, dstPath string, delsrc bool) error {
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("error AppendFile srcPath='%s' or dstPath='%s' empty", srcPath, dstPath)
	}

	// Open source file for reading
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Open destination file in append mode, create if not exists
	dstFile, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// Create a buffer and copy in chunks
	buf := make([]byte, DefaultYencWriteBuffer)
	for {
		n, readErr := srcFile.Read(buf)
		if n > 0 {
			if _, writeErr := dstFile.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if delsrc {
		if err := os.Remove(srcPath); err != nil {
			return fmt.Errorf("error Yenc AppendFile Remove err='%v'", err)
		}
	}
	return nil
} // end func AppendFile (written by AI! GPT-4o)

func SHA256SumFile(path string) (string, error) {
	// Open the file for reading
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Create a SHA-256 hash object
	hash := sha256.New()

	// Copy the file into the hash function
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}

	// Return the hex-encoded checksum
	return strings.ToLower(hex.EncodeToString(hash.Sum(nil))), nil
} // end func SHA256SumFile (written by AI! GPT-4o)

// writeCsvFile writes the fileStat to a CSV file in the current directory.
func (s *SESSION) writeCsvFile() (err error) {
	// not tested since rewrite
	if !cfg.opt.Csv {
		return
	}
	csvFileName := strings.TrimSuffix(filepath.Base(s.nzbPath), filepath.Ext(filepath.Base(s.nzbPath))) + ".csv"
	f, err := os.Create(csvFileName)
	if err != nil {
		return fmt.Errorf("unable to open csv file: %v", err)
	}
	log.Println("writing csv file...")
	fmt.Print("Writing csv file... ")
	csvWriter := csv.NewWriter(f)
	firstLine := true
	// make sorted provider name slice
	providers := make([]string, 0, len(s.providerList))
	for n := range s.providerList {
		providers = append(providers, s.providerList[n].Name)
	}
	sort.Strings(providers)
	for fileName, file := range s.fileStat {
		// write first line
		if firstLine {
			line := make([]string, len(providers)+2)
			line[0] = "Filename"
			line[1] = "Total segments"
			for n, providerName := range providers {
				line[n+2] = providerName
			}
			if err := csvWriter.Write(line); err != nil {
				return fmt.Errorf("unable to write to the csv file: %v", err)
			}
			firstLine = false
		}
		// write line
		line := make([]string, len(providers)+2)
		line[0] = fileName
		line[1] = fmt.Sprintf("%v", file.totalSegments)
		for n, providerName := range providers {
			if value, ok := file.available[providerName]; ok {
				line[n+2] = fmt.Sprintf("%v", value)
			} else {
				line[n+2] = "0"
			}
		}
		if err := csvWriter.Write(line); err != nil {
			return fmt.Errorf("unable to write to the csv file: %v", err)
		}
	}
	csvWriter.Flush()
	if err := csvWriter.Error(); err != nil {
		return fmt.Errorf("unable to write to the csv file: %v", err)
	}
	f.Close()
	dlog(cfg.opt.Csv, "writeCsv: done")
	return
} // end func writeCsv

/*
	func setGlobalTimerNow(timer *time.Time) {
		globalmux.Lock()
		*timer = time.Now()
		globalmux.Unlock()
	}

	func getGlobalTimerSince(timer time.Time) time.Duration {
		globalmux.RLock()
		duration := time.Since(timer)
		globalmux.RUnlock()
		return duration
	}
*/
func ConvertSpeed(bytes int64, durationSeconds int64) (kibPerSec int64, mbps float64) {
	if durationSeconds <= 0 {
		return 0, 0
	}

	// KiB/s: binary (1024)
	kibPerSec = bytes / durationSeconds / 1024

	// Mbps: decimal (1000)
	mbps = float64(bytes) * 8 / float64(durationSeconds) / 1_000_000

	return kibPerSec, mbps
} // end func ConvertSpeed (written by AI: GPT-4o)

func SHA256str(astr string) string {
	// string hash func works only with cacheON!
	// only used to create hashs of segment.Id
	// no cache no hash!
	if !cacheON {
		return ""
	}
	ahash := sha256.Sum256([]byte(astr))
	return hex.EncodeToString(ahash[:])
} // end func SHA256str

func DirExists(dir string) bool {
	//dlog("?DirExists dir='%s'", dir)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		dlog(always, "ERROR DirExists err='%v'", err)
		return false
	}
	return info.IsDir()
} // end func DirExists

func FileExists(File_path string) bool {
	info, err := os.Stat(File_path)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		dlog(always, "ERROR FileExists err='%v'", err)
		return false
	}
	return !info.IsDir()
} // end func FileExists

func Mkdir(dir string) bool {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		dlog(always, "ERROR Mkdir='%s' err='%v'", dir, err)
		return false
	} else {
		//dlog("CREATED DIR %s", dir)
	}
	return true
} // end func Mkdir

func RunProf() {
	if webProf != "" {
		go Prof.PprofWeb(webProf)
	}
	dlog(always, "Started PprofWeb @ Port :61234")
	go func() {
		time.Sleep(time.Second * 15) // pProf
		runtime.GC()
		time.Sleep(time.Second * 5) // pProf
		//dlog("Prof start capturing cpu profile")
		if _, err := Prof.StartCPUProfile(); err != nil {
			dlog(always, "ERROR Prof.StartCPUProfile err='%v'", err)
			return
		}
		//time.Sleep(time.Second * 120)
		//Prof.StopCPUProfile()
		//dlog("Prof stop capturing cpu/mem profiles")
	}()
	go func() {
		if err := Prof.StartMemProfile(120*time.Second, 20*time.Second); err != nil {
			dlog(always, "ERROR Prof.StartMemProfile err='%v'", err)
		}
	}()
} // end fun RunProf

// cosmetics
func yesno(input bool) string {
	switch input {
	case true:
		return "+++"
	case false:
		return "---"
	}
	return "?"
} // end func yesno

func dlog(logthis bool, format string, a ...any) {
	if !logthis {
		return
	}
	log.Printf(format, a...)
} // end dlog

func GoMutexStatus() {
	if loggedrwmutex.DisableLogging {
		dlog(cfg.opt.Debug, "Mutex: loggedrwmutex.DisableLogging")
		return
	}
	for {
		time.Sleep(time.Millisecond * 5000) // check every N milliseconds
		//c.provider.mux.PrintStatus(cfg.opt.Debug)          // prints mutex status for the provider
		//c.provider.ConnPool.mux.PrintStatus(cfg.opt.Debug) // prints mutex status for the providers connpool
		globalmux.PrintStatus(true) // prints global mutex status for the whole app
		globalmux.Lock()
		if memlim != nil && memlim.mux != nil {
			memlim.mux.PrintStatus(true) // prints memory limit mutex status
		}
		globalmux.Unlock()
	}
}

// RotateFiles renames "name" to "name.old.1", "name.old.1" to "name.old.2", ..., up to maxVersions
// If a version exists, it is shifted up by one. All renames are done in reverse order to avoid overwrites.
func RotateLogFiles(name string, maxVersions int) error {
	if maxVersions < 1 {
		dlog(always, "RotateLogFiles: maxVersions=%d is less than 1, nothing to do", maxVersions)
		return nil
	}
	// Start from the highest version and move backwards
	for i := maxVersions; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.old.%d", name, i)
		newPath := fmt.Sprintf("%s.old.%d", name, i+1)
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				return fmt.Errorf("failed to rename %q to %q: %w", oldPath, newPath, err)
			}
		}
	}
	// Now move the original file to .old.1
	if _, err := os.Stat(name); err == nil {
		if err := os.Rename(name, fmt.Sprintf("%s.old.1", name)); err != nil {
			return fmt.Errorf("failed to rename %q to %q: %w", name, name+".old.1", err)
		}
	}
	return nil
} // end func RotateFiles (written by AI: GPT-4.1)

func SetLogToTerminal() {
	// Reset log output to the terminal
	log.SetOutput(os.Stdout)
	dlog(cfg.opt.Debug, "output reset to os.Stdout")
} // end func SetLogToTerminal (written by AI: GPT-4.1)

func LogToFile(filename string, append bool) (err error) {
	// Set log output to the specified file
	var logFile *os.File
	if append {
		// Open the file in append mode
		logFile, err = os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	} else {
		// Open the file in overwrite mode
		logFile, err = os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	}
	if err != nil {
		dlog(always, "ERROR LogToFile: failed to open log file '%s': %v", filename, err)
		return
	}
	if cfg.opt.Debug {
		SetLogToTerminal()
		dlog(always, "LogToFile: log output set to '%s'", filename)
	}
	log.SetOutput(logFile)
	return
} // end func LogToFile (written by AI: GPT-4.1)

func DecreaseDLQueueCnt() {
	// this is used to decrease the dlQueueCnt counter
	GCounter.Decr("dlQueueCnt")
}

func IncreaseDLQueueCnt() {
	// this is used to decrease the dlQueueCnt counter
	GCounter.Incr("dlQueueCnt")
	GCounter.Incr("TOTAL_dlQueueCnt")
	//GCounter.IncrMax("dlQueueCnt", uint64(len(s.segmentList)), "pushDL")       // increment temporary dlQueueCnt counter
	//GCounter.IncrMax("TOTAL_dlQueueCnt", uint64(len(s.segmentList)), "pushDL") // increment TOTAL_dlQueueCnt counter
}

func DecreaseUPQueueCnt() {
	// this is used to decrease the upQueueCnt counter
	GCounter.Decr("upQueueCnt")
}

func IncreaseUPQueueCnt() {
	// this is used to decrease the upQueueCnt counter
	GCounter.Incr("upQueueCnt")
	GCounter.Incr("TOTAL_upQueueCnt")
}

func AdjustMicroSleep(microsleep int64, pushed, todo uint64, lastRunTook time.Duration, min, max int64) int64 {
	// Defensive: avoid division by zero
	if pushed == 0 {
		return max / 3 // our max is 15s so if nothing had been pushed, we sleep 5s
	}

	// Calculate work ratio: how much was done versus remaining
	workRatio := float64(pushed) / float64(todo)

	// Adjust microsleep: if work ratio is low, decrease sleep (work faster); if high, increase sleep (slow down)
	adjustment := int64(float64(lastRunTook.Milliseconds()) * (1.0 - workRatio))

	newSleep := microsleep + adjustment

	// Clamp between min and max
	if newSleep < min {
		newSleep = min
	}
	if newSleep > max {
		newSleep = max
	}
	dlog(always, "AdjustMicroSleep: pushed=%d todo=%d lastRunTook=%s microsleep=%d -> newSleep=%d (min=%d max=%d)", pushed, todo, lastRunTook, microsleep, newSleep, min, max)
	return newSleep
}

func fatal() bool {
	return true
}

// GetImpossibleCloseCaseVariablesToString returns a string representation of the impossible close case variables.
// This is used for debugging purposes to understand the state of the system when an impossible close case occurs.
func GetImpossibleCloseCaseVariablesToString(segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ uint64) string {
	return fmt.Sprintf("segm=%d\n allOk=%d\n done=%d\n dead=%d\n isdl=%d\n indl=%d\n inup=%d\n isup=%d\n checked=%d\n dmca=%d\n nodl=%d\n noup=%d\n cached=%d\n inretry=%d\n inyenc=%d\n isyenc=%d\n dlQ=%d\n upQ=%d\n yeQ=%d\n",
		segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ)
}
