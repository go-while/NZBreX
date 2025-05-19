package main

import (
	"fmt"
	"github.com/Tensai75/nzbparser"
	"log"
	"io"
	"os"
	"time"
	"strings"
	"compress/gzip"
	//"encoding/csv"
	"encoding/json"
	//"sort"
	"crypto/sha256"
	"encoding/hex"
)

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

/*
func loadNzbFile(path string) (*nzbparser.Nzb, error) {
	if b, err := os.Open(path); err != nil {
		return nil, err
	} else {
		defer b.Close()
		if nzbfile, err := nzbparser.Parse(b); err != nil {
			return nil, err
		} else {
			return nzbfile, nil
		}
	}
} // end func loadNzbFile
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

func loadProviderList(path string) error {
	if file, err := os.ReadFile(path); err != nil {
		return err
	} else {
		if err := json.Unmarshal(file, &cfg.providers); err != nil {
			return err
		}
		id := 0
		providernames := make(map[string]bool) // tmp map to check unique/dupe provider names
		for n, _ := range cfg.providers {
			if !cfg.providers[n].Enabled {
				continue
			}
			if providernames[cfg.providers[n].Name] {
				// duplicate provider Name
				return fmt.Errorf("FATAL ERROR: loadProviderList duplicate provider.Name (n=%d) in config", n)
			}
			providernames[cfg.providers[n].Name] = true
			p := &cfg.providers[n] // link to this provider

			NewConnPool(p)
			p.id = id
			providerList = append(providerList, p)
			if cfg.opt.Debug {
				log.Printf("CFG Loaded Provider (id=%d) '%s'", id, p.Name)
			}
			id++
		}
	}
	return nil
} // end func loadProviderList

/*
func writeCsvFile() {
	if cfg.opt.Csv {
		csvFileName := strings.TrimSuffix(filepath.Base(argsNZBpath), filepath.Ext(filepath.Base(argsNZBpath))) + ".csv"
		f, err := os.Create(csvFileName)
		if err != nil {
			exit(fmt.Errorf("unable to open csv file: %v", err))
		}
		log.Println("writing csv file...")
		fmt.Print("Writing csv file... ")
		csvWriter := csv.NewWriter(f)
		firstLine := true
		// make sorted provider name slice
		providers := make([]string, 0, len(providerList))
		for n := range providerList {
			providers = append(providers, providerList[n].Name)
		}
		sort.Strings(providers)
		for fileName, file := range fileStat {
			// write first line
			if firstLine {
				line := make([]string, len(providers)+2)
				line[0] = "Filename"
				line[1] = "Total segments"
				for n, providerName := range providers {
					line[n+2] = providerName
				}
				if err := csvWriter.Write(line); err != nil {
					exit(fmt.Errorf("unable to write to the csv file: %v", err))
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
				exit(fmt.Errorf("unable to write to the csv file: %v", err))
			}
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			exit(fmt.Errorf("unable to write to the csv file: %v", err))
		}
		f.Close()
		fmt.Print("done")
	}
}
*/

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
