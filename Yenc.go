package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-while/NZBreX/rapidyenc"
	"github.com/go-while/NZBreX/yenc" // fork of chrisfarms with little mods
)

type yenc_item struct {
	item  *segmentChanItem
	yPart *yenc.Part
	//	s *SESSION
}

func (s *SESSION) YencMerge(result *string) {
	if !cfg.opt.YencMerge || !cacheON {
		return
	}
	var waitMerge sync.WaitGroup
	mergeStart := time.Now().Unix()
	dlog(always, "Experimental YencWrite: try merging files....")
	filenames := []string{}
	for _, item := range s.segmentList {
		if !slices.Contains(filenames, item.file.Filename) {
			filenames = append(filenames, item.file.Filename)
		}
	}

	for _, fn := range filenames {
		// launch merging in parallel
		waitMerge.Add(1)
		getCoreLimiter()
		go func(filename string, waitMerge *sync.WaitGroup) {
			defer returnCoreLimiter()
			defer waitMerge.Done()
			target := filepath.Join(cfg.opt.Cachedir, s.nzbHashedname, "yenc", filename)
			if FileExists(target) {
				dlog(always, "ERROR YencMerge: exists target='%s'", target)
				return
			}
			var items []*segmentChanItem
			// capture all items for our filename
		loopItems:
			for _, item := range s.segmentList {
				if item.file.Filename != filename {
					continue loopItems
				}

				/* checking flagisYenc allows merging only ...
				 * ... if parts have been downloaded this round
				 */

				/*
					if !item.flagisYenc {
						continue loopItems
					}
				*/

				items = append(items, item)
			} // end for s.segmentList
			if len(items) == 0 {
				dlog(always, "ERROR YencMerge: no items found to merge? fn='%s'", filename)
				return
			}
			dlog(always, "YencMerge: try merge fn='%s'", filename)
			deleted := false
			missingParts := 0
		mergeItems:
			for _, item := range items {
				partname, _, _, fp, _ := cache.GetYenc(item) // fp is "/path/to/cache/{nzbhashname}/yenc/[filename}.NNNN" file
				if !FileExists(fp) {
					missingParts++
					if strings.HasSuffix(filename, ".par") || strings.HasSuffix(filename, ".par2") {
						if deleted {
							continue mergeItems
						}
						if cfg.opt.Debug {
							dlog(always, "WARN YencMerge: missing partNo=%d fn='%s' not merging...", item.segment.Number, filename)
						}
						if FileExists(target + ".tmp") {
							if err := os.Remove(target + ".tmp"); err != nil {
								dlog(always, "ERROR YencMerge: remove fn='%s'.tmp failed err='%v'", filename, err)
								return
							}
						}
						deleted = true
						continue mergeItems
					}
					dlog(always, "WARN YencMerge: fn='%s' missing partNo=%d writing %d nullbytes", filename, item.segment.Number, item.segment.Bytes)
					if err := AppendFileBytes(item.segment.Bytes, target+".tmp"); err != nil {
						dlog(always, "ERROR YencMerge: AppendFile nullbytes err='%v'", err)
						return
					}
					continue mergeItems
				}
				if cfg.opt.Debug {
					dlog(always, "... merging part: '%s'", partname)
				}
				if err := AppendFile(fp, target+".tmp", cfg.opt.YencDelParts); err != nil {
					dlog(always, "ERROR YencMerge AppendFile err='%v'", err)
					return
				}
			} // end for items

			// debug verify testhash
			testhash := ""
			switch filename {
			case "debian-11.1.0-i386-DVD-1.iso":
				testhash = "d3af1d61414b3e9b2278aeedb2416bf7d1542c5d7d5668e058adc4b331baf885"
			case "debian-11.6.0-amd64-netinst.iso":
				testhash = "e482910626b30f9a7de9b0cc142c3d4a079fbfa96110083be1d0b473671ce08d"
			case "debian-12.2.0-amd64-netinst.iso":
				testhash = "23ab444503069d9ef681e3028016250289a33cc7bab079259b73100daee0af66"
			case "debian-12.9.0-amd64-DVD-1.iso":
				testhash = "d336415ab09c0959d4ef32384637d8b15fcaee12a04154d69bbca8b4442d2aa3"
			case "ubuntu-22.04-live-server-amd64.iso":
				testhash = "84aeaf7823c8c61baa0ae862d0a06b03409394800000b3235854a6b38eb4856f"
			case "ubuntu-24.04-live-server-amd64.iso":
				testhash = "8762f7e74e4d64d72fceb5f70682e6b069932deedb4949c6975d0f0fe0a91be3"
			}
			if testhash != "" {
				dlog(always, "YencMerge: wait ... sha256sum fn='%s'", filename)
				hash, err := SHA256SumFile(target + ".tmp")
				if err != nil {
					dlog(always, "ERROR YencMerge: fn='%s' SHA256SumFile err='%v'", target+".tmp", err)
					return
				}
				if hash != testhash {
					dlog(always, "ERROR YencMerge: fn='%s' has hash='%s' != '%s'", filename, hash, testhash)
					os.Remove(target + ".tmp")
					return
				}
				dlog(always, "YencMerge: Verify hash OK fn='%s'", filename)
			} // end debug verify testhash

			if deleted || missingParts > 0 {
				if cfg.opt.Debug {
					dlog(always, "YencMerge: missingParts=%d fn='%s' deleted=%t", missingParts, filename, deleted)
				}
			}

			// finally rename
			if !deleted {
				if err := os.Rename(target+".tmp", target); err != nil {
					dlog(always, "ERROR YencMerge: move .tmp failed fn='%s' err='%v'", filename, err)
					return
				}
			}
			dlog(always, "YencMerge: OK fn='%s'", filename)
		}(fn, &waitMerge) // end go func
	} // end for filename

	waitMerge.Wait()
	mergeTook := time.Now().Unix() - mergeStart
	*result = *result + fmt.Sprintf("\n\n> MergeTook: %d sec", mergeTook)

} // end func YencMerge

func testRapidyencDecoderFiles() (errs []error) {
	files := []string{
		"yenc/multipart_test.yenc",
		"yenc/multipart_test_badcrc.yenc",
		"yenc/singlepart_test.yenc",
		"yenc/singlepart_test_badcrc.yenc",
	}
	for _, fname := range files {
		fmt.Printf("\n=== Testing rapidyenc with file: %s ===\n", fname)
		f, err := os.Open(filepath.Clean(fname))
		if err != nil {
			fmt.Printf("Failed to open %s: %v\n", fname, err)
			continue
		}

		pipeReader, pipeWriter := io.Pipe()
		decoder := rapidyenc.AcquireDecoderWithReader(pipeReader)
		decoder.SetDebug(true, true)
		defer rapidyenc.ReleaseDecoder(decoder)
		segId := fname
		decoder.SetSegmentId(&segId)

		// Start goroutine to read decoded data
		var decodedData bytes.Buffer
		done := make(chan error, 1)
		go func() {
			buf := make([]byte, rapidyenc.DefaultBufSize)
			for {
				n, aerr := decoder.Read(buf)
				if n > 0 {
					decodedData.Write(buf[:n])
				}
				if aerr == io.EOF {
					done <- nil
					return
				}
				if aerr != nil {
					done <- aerr
					return
				}
			}
		}()

		// Write file lines to the pipeWriter
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if _, err := pipeWriter.Write([]byte(line + "\r\n")); err != nil {
				fmt.Printf("Error writing to pipe: %v\n", err)
				pipeWriter.Close()
				return
			}
		}
		if _, err := pipeWriter.Write([]byte(".\r\n")); err != nil { // NNTP end marker
			fmt.Printf("Error writing end marker to pipe: %v\n", err)
			pipeWriter.Close()
			return
		}
		pipeWriter.Close()
		f.Close()
		if aerr := <-done; aerr != nil {
			err = aerr
			var aBadCrc uint32
			meta := decoder.Meta()
			dlog(always, "DEBUG Decoder error: '%v' (maybe an expected error, check below)\n", err)
			expectedCrc := decoder.ExpectedCrc()
			if expectedCrc != 0 && expectedCrc != meta.Hash {

				// Set aBadCrc based on the file name
				switch fname {
				case "yenc/singlepart_test_badcrc.yenc":
					aBadCrc = 0x6d04a475
				case "yenc/multipart_test_badcrc.yenc":
					aBadCrc = 0xf6acc027
				}
				if aBadCrc > 0 && aBadCrc != meta.Hash {
					fmt.Printf("WARNING1 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else if aBadCrc > 0 && aBadCrc == meta.Hash {
					fmt.Printf("rapidyenc OK expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
				} else if expectedCrc != meta.Hash {
					fmt.Printf("WARNING2 rapidyenc: CRC mismatch! expected=%#08x | got meta.Hash=%#08x | wanted aBadCrc=%#08x fname: '%s'\n\n", expectedCrc, meta.Hash, aBadCrc, fname)
					errs = append(errs, aerr)
				} else {
					fmt.Printf("GOOD CRC matches! aBadCrc=%#08x Name: '%s' fname: '%s'\n", aBadCrc, meta.Name, fname)
				}

			} else if expectedCrc == 0 {
				fmt.Printf("WARNING rapidyenc: No expected CRC set, cannot verify integrity. fname: '%s'\n", fname)
				errs = append(errs, aerr)
			}
		} else {
			meta := decoder.Meta()
			fmt.Printf("OK Decoded %d bytes, CRC32: %#08x, Name: '%s' fname: '%s'\n", decodedData.Len(), meta.Hash, meta.Name, fname)
		}
	}
	return errs
}
