package main

import (
	"time"
	"fmt"
	"path/filepath"
	"slices"
)

func Results(preparationStartTime time.Time) (result string, runtime_info string) {
	//endTime := time.Now()
	transferTook := time.Since(segmentCheckStartTime)
	if cfg.opt.CheckFirst {
		transferTook = time.Since(segmentCheckEndTime)
	}
	dlSpeed := int(float64(Counter.get("TOTAL_RXbytes")) / transferTook.Seconds() / 1024)
	upSpeed := int(float64(Counter.get("TOTAL_TXbytes")) / transferTook.Seconds() / 1024)
	var transfertook, avgUpDlspeed string
	totalruntime := fmt.Sprintf(" | QUIT |\n\n> NZB: '%s' (%d segments)\n> Total Runtime: %.0f sec (%v)", filepath.Base(cfg.opt.NZBfilepath), len(segmentList), time.Since(preparationStartTime).Seconds(), time.Since(preparationStartTime))
	segchecktook := fmt.Sprintf("\n> SegCheck: %.0f sec (%v)", segmentCheckTook.Seconds(), segmentCheckTook)
	if !cfg.opt.CheckOnly {
		transfertook = fmt.Sprintf("\n> Transfer: %.0f sec (%v)", transferTook.Seconds(), transferTook)
		avgUpDlspeed = fmt.Sprintf("\n> DL %d KiB/s  (Total: %.2f MiB)\n> UL %d KiB/s  (Total: %.2f MiB)", dlSpeed, float64(Counter.get("TOTAL_RXbytes"))/float64(1024)/float64(1024), upSpeed, float64(Counter.get("TOTAL_TXbytes"))/float64(1024)/float64(1024))
	}
	runtime_info = totalruntime + segchecktook + transfertook + avgUpDlspeed

	provGroups := make(map[string][]int)
	var keeporder []string
	for id, provider := range providerList {
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
				result = result + fmt.Sprintf("\n\n> Results | Provi: '%s'", providerList[ids[0]].Name)
			} else {
				// should be unreachable code
				continue
			}
			for _, pid := range ids {
				providerList[pid].mux.RLock()
				groupStr := ""
				if len(ids) > 1 {
					groupStr = fmt.Sprintf("| Group: '%s'", providerList[pid].Group)
				}
				if cfg.opt.CheckOnly {
					result = result + fmt.Sprintf("\n>  Id:%3d | checked: %"+D+"d | avail: %"+D+"d | miss: %"+D+"d | @ '%s' %s",
						providerList[pid].id,
						providerList[pid].articles.checked,
						providerList[pid].articles.available,
						providerList[pid].articles.missing,
						providerList[pid].Name,
						groupStr,
					)
				} else {
					result = result + fmt.Sprintf("\n>  Id:%3d | checked: %"+D+"d | avail: %"+D+"d | miss: %"+D+"d | dl: %"+D+"d | up: %"+D+"d | @ '%s' %s",
						providerList[pid].id,
						providerList[pid].articles.checked,
						providerList[pid].articles.available,
						providerList[pid].articles.missing,
						providerList[pid].articles.downloaded,
						providerList[pid].articles.refreshed,
						providerList[pid].Name,
						groupStr,
					)
				}
				if len(ids) > 1 {
					pGchecked += providerList[pid].articles.checked
					pGavailable += providerList[pid].articles.available
					pGmissing += providerList[pid].articles.missing
					pGdownloaded += providerList[pid].articles.downloaded
					pGrefreshed += providerList[pid].articles.refreshed
				}
				providerList[pid].mux.RUnlock()
			} // end for pid in range ids
			if len(ids) > 1 {
				// count totals only if we have more than 1 provider in group
				result = result + fmt.Sprintf("\n> ^Totals | checked: %"+D+"d | avail: %"+D+"d | miss: %"+D+"d | dl: %"+D+"d | up: %"+D+"d | Group: '%s'",
					pGchecked, pGavailable, pGmissing, pGdownloaded, pGrefreshed, group,
				)
			}
		}
	}
	return
} // end func Results
