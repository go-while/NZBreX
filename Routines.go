package main

import (
	"fmt"
	"log"
	"os"
	"time"
)

func (s *SESSION) IsSegmentStupid(item *segmentChanItem, doLock bool) (crazy bool) {
	if doLock {
		item.mux.RLock()
		defer item.mux.RUnlock()
	}
	// no mutex here! should be locked from caller before or race cond here!
	// dead.. or done processing? not sure....
	// check me bug? NoDownload-flag !
	crazy = ((len(item.availableOn) == 0 && len(item.missingOn) == len(s.providerList)) ||
		((len(item.availableOn)-len(item.ignoreDlOn)) == 0 && len(item.missingOn)+len(item.ignoreDlOn) == len(s.providerList)))
	return
}

func (s *SESSION) GoCheckRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) (code int, err error) {
	if cfg.opt.SloMoC > 0 {
		dlog(cfg.opt.BUG, "GoCheckRoutine SloMoC=%d", cfg.opt.SloMoC)
		time.Sleep(time.Duration(cfg.opt.SloMoC) * time.Millisecond)
	}
	GCounter.Incr("GoCheckRoutines")
	defer GCounter.Decr("GoCheckRoutines")

	dlog(cfg.opt.DebugCR, "GoWorker (%d) checking seg.Id='%s' @ '%s' remainingInChan=%d/%d", wid, item.segment.Id, provider.Name, len(s.segmentChansCheck[provider.Group]), cap(s.segmentChansCheck[provider.Group]))

	if cacheON && !cfg.opt.CheckCacheOnBoot {
		cache.CheckCache(item)
	}
	var connitem *ConnItem
	if sharedCC != nil {
		connitem, err = SharedConnGet(sharedCC, provider)
	} else {
		connitem, err = provider.ConnPool.GetConn()
	}
	if err != nil {
		return 0, fmt.Errorf("ERROR in GoCheckRoutine: ConnGet '%s' connitem='%v' sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}
	if connitem == nil || connitem.conn == nil {
		return 0, fmt.Errorf("ERROR in GoCheckRoutine: ConnGet got nil item or conn '%s' connitem='%v'  sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}

	code, err = CMD_STAT(connitem, item)
	if code == 0 && err != nil {
		// connection problem, closed?
		provider.ConnPool.CloseConn(connitem, sharedCC) // close conn on error
		dlog(always, "WARN checking seg.Id='%s' failed @ '%s' err='%v'", item.segment.Id, provider.Name, err)
		return code, err
	}

	item.mux.Lock() // LOCK item for switch
	switch code {
	case 223:
		// messageid found at provider
		for pid, prov := range s.providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.checkedOn++
			item.availableOn[pid] = true
			delete(item.missingOn, pid)
			if provider.NoDownload {
				item.ignoreDlOn[pid] = true
			}
			if provider.NoUpload {
				item.ignoreUlOn[pid] = true
			}
		}
		//checkedAt += item.checkedAt // segmentBar
		provider.mux.Lock() // mutex #939d articles.checked/available++
		provider.articles.checked++
		provider.articles.available++
		provider.mux.Unlock() // mutex #939d articles.checked/available++

		if cfg.opt.Csv {
			s.fileStatLock.Lock()
			s.fileStat[s.nzbName].available[provider.Name]++
			s.fileStatLock.Unlock()
		}

	default:
		// messageid NOT found at provider
		for id, prov := range s.providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.checkedOn++
			switch code {
			case 430:
				// 430: no download on this provider
				item.missingOn[id] = true
				err = nil // reset error
			case 451:
				// 451: DMCA on this provider
				item.dmcaOn[id] = true
				err = nil // reset error

			default:
				// uknown code
				dlog(always, "GoCheckRoutine CMD_STAT seg.Id='%s' code=%d err='%v' @ '%s'", item.segment.Id, code, err, provider.Name)
				item.ignoreDlOn[id] = true
				item.missingOn[id] = true
				item.errorOn[id] = true
			}
		}

		// update provider statistics
		provider.mux.Lock() // mutex #3b99 articles.checked/missing++
		provider.articles.checked++
		provider.articles.missing++
		provider.mux.Unlock() // mutex #3b99 articles.checked/missing++
		if cfg.opt.Csv {
			s.fileStatLock.Lock()
			s.fileStat[s.nzbName].missing[provider.Name]++
			s.fileStatLock.Unlock()
		}

	} // end switch
	item.mux.Unlock()

	dlog(cfg.opt.DebugCR, "GoWorker (%d) CheckRoutine end seg.Id='%s' '%s'", wid, item.segment.Id, provider.Name)
	if sharedCC != nil {
		SharedConnReturn(sharedCC, connitem, provider)
	} else {
		provider.ConnPool.ParkConn(wid, connitem, "GoCheckRoutine")
	}
	if testing {
		go func(item *segmentChanItem) {
			s.WorkDividerChan <- &WrappedItem{wItem: item, src: "CR"} // send connitem to work divider
		}(item)
	}

	dlog(cfg.opt.DebugCR, "GoCheckRoutine CMD_STAT seg.Id='%s' code=223 err='%v' @ '%s'", item.segment.Id, err, provider.Name)

	return code, err
} // end func GoCheckRoutine

func (s *SESSION) GoDownsRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) (int, error) {
	if cfg.opt.SloMoD > 0 {
		dlog(cfg.opt.BUG, "GoDownsRoutine SloMoD=%d", cfg.opt.SloMoD)
		time.Sleep(time.Duration(cfg.opt.SloMoD) * time.Millisecond)
	}
	GCounter.Incr("GoDownsRoutines")
	defer GCounter.Decr("GoDownsRoutines")

	// check cache before download
	if cacheON && cache.ReadCache(item) > 0 {
		// item has been read from cache
		//DecreaseDLQueueCnt() // decrease when read from cache // DISABLED
		//memlim.MemReturn(who+":cacheRead", item)
		return 920, nil
	}
	start := time.Now() // start time for this routine

	var err error
	var connitem *ConnItem
	if sharedCC != nil {
		connitem, err = SharedConnGet(sharedCC, provider)
	} else {
		connitem, err = provider.ConnPool.GetConn()
	}
	if err != nil {
		return 0, fmt.Errorf("ERROR in GoDownsRoutine: ConnGet '%s' connitem='%v' sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}
	if connitem == nil || connitem.conn == nil {
		return 0, fmt.Errorf("ERROR in GoDownsRoutine: ConnGet got nil item or conn '%s' connitem='%v'  sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}
	dlog(cfg.opt.DebugWorker, "GoDownsRoutine got connitem='%v' sharedCC='%v' --> CMD_ARTICLE seg.Id='%s'", connitem, sharedCC, item.segment.Id)

	startArticle := time.Now()
	code, msg, rxb, err := CMD_ARTICLE(connitem, item)

	if err != nil {
		dlog(always, "ERROR in GoDownsRoutine: CMD_ARTICLE seg.Id='%s' @ '%s'#'%s' err='%v'", item.segment.Id, provider.Name, provider.Group, err)
		// handle connection problem / closed connection
		provider.ConnPool.CloseConn(connitem, sharedCC) // close conn on error
		return 0, fmt.Errorf("error in GoDownsRoutine: CMD_ARTICLE seg.Id='%s' @ '%s'#'%s' err='%v'", item.segment.Id, provider.Name, provider.Group, err)
	}

	dlog(cfg.opt.Debug, "GoDownsRoutine CMD_ARTICLE seg.Id='%s' code=%d msg='%s' err='%v' took=(%d ms)", item.segment.Id, code, msg, err, time.Since(startArticle).Milliseconds())

	switch code {
	case 220:
		dlog(cfg.opt.Debug, "GoDownsRoutine CMD_ARTICLE case 220: seg.Id='%s' code=220 msg='%s' err='%v'", item.segment.Id, msg, err)
		//DecreaseDLQueueCnt() // on code 220 // DISBALED
		if item.size == 0 {
			dlog(always, "ERROR GoDownsRoutine CMD_ARTICLE seg.Id='%s' @ '%s'#'%s' code=220 but item.size=0! msg='%s' err='%v'", item.segment.Id, provider.Name, provider.Group, msg, err)
			os.Exit(1) // FIXME REVIEW: should not happen, but if it does, we exit here
		}
		// to calulate total download speed of this session working on a nzb
		s.counter.Add("TMP_RXbytes", uint64(item.size))
		s.counter.Add("TOTAL_RXbytes", uint64(item.size))

		// to calulate total download speed of this provider
		provider.ConnPool.counter.Add("TMP_RXbytes", uint64(item.size))
		provider.ConnPool.counter.Add("TOTAL_RXbytes", uint64(item.size))

		// to calulate global total download speed
		GCounter.Add("TMP_RXbytes", uint64(item.size))
		GCounter.Add("TOTAL_RXbytes", uint64(item.size))
		// got the article, set our group to availableOn
	flagProviderdl:
		for pid, prov := range s.providerList {
			if prov.Group != provider.Group {
				// not our group
				continue flagProviderdl
			}
			item.mux.Lock() // mutex #0a11
			item.availableOn[pid] = true
			delete(item.missingOn, pid)
			item.mux.Unlock() // mutex #0a11
		}
		item.mux.Lock() // mutex #e96b
		item.rxb += rxb
		item.flagisDL = true
		item.flaginDL = false
		item.flaginDLMEM = false
		if cfg.opt.ByPassSTAT {
			item.checkedOn++
		}
		item.PrintItemFlags(cfg.opt.DebugFlags, false, "post-CMD_ARTICLE: case 220")
		item.mux.Unlock() // mutex #e96b

		// update statistics
		provider.mux.Lock() // mutex #918f articles.available++/downloaded++
		if cfg.opt.ByPassSTAT {
			provider.articles.available++
			if cfg.opt.Csv {
				s.fileStatLock.Lock()
				s.fileStat[s.nzbName].available[provider.Name]++
				s.fileStatLock.Unlock()
			}
		}
		provider.articles.downloaded++
		provider.mux.Unlock() // mutex #918f articles.available++/downloaded++

		// pass item to cache.
		dlog(cfg.opt.DebugWorker, "DEBUG GoDownsRoutine CMD_ARTICLE: reached cache.Add2Cache seg.Id='%s' @ '%s'#'%s'", item.segment.Id, provider.Name, provider.Group)
		cache.Add2Cache(item)
		// pass to ParkConn

	default:
		dlog(cfg.opt.Debug, "GoDownsRoutine CMD_ARTICLE: case default: seg.Id='%s' code=%d msg='%s' err='%v' '%s'#'%s'", item.segment.Id, code, msg, err, provider.Name, provider.Group)
		// downloading article failed from provider-group
	setFlags:
		for pid, prov := range s.providerList {
			if prov.Group != provider.Group {
				// not our group
				continue setFlags
			}
			// modify availableON and missingON lists in our group
			item.mux.Lock() // mutex #821d

			switch code {
			case 430:
				item.missingOn[pid] = true
			case 451:
				item.dmcaOn[pid] = true
				item.ignoreDlOn[pid] = true
				item.missingOn[pid] = true
			case 99932:
				// got bad_crc
				item.errorOn[pid] = true
				item.ignoreDlOn[pid] = true
				dlog(always, "CRC32 failed seg.Id='%s' @ '%s'#'%s'", item.segment.Id, provider.Name, provider.Group)
			default:
				item.ignoreDlOn[pid] = true
			} // end switch code

			// remove provider from availableON list
			delete(item.availableOn, pid)
			item.mux.Unlock() // mutex #821d
		}

		item.mux.Lock() // mutex #030b
		//isdead := len(item.availableOn) == 0 && len(item.missingOn) == len(s.providerList) // check me bug? NoDownload-flag !
		isdead := s.IsSegmentStupid(item, false)
		item.flaginDL = false
		item.mux.Unlock() // mutex #030b

		provider.mux.Lock() // mutex #24b0 articles.available--/missing++
		if provider.articles.available > 0 {
			provider.articles.available--
		}
		provider.articles.missing++
		provider.mux.Unlock() // mutex #24b0 articles.available--/missing++
		if cfg.opt.Csv {
			s.fileStatLock.Lock()
			s.fileStat[s.nzbName].missing[provider.Name]++
			s.fileStatLock.Unlock()
		}
		moreProvider := false // tmp flag if we have more providers to download from
		for pid, prov := range s.providerList {
			if prov.Group != provider.Group {
				// check other groups
				if !prov.NoDownload {
					if item.availableOn[pid] && !item.ignoreDlOn[pid] && !item.errorOn[pid] {
						moreProvider = true
						break // we have at least one provider to download from
					}
				}
				continue
			}
		}
		if !moreProvider {
			//DecreaseDLQueueCnt() // failed article download, CODE != 220 or 0 // DISABLED
		}
		if isdead {
			if cfg.opt.YencWrite && GCounter.GetValue("TOTAL_yencQueueCnt") > 0 {
				GCounter.Decr("yencQueueCnt")
			}
		}
		dlog((code == 430 && cfg.opt.Print430), "INFO DownsRoutine code=430 msg='%s' seg.Id='%s' seg.N=%d isdead=%t availableOn=%d ignoreDlOn=%d missingOn=%d pl=%d", msg, item.segment.Id, item.segment.Number, isdead, len(item.availableOn), len(item.ignoreDlOn), len(item.missingOn), len(s.providerList))

		//memlim.MemReturn("MemRetOnERR 'downloading article failed':"+who, item)  // DISABLED
		//}
	} // end switch code

	if sharedCC != nil {
		SharedConnReturn(sharedCC, connitem, provider)
	} else {
		provider.ConnPool.ParkConn(wid, connitem, "GoDownsRoutine")
	}
	if testing {
		go func(item *segmentChanItem) {
			s.WorkDividerChan <- &WrappedItem{wItem: item, src: "DR"} // send connitem to work divider
		}(item)
	}
	dlog(cfg.opt.DebugDR || cfg.opt.DebugWorker, "GoDownsRoutine end seg.Id='%s' @ '%s' took=(%d ms) err='%v'", item.segment.Id, provider.Name, time.Since(start).Milliseconds(), err)
	return code, err
} // end func GoDownsRoutine

func (s *SESSION) GoReupsRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) (int, error) {
	if cfg.opt.SloMoU > 0 {
		dlog(cfg.opt.BUG, "GoReupsRoutine SloMoU=%d", cfg.opt.SloMoU)
		time.Sleep(time.Duration(cfg.opt.SloMoU) * time.Millisecond)
	}
	GCounter.Incr("GoReupsRoutines")
	defer GCounter.Decr("GoReupsRoutines")
	//who := fmt.Sprintf("UR=%d@'%s' seg.Id='%s'", wid, provider.Name, item.segment.Id)  // DISABLED MEMRETURN

	var err error
	var connitem *ConnItem
	if sharedCC != nil {
		connitem, err = SharedConnGet(sharedCC, provider)
	} else {
		connitem, err = provider.ConnPool.GetConn()
	}
	if err != nil {
		return 0, fmt.Errorf("ERROR in GoReupsRoutine: ConnGet '%s' connitem='%v' sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}
	if connitem == nil || connitem.conn == nil {
		return 0, fmt.Errorf("ERROR in GoReupsRoutine: ConnGet got nil item or conn '%s' connitem='%v'  sharedCC='%v' err='%v'", provider.Name, connitem, sharedCC, err)
	}

	var uploaded, unwanted, retry bool
	var cmd uint8

	//provider.mux.RLock() // FIXME TODO #b8bd287b:
	if provider.PreferIHAVE && provider.capabilities.ihave {
		cmd = 2
	} else if provider.capabilities.post {
		cmd = 1
	} else if provider.capabilities.ihave {
		cmd = 2
	} else {
		//provider.mux.RUnlock() // FIXME TODO #b8bd287b:
		provider.ConnPool.CloseConn(connitem, sharedCC) // close conn on error
		return 0, fmt.Errorf("WARN selecting upload mode failed '%s' caps='%#v'", provider.Name, provider.capabilities)
	}
	//provider.mux.RUnlock() // FIXME TODO #b8bd287b:

	dlog(cfg.opt.DebugUR || cfg.opt.DebugWorker, "ReUp: init seg.Id='%s' @ '%s'#'%s' cmd='%d' (UR=%d)", item.segment.Id, provider.Name, provider.Group, cmd, wid)

	var code int
	var msg string
	var txb uint64  // tx bytes
	freemem := true // flag to free or not to clear memory after upload

	switch cmd {
	case 1:
		code, msg, txb, err = CMD_POST(connitem, item)
		switch code {
		case 240:
			uploaded = true
		default:
			dlog(always, "ERROR in GoReupsRoutine: CMD_POST seg.Id='%s' @ '%s'#'%s' err='%v'", item.segment.Id, provider.Name, provider.Group, err)
			err = fmt.Errorf("error in GoReupsRoutine: CMD_POST seg.Id='%s' @ '%s'#'%s' code=%d msg='%s' err='%v'", item.segment.Id, provider.Name, provider.Group, code, msg, err)
			unwanted = true
		}

	case 2:
		code, msg, txb, err = CMD_IHAVE(connitem, item)
		switch code {
		case 235:
			uploaded = true
			// pass
		case 436:
			retry = true
			// pass
		case 437:
			unwanted = true
			// pass
		default:
			dlog(always, "ERROR in GoReupsRoutine: CMD_IHAVE seg.Id='%s' @ '%s'#'%s' code=%d msg='%s' err='%v'", item.segment.Id, provider.Name, provider.Group, code, msg, err)
			err = fmt.Errorf("error in GoReupsRoutine: CMD_IHAVE seg.Id='%s' @ '%s'#'%s' code=%d msg='%s' err='%v'", item.segment.Id, provider.Name, provider.Group, code, msg, err)
			unwanted = true
		}
	} // end switch cmd

	if uploaded {

		// to calulate total upload speed of this session working on a nzb
		s.counter.Add("TMP_TXbytes", uint64(item.size))
		s.counter.Add("TOTAL_TXbytes", uint64(item.size))

		// to calulate total upload speed of this provider
		provider.ConnPool.counter.Add("TMP_TXbytes", uint64(item.size))
		provider.ConnPool.counter.Add("TOTAL_TXbytes", uint64(item.size))

		// to calulate global total upload speed
		GCounter.Add("TMP_TXbytes", uint64(item.size))
		GCounter.Add("TOTAL_TXbytes", uint64(item.size))
		// react to finished upload
		item.mux.Lock()
		item.txb += txb
		// flag item on our group as available
		for id, prov := range s.providerList {
			if prov.Group != provider.Group {
				// not our group
				continue
			}
			//item.availableOn[id] = true
			item.uploadedTo[id] = true
			delete(item.missingOn, id)
		}
		item.flaginUP = false
		item.flagisUP = true
		item.mux.Unlock()
		// update provider statistics
		provider.mux.Lock() // mutex #87c9 articles.refreshed++
		provider.articles.refreshed++
		provider.mux.Unlock() // mutex #87c9 articles.refreshed++
		//freemem = true // DISABLED
		//GCounter.Decr("upQueueCnt") // DISABLED

	} else if unwanted {

		// item is unwanted at provider, set flag.
		dlog(always, "Flag Unwanted seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		moreProvider := false
		item.mux.Lock()
		for pid, prov := range s.providerList {
			if prov.Group != provider.Group {
				// check other groups
				if !prov.NoUpload && (prov.capabilities.post || prov.capabilities.ihave) {
					if item.missingOn[pid] && !item.unwantedOn[pid] && !item.uploadedTo[pid] {
						moreProvider = true
						break // we have at least one more provider to upload to
					}
				}
				continue
			}
			item.unwantedOn[pid] = true // flag as unwanted on this provider-group
		}
		item.flaginUP = false // no more in upload because it was unwanted
		item.mux.Unlock()

		// TODO: need better check if we have other providers
		// with posting capabilities and queue item to one of them
		if moreProvider {
			freemem = false
			//GCounter.Decr("upQueueCnt") // DISABLED
			//GCounter.Decr("TOTAL_upQueueCnt") // DISABLED
		}

	} else if retry {

		dlog(always, "Flag Retry seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		item.mux.Lock()
		item.retryIn = time.Now().Unix() + 15
		item.retryOn[provider.id] = true
		item.flaginUP = false
		item.mux.Unlock()
		//GCounter.Decr("upQueueCnt") // DISABLED
		//GCounter.Decr("TOTAL_upQueueCnt") // DISABLED
		//freemem = true //REVIEW: should we clear memory here? maybe not, because we want to retry this item later!
	}

	if freemem {
		dlog(cfg.opt.DebugUR || cfg.opt.DebugWorker, "GoWorker (%d) ReupsRoutine freemem seg.Id='%s'", wid, item.segment.Id)
		// clears this item content from memory because it got uploaded
		item.mux.Lock()
		item.article = []string{} // DISABLED-NOT
		item.mux.Unlock()
	} else {
		dlog(always, "GoWorker (%d) WARN ReupsRoutine NO freemem seg.Id='%s'", wid, item.segment.Id)
	}

	if err != nil {
		dlog(always, "ERROR in GoReupsRoutine: seg.Id='%s' @ '%s'#'%s' err='%v'", item.segment.Id, provider.Name, provider.Group, err)
		// handle connection problem / closed connection
		provider.ConnPool.CloseConn(connitem, sharedCC) // close conn on error
		return 0, fmt.Errorf("error in GoReupsRoutine: seg.Id='%s' @ '%s'#'%s' err='%v'", item.segment.Id, provider.Name, provider.Group, err)
	}

	if sharedCC != nil {
		SharedConnReturn(sharedCC, connitem, provider)
	} else {
		provider.ConnPool.ParkConn(wid, connitem, "GoReupsRoutine")
	}

	dlog(cfg.opt.DebugUR || cfg.opt.DebugWorker, "GoWorker (%d) ReupsRoutine end seg.Id='%s' '%s'#'%s' err='%v'", wid, item.segment.Id, provider.Name, provider.Group, err)
	return code, err
} // end func GoReupsRoutine

func (s *SESSION) StopRoutines() {
	if cfg.opt.Debug {
		log.Print("StopRoutines: pushing")
	}
	// pushing nil into the segment chans will stop the routines
	for _, provider := range s.providerList {
		closeSegmentChannel(s.segmentChansCheck[provider.Group])
		closeSegmentChannel(s.segmentChansDowns[provider.Group])
		closeSegmentChannel(s.segmentChansReups[provider.Group])

	}
	close(s.stopChan)
	if cfg.opt.Debug {
		log.Print("StopRoutines: released")
	}
} // end func StopRoutines

func closeSegmentChannel(input chan *segmentChanItem) {
	if input == nil {
		log.Printf("closeChannel: input channel is nil")
		return
	}
	select {
	case item, ok := <-input:
		if item != nil {
			log.Printf("ERROR in closeSegmentChannel: LOST item seg.Id='%s'", item.segment.Id)
		}
		if !ok {
			if cfg.opt.Debug {
				log.Printf("closeSegmentChannel: channel %p already closed", input)
			}
			return
		}
	default:
		close(input) // close the channel if it is not already closed
		if cfg.opt.Debug {
			log.Printf("closeSegmentChannel: closed channel %p", input)
		}
	}
} // end func closeSegmentChannel
