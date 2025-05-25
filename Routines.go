package main

import (
	"fmt"
	"log"
	"time"
)

func IsSegmentStupid(item *segmentChanItem) (crazy bool) {
	// dead.. or done processing? not sure....
	// check me bug? NoDownload-flag !
	crazy = ((len(item.availableOn) == 0 && len(item.missingOn) == len(providerList)) ||
		((len(item.availableOn)-len(item.ignoreDlOn)) == 0 && len(item.missingOn)+len(item.ignoreDlOn) == len(providerList)))
	return
}

func GoCheckRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) error {
	if cfg.opt.SloMoC > 0 {
		time.Sleep(time.Duration(cfg.opt.SloMoC) * time.Millisecond)
	}
	Counter.incr("GoCheckRoutines")
	defer Counter.decr("GoCheckRoutines")

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) checking seg.Id='%s' @ '%s' remainingInChan=%d/%d", wid, item.segment.Id, provider.Name, len(segmentChansCheck[provider.Group]), cap(segmentChansCheck[provider.Group]))
	}

	if cacheON && !cfg.opt.CheckCacheOnBoot {
		cache.CheckCache(item)
	}

	connitem := SharedConnGet(sharedCC)
	if connitem == nil {
		newconnitem, err := provider.Conns.GetConn()
		if err != nil {
			log.Printf("ERROR CheckRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
			return err
		}
		connitem = newconnitem
	}

	code, err := CMD_STAT(provider, connitem, item)
	//checkedAt := 0 // segmentBar
	switch code {
	case 223:
		// messageid found at provider
		for pid, prov := range providerList {
			if prov.Group != provider.Group {
				continue
			}
			/* ??? moved check NoDownload and nodl counter to pushDL() */

			switch provider.NoDownload {
			case false:
				// pass
			case true:
				// check me bug? NoDownload-flag !
				// only flag as available on provider if provider actually allows downloading...?
				item.mux.Lock()
				item.ignoreDlOn[pid] = true
				item.mux.Unlock()
			}

			item.mux.Lock()
			item.availableOn[pid] = true
			delete(item.missingOn, pid)
			item.checkedAt++
			item.mux.Unlock()
		}
		//checkedAt += item.checkedAt // segmentBar
		provider.mux.Lock() // mutex #939d articles.checked/available++
		provider.articles.checked++
		provider.articles.available++
		provider.mux.Unlock() // mutex #939d articles.checked/available++
		//fileStatLock.Lock()
		//fileStat[item.num].available[provider.Name]++
		//fileStatLock.Unlock()

	default:
		if code == 0 && err != nil {
			// connection problem, closed?
			provider.Conns.CloseConn(connitem, sharedCC) // close conn on error
			log.Printf("WARN checking seg.Id='%s' failed @ '%s' err='%v' re-queued", item.segment.Id, provider.Name, err)
			time.Sleep(time.Second * 5)
			segmentChansCheck[provider.Group] <- item
			return err

		} else {
			// messageid NOT found at provider
			item.mux.Lock()
			for id, prov := range providerList {
				if prov.Group != provider.Group {
					continue
				}
				item.missingOn[id] = true
				if code == 451 {
					item.dmcaOn[id] = true
				}
				item.checkedAt++
			}

			//checkedAt += item.checkedAt // segmentBar
			item.mux.Unlock()
			provider.mux.Lock() // mutex #3b99 articles.checked/missing++
			provider.articles.checked++
			provider.articles.missing++
			provider.mux.Unlock() // mutex #3b99 articles.checked/missing++
		}
	} // end switch

	/*
		if cfg.opt.Bar && checkedAt == len(providerList) {
			segmentBar.Increment()
			segmentBar.SetMessage(fmt.Sprintf("item:%d", item.segment.Number))
		}
	*/

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) CheckRoutine quit '%s'", wid, provider.Name)
	}

	SharedConnReturn(sharedCC, connitem)
	//provider.Conns.ParkConn(provider, connitem)
	return nil
} // end func GoCheckRoutine

func GoDownsRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) error {
	if cfg.opt.CheckOnly {
		return nil
	}
	if cfg.opt.SloMoD > 0 {
		time.Sleep(time.Duration(cfg.opt.SloMoD) * time.Millisecond)
	}
	Counter.incr("GoDownsRoutines")
	defer Counter.decr("GoDownsRoutines")

	who := fmt.Sprintf("DR=%d@'%s' seg.Id='%s'", wid, provider.Name, item.segment.Id)
	memlim.MemCheckWait(who, item)

	// check cache before download
	if cacheON && cache.ReadCache(item) > 0 {
		// item has been read from cache
		Counter.decr("dlQueueCnt")       // decrease temporary counter when read from cache
		Counter.decr("TOTAL_dlQueueCnt") // decrease total counter when read from cache
		//memlim.MemReturn(who+":cacheRead", item)
		return nil
	}

	item.mux.RLock()                  // mutex #de94
	if item.ignoreDlOn[provider.id] { // check me bug? NoDownload-flag !
		item.mux.RUnlock() // mutex #de94
		memlim.MemReturn(who+"ignoreDlOn", item)
		return nil
	}
	item.mux.RUnlock() // mutex #de94

	connitem := SharedConnGet(sharedCC)
	if connitem == nil {
		newconnitem, err := provider.Conns.GetConn()
		if err != nil {
			memlim.MemReturn("MemRetOnERR 'GetConn':"+who, item)
			log.Printf("ERROR GoDownsRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
			return err
		}
		connitem = newconnitem
	}

	code, msg, err := CMD_ARTICLE(provider, connitem, item)

	switch code {
	case 220:
		if err != nil {
			log.Printf("GoDownsRoutine got code 220 but err='%v'", err)
		}
		Counter.decr("dlQueueCnt") // on code 220
		Counter.add("TMP_RXbytes", uint64(item.size))
		Counter.add("TOTAL_RXbytes", uint64(item.size))

		for pid, prov := range providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.mux.Lock() // mutex #0a11
			item.availableOn[pid] = true
			delete(item.missingOn, pid)
			item.mux.Unlock() // mutex #0a11
		}
		item.mux.Lock()      // mutex #e96b
		item.flagisDL = true // FIXME REVIEW should be set to false here if crc32 reports an error but code 220 is still there....
		item.flaginDL = false
		if cfg.opt.ByPassSTAT {
			item.checkedAt++
		}
		item.mux.Unlock()   // mutex #e96b
		provider.mux.Lock() // mutex #918f articles.available++/downloaded++
		if cfg.opt.ByPassSTAT {
			provider.articles.available++
		}
		provider.articles.downloaded++
		provider.mux.Unlock() // mutex #918f articles.available++/downloaded++
		if cacheON {
			cache.Add2Cache(item)
		}
	default:
		if code == 0 && err != nil {
			// connection problem, closed?
			provider.Conns.CloseConn(connitem, sharedCC) // close conn on error

			item.mux.RLock() // mutex #74b7
			failed := item.fails
			isdead := IsSegmentStupid(item)
			//isdead := len(item.availableOn) == 0 && len(item.missingOn) == len(providerList) // check me bug? NoDownload-flag !
			item.mux.RUnlock() // mutex #74b7

			if !isdead && failed <= 5 {

				log.Printf("WARN CMD_ARTICLE failed re-queue in 5 sec: seg.Id='%s' @ '%s' failed=%d isdead=%t code=%d msg='%s' err='%v'", item.segment.Id, provider.Name, failed, isdead, code, msg, err)
				item.mux.Lock() // mutex #ee45
				item.fails++
				item.mux.Unlock() // mutex #ee45
				time.Sleep(time.Second * 5)
				memlim.MemReturn("MemRetOnERR 'CMD_ARTICLE re-queued':"+who, item)
				segmentChansDowns[provider.Group] <- item

			} else if isdead {
				Counter.decr("dlQueueCnt") // code=0 && err != nil && isdead
				memlim.MemReturn("MemRetOnERR 'CMD_ARTICLE failed':"+who, item)
				log.Printf("!!!! DEBUG GoDownsRoutine: isdead seg.Id='%s' code=0 or err='%v'", item.segment.Id, err)
			}
			return err

		} else {
			// downloading article failed from provider
			for pid, prov := range providerList {
				if prov.Group != provider.Group {
					continue
				}
				// modify availableON and missingON lists
				item.mux.Lock() // mutex #821d
				item.missingOn[pid] = true
				switch code {
				case 451:
					item.dmcaOn[pid] = true
				case 99932:
					// got bad_crc
					item.errorOn[pid] = true
				} // end switch code
				// remove provider from availableON list
				delete(item.availableOn, pid)
				item.mux.Unlock() // mutex #821d
			}

			item.mux.Lock() // mutex #030b
			//isdead := len(item.availableOn) == 0 && len(item.missingOn) == len(providerList) // check me bug? NoDownload-flag !
			isdead := IsSegmentStupid(item)
			item.flaginDL = false
			item.mux.Unlock() // mutex #030b

			provider.mux.Lock() // mutex #24b0 articles.available--/missing++
			if provider.articles.available > 0 {
				provider.articles.available--
			}
			provider.articles.missing++
			provider.mux.Unlock() // mutex #24b0 articles.available--/missing++

			Counter.decr("dlQueueCnt")
			if isdead {

				if cfg.opt.YencWrite && Counter.get("TOTAL_yencQueueCnt") > 0 {
					Counter.decr("yencQueueCnt")
				}
			}
			if code == 430 {
				if cfg.opt.Print430 {
					log.Printf("INFO DownsRoutine code=430 msg='%s' seg.Id='%s' seg.N=%d isdead=%t availableOn=%d ignoreDlOn=%d missingOn=%d pl=%d", msg, item.segment.Id, item.segment.Number, isdead, len(item.availableOn), len(item.ignoreDlOn), len(item.missingOn), len(providerList))
				}
			}
			memlim.MemReturn("MemRetOnERR 'downloading article failed':"+who, item)
		}
	} // end switch code
	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) DownsRoutine quit '%s'", wid, provider.Name)
	}

	SharedConnReturn(sharedCC, connitem)
	//provider.Conns.ParkConn(provider, connitem)
	return nil
} // end func DownsRoutine

func GoReupsRoutine(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) error {
	if cfg.opt.CheckOnly {
		return nil
	}
	if cfg.opt.SloMoU > 0 {
		time.Sleep(time.Duration(cfg.opt.SloMoU) * time.Millisecond)
	}
	Counter.incr("GoReupsRoutines")
	defer Counter.decr("GoReupsRoutines")

	who := fmt.Sprintf("UR=%d@'%s' seg.Id='%s'", wid, provider.Name, item.segment.Id)

	connitem := SharedConnGet(sharedCC)

	/*
		connitem, err := provider.Conns.GetConn(wid, provider)
		if err != nil {
			log.Printf("ERROR GoReupsRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
			memlim.MemReturn("MemRetOnERR:"+who, item)
			return err
		}
	*/

	var uploaded, unwanted, retry, clearmem bool

	doPOST, doIHAVE := false, false

	//provider.mux.RLock() // FIXME TODO #b8bd287b:
	if provider.PreferIHAVE && provider.capabilities.ihave {
		doIHAVE = true
	} else if provider.capabilities.post {
		doPOST = true
	} else if provider.capabilities.ihave {
		doIHAVE = true
	} else {
		//provider.mux.RUnlock() // FIXME TODO #b8bd287b:
		provider.Conns.CloseConn(connitem, sharedCC) // close conn on error
		return fmt.Errorf("WARN selecting upload mode failed '%s' caps='%#v'", provider.Name, provider.capabilities)
	}
	//provider.mux.RUnlock() // FIXME TODO #b8bd287b:

	if cfg.opt.Debug {
		log.Printf("ReUp: (%d) seg.Id='%s' @ '%s' doPost=%t doIHAVE=%t", wid, item.segment.Id, provider.Name, doPOST, doIHAVE)
	}

	if doPOST {
		code, _, err := CMD_POST(provider, connitem, item)
		Counter.add("TMP_TXbytes", uint64(item.size))
		Counter.add("TOTAL_TXbytes", uint64(item.size))

		switch code {

		case 240:
			// article posted
			uploaded = true
			// pass

		default:
			if code == 0 && err != nil {
				memlim.MemReturn("MemRetOnERR 'CMD_POST':"+who, item)
				// connection problem, closed?
				provider.Conns.CloseConn(connitem, sharedCC) // close conn on error
				log.Printf("WARN CMD_POST failed seg.Id='%s' @ '%s' err='%v' re-queued", item.segment.Id, provider.Name, err)
				time.Sleep(time.Second * 5)
				segmentChansReups[provider.Group] <- item
				return err
			}
			if code > 0 {
				// Posting failed with any error code
				unwanted = true // TODO checkme: maybe flag as retry?
				// pass
			}
		}

	} else if doIHAVE {
		code, _, err := CMD_IHAVE(provider, connitem, item)
		Counter.add("TMP_TXbytes", uint64(item.size))
		Counter.add("TOTAL_TXbytes", uint64(item.size))
		switch code {

		case 235:
			// article accepted
			uploaded = true
			// pass

		case 436:
			// retry later
			retry = true
			// pass

		default:
			if code == 0 && err != nil {
				// connection problem, closed?
				memlim.MemReturn("MemRetOnERR 'CMD_IHAVE':"+who, item)
				provider.Conns.CloseConn(connitem, sharedCC) // close conn on error
				log.Printf("WARN CMD_IHAVE failed seg.Id='%s' @ '%s' err='%v' re-queued", item.segment.Id, provider.Name, err)
				time.Sleep(time.Second * 5)
				segmentChansReups[provider.Group] <- item
				return err
			}
			unwanted = true
			// pass

		} // end switch code
	}

	if uploaded {

		// react to finished upload
		item.mux.Lock()
		for id, prov := range providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.availableOn[id] = true
			delete(item.missingOn, id)
		}
		item.flaginUP = false
		item.flagisUP = true
		item.mux.Unlock()
		provider.mux.Lock() // mutex #87c9 articles.refreshed++
		provider.articles.refreshed++
		provider.mux.Unlock() // mutex #87c9 articles.refreshed++
		clearmem = true
		Counter.decr("upQueueCnt")

	} else if unwanted {

		// item is unwanted at provider, set flag.
		log.Printf("Flag Unwanted seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		moreProvider := false
		item.mux.Lock()
		for pid, prov := range providerList {
			if prov.Group != provider.Group {
				if !prov.NoUpload && (prov.capabilities.post || prov.capabilities.ihave) {
					if item.missingOn[pid] && !item.unwantedOn[pid] {
						moreProvider = true
					}
				}
				continue
			}
			item.unwantedOn[pid] = true
		}
		item.flaginUP = false
		item.mux.Unlock()

		// TODO: need better check if we have other providers
		// with posting capabilities and queue item to one of them
		if !moreProvider {
			clearmem = true
			Counter.decr("upQueueCnt")
			Counter.decr("TOTAL_upQueueCnt")
		}

	} else if retry {

		log.Printf("Flag Retry seg.Id='%s' @ '%s'", item.segment.Id, provider.Name)
		item.mux.Lock()
		item.retryIn = time.Now().Unix() + 15
		item.retryOn[provider.id] = true
		item.flaginUP = false
		item.mux.Unlock()
		Counter.decr("upQueueCnt")
		Counter.decr("TOTAL_upQueueCnt")
		//clearmem = true
	}

	/*
		if cfg.opt.Bar {
			BarMutex.Lock()
			if upBarStarted {
				upBar.Increment()
			}
			BarMutex.Unlock()
		}
	*/

	if clearmem {
		if cfg.opt.Debug {
			log.Printf("OK ReupsRoutine clearmem seg.Id='%s'", item.segment.Id)
		}
		// clears this item content from memory because it got uploaded
		//doMemReturn := true
		item.mux.Lock()
		item.lines = []string{}
		/* // watch out for broken wings #99ffff!
		 * // ideas was to not release memory here until yenc has been written, if flag is set...
		 * // but something slows down by 90% if broken wings are enabled and it freezes...
		if item.flaginYenc {
			doMemReturn = false
		}*/
		item.mux.Unlock()
		//if doMemReturn {
		memlim.MemReturn(who, item)
		//}
	} else {
		log.Printf("WARN ReupsRoutine NO clearmem seg.Id='%s'", item.segment.Id)
	}

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) ReupsRoutine quit '%s'", wid, provider.Name)
	}

	SharedConnReturn(sharedCC, connitem)
	//provider.Conns.ParkConn(provider, connitem)
	return nil
} // end func ReupsRoutine

func StopRoutines() {
	if cfg.opt.Debug {
		log.Print("StopRoutines: pushing")
	}
	// pushing nil into the segment chans will stop the routines
	for _, provider := range providerList {
		if segmentChansCheck[provider.Group] != nil {
			segmentChansCheck[provider.Group] <- nil
		}
		if segmentChansDowns[provider.Group] != nil {
			segmentChansDowns[provider.Group] <- nil
		}
		if segmentChansReups[provider.Group] != nil {
			segmentChansReups[provider.Group] <- nil
		}
	}
	// push an empty stuct to stop_chan will stop everyone listening
	stop_chan <- struct{}{}
	if cfg.opt.Debug {
		log.Print("StopRoutines: released")
	}
} // end func StopRoutines
