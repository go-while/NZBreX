package main

import (
	"fmt"
	"log"

	//"path/filepath"
	"slices"
	"time"
)

func (s *SESSION) Results(preparationStartTime time.Time) (result string, runtime_info string) {
	if cfg.opt.Verbose {
		defer log.Printf("func Results() returned sessId=%d", s.sessId)
	}

	transferTook := time.Since(s.segmentCheckStartTime)
	if cfg.opt.CheckFirst {
		transferTook = time.Since(s.segmentCheckEndTime)
	}
	dlSpeed := int(float64(GCounter.GetValue("TOTAL_RXbytes")) / transferTook.Seconds() / 1024)
	upSpeed := int(float64(GCounter.GetValue("TOTAL_TXbytes")) / transferTook.Seconds() / 1024)
	var transfertook, avgUpDlspeed string
	totalruntime := fmt.Sprintf(" | QUIT |\n\n> NZB: '%s' (%d segments)\n> Total Runtime: %.0f sec (%v)", s.nzbName, len(s.segmentList), time.Since(preparationStartTime).Seconds(), time.Since(preparationStartTime))
	segchecktook := fmt.Sprintf("\n> SegCheck: %.0f sec (%v)", s.segmentCheckTook.Seconds(), s.segmentCheckTook)
	if !cfg.opt.CheckOnly {
		transfertook = fmt.Sprintf("\n> Transfer: %.0f sec (%v)", transferTook.Seconds(), transferTook)
		avgUpDlspeed = fmt.Sprintf("\n> DL %d KiB/s  (Total: %.2f MiB)\n> UL %d KiB/s  (Total: %.2f MiB)", dlSpeed, float64(GCounter.GetValue("TOTAL_RXbytes"))/float64(1024)/float64(1024), upSpeed, float64(GCounter.GetValue("TOTAL_TXbytes"))/float64(1024)/float64(1024))
	}
	runtime_info = totalruntime + segchecktook + transfertook + avgUpDlspeed

	provGroups := make(map[string][]int)
	var keeporder []string
	for id, provider := range s.providerList {
		if !slices.Contains(keeporder, provider.Group) {
			keeporder = append(keeporder, provider.Group)
		}
		provGroups[provider.Group] = append(provGroups[provider.Group], id)
	}

	for _, group := range keeporder {
		for agroup, ids := range provGroups {
			if group != agroup {
				continue
			}
			var pGchecked, pGavailable, pGmissing, pGdownloaded, pGrefreshed uint64
			if len(ids) > 1 {
				result = result + fmt.Sprintf("\n\n> Results | Group: '%s'", group)
			} else if len(ids) == 1 {
				result = result + fmt.Sprintf("\n\n> Results | Provi: '%s'", s.providerList[ids[0]].Name)
			} else {
				// should be unreachable code
				continue
			}
			for _, pid := range ids {
				s.providerList[pid].mux.RLock()
				groupStr := ""
				if len(ids) > 1 {
					groupStr = fmt.Sprintf("| Group: '%s'", s.providerList[pid].Group)
				}
				if cfg.opt.CheckOnly {
					result = result + fmt.Sprintf("\n>  Id:%3d | checked: %"+s.D+"d | avail: %"+s.D+"d | miss: %"+s.D+"d | @ '%s' %s",
						s.providerList[pid].id,
						s.providerList[pid].articles.checked,
						s.providerList[pid].articles.available,
						s.providerList[pid].articles.missing,
						s.providerList[pid].Name,
						groupStr,
					)
				} else {
					result = result + fmt.Sprintf("\n>  Id:%3d | checked: %"+s.D+"d | avail: %"+s.D+"d | miss: %"+s.D+"d | dl: %"+s.D+"d | up: %"+s.D+"d | @ '%s' %s",
						s.providerList[pid].id,
						s.providerList[pid].articles.checked,
						s.providerList[pid].articles.available,
						s.providerList[pid].articles.missing,
						s.providerList[pid].articles.downloaded,
						s.providerList[pid].articles.refreshed,
						s.providerList[pid].Name,
						groupStr,
					)
				}
				if len(ids) > 1 {
					pGchecked += s.providerList[pid].articles.checked
					pGavailable += s.providerList[pid].articles.available
					pGmissing += s.providerList[pid].articles.missing
					pGdownloaded += s.providerList[pid].articles.downloaded
					pGrefreshed += s.providerList[pid].articles.refreshed
				}
				s.providerList[pid].mux.RUnlock()
			} // end for pid in range ids
			if len(ids) > 1 {
				// count totals only if we have more than 1 provider in group
				result = result + fmt.Sprintf("\n> ^Totals | checked: %"+s.D+"d | avail: %"+s.D+"d | miss: %"+s.D+"d | dl: %"+s.D+"d | up: %"+s.D+"d | Group: '%s'",
					pGchecked, pGavailable, pGmissing, pGdownloaded, pGrefreshed, group,
				)
			}
		}
	}
	return
} // end func Results
