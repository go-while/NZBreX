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
	"sort"
	"strings"
	"time"

	"github.com/Tensai75/nzbparser"
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

func (s *SESSION) loadProviderList() error {
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
				if cfg.opt.Debug {
					log.Printf("CFG Skipped Provider (id=%d) '%s' because not enabled", id, cfg.providers[n].Name)
				}
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
				log.Printf("ERROR loadProviderList provider '%s'. set 'NoDownload' to same value in group '%s'!", Name, Group)
				os.Exit(1)
			}
			if !cfg.providers[n].NoUpload && providerACL[Group]["NoUpload"] {
				log.Printf("ERROR loadProviderList provider '%s'. set 'NoUpload' to same value in group '%s'!", Name, Group)
				os.Exit(1)
			}

			// read NoDownload / NoUpload values from provider and set once per group for all
			if cfg.providers[n].NoDownload {
				providerACL[Group]["NoDownload"] = true
			}
			if cfg.providers[n].NoUpload {
				providerACL[Group]["NoUpload"] = true
			}

			// link to this provider
			p := &cfg.providers[n]
			// provider is ready to connect
			NewConnPool(p)
			p.id = id
			s.providerList = append(s.providerList, p)
			if cfg.opt.Debug {
				log.Printf("CFG Loaded Provider (id=%d) '%s'", id, p.Name)
			}
			id++
		}
	}
	return nil
} // end func loadProviderList

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
} // end func LoadHeadersFromFile

func AppendFileBytes(nullbytes int, dstPath string) error {
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

func AppendFile(srcPath string, dstPath string, delsrc bool) error {
	if srcPath == "" || dstPath == "" {
		return fmt.Errorf("error Appendfile srcPath='%s' or dstPath='%s' empty", srcPath, dstPath)
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
	log.Printf("writeCsv: done")
	return
} // end func writeCsv

func setTimerNow(timer *time.Time) {
	globalmux.Lock()
	*timer = time.Now()
	globalmux.Unlock()
}

func getTimeSince(timer time.Time) time.Duration {
	globalmux.RLock()
	duration := time.Since(timer)
	globalmux.RUnlock()
	return duration
}

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
	//log.Printf("?DirExists dir='%s'", dir)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		log.Printf("ERROR DirExists err='%v'", err)
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
		log.Printf("ERROR FileExists err='%v'", err)
		return false
	}
	return !info.IsDir()
} // end func FileExists

func Mkdir(dir string) bool {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		log.Printf("ERROR Mkdir='%s' err='%v'", dir, err)
		return false
	} else {
		//log.Printf("CREATED DIR %s", dir)
	}
	return true
} // end func Mkdir

func RunProf() {
	if webProf != "" {
		go Prof.PprofWeb(webProf)
	}
	log.Printf("Started PprofWeb @ Port :61234")
	go func() {
		time.Sleep(time.Second * 15) // pProf
		runtime.GC()
		time.Sleep(time.Second * 5) // pProf
		//log.Printf("Prof start capturing cpu profile")
		if _, err := Prof.StartCPUProfile(); err != nil {
			log.Printf("ERROR Prof.StartCPUProfile err='%v'", err)
			return
		}
		//time.Sleep(time.Second * 120)
		//Prof.StopCPUProfile()
		//log.Printf("Prof stop capturing cpu/mem profiles")
	}()
	go func() {
		if err := Prof.StartMemProfile(120*time.Second, 20*time.Second); err != nil {
			log.Printf("ERROR Prof.StartMemProfile err='%v'", err)
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

/*
func dlog(anyflag bool, format string, a ...any) {
	if anyflag {
		log.Printf(format, a...)
	}
} // end dlog
*/

/*
func getUptime(what string, booted time.Time) (uptime float64) {
	switch what {
	case "Seconds":
		uptime = time.Since(booted).Seconds()
	case "Minutes":
		uptime = time.Since(booted).Minutes()
	case "Hours":
		uptime = time.Since(booted).Hours()
	default:
		uptime = -1
	}
	return
} // end func getUptime
*/
