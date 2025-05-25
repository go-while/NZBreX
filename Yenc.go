package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

func YencMerge(nzbhashname string, result *string) {
	if !cfg.opt.YencMerge || !cacheON {
		return
	}
	var waitMerge sync.WaitGroup
	mergeStart := time.Now().Unix()
	log.Printf("Experimental YencWrite: try merging files....")
	filenames := []string{}
	for _, item := range segmentList {
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
			target := filepath.Join(cfg.opt.Cachedir, nzbhashname, "yenc", filename)
			if FileExists(target) {
				log.Printf("YencMerge: exists target='%s'", target)
				return
			}
			var items []*segmentChanItem
			// capture all items for our filename
		loopItems:
			for _, item := range segmentList {
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
			} // end for segmentList
			if len(items) == 0 {
				log.Printf("ERROR YencMerge: no items found to merge? fn='%s'", filename)
				return
			}
			log.Printf("YencMerge: try merge fn='%s'", filename)
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
							log.Printf("WARN YencMerge: missing partNo=%d fn='%s' not merging...", item.segment.Number, filename)
						}
						if FileExists(target + ".tmp") {
							if err := os.Remove(target + ".tmp"); err != nil {
								log.Printf("ERROR YencMerge: remove fn='%s'.tmp failed err='%v'", filename, err)
								return
							}
						}
						deleted = true
						continue mergeItems
					}
					log.Printf("WARN YencMerge: fn='%s' missing partNo=%d writing %d nullbytes", filename, item.segment.Number, item.segment.Bytes)
					if err := AppendFileBytes(item.segment.Bytes, target+".tmp"); err != nil {
						log.Printf("ERROR YencMerge: AppendFile nullbytes err='%v'", err)
						return
					}
					continue mergeItems
				}
				if cfg.opt.Debug {
					log.Printf("... merging part: '%s'", partname)
				}
				if err := AppendFile(fp, target+".tmp", cfg.opt.YencDelParts); err != nil {
					log.Printf("ERROR YencMerge AppendFile err='%v'", err)
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
				log.Printf("YencMerge: wait ... sha256sum fn='%s'", filename)
				hash, err := SHA256SumFile(target + ".tmp")
				if err != nil {
					log.Printf("ERROR YencMerge: fn='%s' SHA256SumFile err='%v'", target+".tmp", err)
					return
				}
				if hash != testhash {
					log.Printf("ERROR YencMerge: fn='%s' has hash='%s' != '%s'", filename, hash, testhash)
					os.Remove(target + ".tmp")
					return
				}
				log.Printf("YencMerge: Verify hash OK fn='%s'", filename)
			} // end debug verify testhash

			if deleted || missingParts > 0 {
				if cfg.opt.Debug {
					log.Printf("YencMerge: missingParts=%d fn='%s' deleted=%t", missingParts, filename, deleted)
				}
			}

			// finally rename
			if !deleted {
				if err := os.Rename(target+".tmp", target); err != nil {
					log.Printf("ERROR YencMerge: move .tmp failed fn='%s' err='%v'", filename, err)
					return
				}
			}
			log.Printf("YencMerge: OK fn='%s'", filename)
		}(fn, &waitMerge) // end go func
	} // end for filename

	waitMerge.Wait()
	mergeTook := time.Now().Unix() - mergeStart
	*result = *result + fmt.Sprintf("\n\n> MergeTook: %d sec", mergeTook)

} // end func YencMerge
