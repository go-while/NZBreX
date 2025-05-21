package main

import (
	"fmt"
	"log"
	"time"
)

func GoCheckRoutine(wid int, provider *Provider, item *segmentChanItem) error {
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

	connitem, err := provider.Conns.GetConn(wid, provider)
	if err != nil {
		log.Printf("ERROR CheckRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
		return err
	}

	code, err := CMD_STAT(provider, connitem, item)
	//checkedAt := 0 // segmentBar
	switch code {
	case 223:
		// messageid found at provider
		item.mux.Lock()
		for id, prov := range providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.availableOn[id] = true
			delete(item.missingOn, id)
			item.checkedAt++

		}
		//checkedAt += item.checkedAt // segmentBar
		item.mux.Unlock()
		provider.mux.Lock() // mutex #939d articles.checked/available++
		provider.articles.checked++
		provider.articles.available++
		provider.mux.Unlock() // mutex #939d articles.checked/available++
		//fileStatLock.Lock()
		//fileStat[item.num].available[provider.Name]++
		//fileStatLock.Unlock()

	default:
		if code == 0 || err != nil {
			// connection problem, closed?
			provider.Conns.CloseConn(provider, connitem) // close conn on error
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
			provider.mux.Lock() // mutex #24b0 articles.checked/missing++
			provider.articles.checked++
			provider.articles.missing++
			provider.mux.Unlock() // mutex #24b0 articles.checked/missing++
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
	provider.Conns.ParkConn(provider, connitem)
	return nil
} // end func CheckRoutine

func GoDownsRoutine(wid int, provider *Provider, item *segmentChanItem) error {
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
		Counter.decr("dlQueueCnt")
		Counter.decr("TOTAL_dlQueueCnt")
		return nil
	}

	connitem, err := provider.Conns.GetConn(wid, provider)
	if err != nil {
		memlim.MemReturn("MemRetOnERR 'GetConn':"+who, item)
		log.Printf("ERROR GoDownsRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
		return err
	}

	code, err := CMD_ARTICLE(provider, connitem, item)

	switch code {
	case 220:
		Counter.decr("dlQueueCnt")
		Counter.add("TMP_RXbytes", uint64(item.size))
		Counter.add("TOTAL_RXbytes", uint64(item.size))
		item.mux.Lock()
		for id, prov := range providerList {
			if prov.Group != provider.Group {
				continue
			}
			item.availableOn[id] = true
			delete(item.missingOn, id)
		}
		item.flagisDL = true
		item.flaginDL = false
		item.mux.Unlock()
		provider.mux.Lock()
		provider.articles.downloaded++
		provider.mux.Unlock()
		if cacheON {
			cache.Add2Cache(item)
		}
	default:
		if code == 0 || err != nil {
			// connection problem, closed?
			provider.Conns.CloseConn(provider, connitem) // close conn on error

			item.mux.RLock()
			failed := item.fails
			isdead := len(item.availableOn) == 0 && len(item.missingOn) == len(providerList)
			item.mux.RUnlock()

			if failed <= 5 {

				log.Printf("WARN CMD_ARTICLE failed seg.Id='%s' @ '%s' err='%v' failed=%d re-queue in 5s", item.segment.Id, provider.Name, err, failed)
				item.mux.Lock()
				item.fails++
				item.mux.Unlock()
				time.Sleep(time.Second * 5)
				segmentChansDowns[provider.Group] <- item

			} else if isdead {
				Counter.decr("dlQueueCnt")
				Counter.decr("TOTAL_dlQueueCnt")
				memlim.MemReturn("MemRetOnERR 'CMD_ARTICLE failed':"+who, item)
			}
			return err

		} else {
			// downloading article failed from provider
			item.mux.Lock()
			for id, prov := range providerList {
				if prov.Group != provider.Group {
					continue
				}
				// modify availableON and missingON lists
				item.missingOn[id] = true
				// remove our provider from availableON list
				delete(item.availableOn, id)
			}
			isdead := len(item.availableOn) == 0 && len(item.missingOn) == len(providerList)
			item.flaginDL = false
			item.mux.Unlock()
			provider.mux.Lock() // mutex #24b0 articles.checked/missing++
			provider.articles.missing++
			provider.mux.Unlock() // mutex #24b0 articles.checked/missing++
			if isdead {
				Counter.decr("dlQueueCnt")
				Counter.decr("TOTAL_dlQueueCnt")
			}
			memlim.MemReturn("MemRetOnERR 'downloading article failed':"+who, item)
		}
	} // end switch code
	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) DownsRoutine quit '%s'", wid, provider.Name)
	}
	provider.Conns.ParkConn(provider, connitem)
	return nil
} // end func DownsRoutine

func GoReupsRoutine(wid int, provider *Provider, item *segmentChanItem) error {
	if cfg.opt.CheckOnly {
		return nil
	}
	if cfg.opt.SloMoU > 0 {
		time.Sleep(time.Duration(cfg.opt.SloMoU) * time.Millisecond)
	}
	Counter.incr("GoReupsRoutines")
	defer Counter.decr("GoReupsRoutines")

	who := fmt.Sprintf("UR=%d@'%s' seg.Id='%s'", wid, provider.Name, item.segment.Id)

	connitem, err := provider.Conns.GetConn(wid, provider)
	if err != nil {
		log.Printf("ERROR GoReupsRoutine connect failed Provider '%s' err='%v'", provider.Name, err)
		memlim.MemReturn("MemRetOnERR:"+who, item)
		return err
	}

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
		provider.Conns.CloseConn(provider, connitem) // close conn on error
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

		case 441:
			// Posting failed
			unwanted = true // TODO checkme: maybe flag as retry?
			// pass

		default:
			if code == 0 || err != nil {
				memlim.MemReturn("MemRetOnERR 'CMD_POST':"+who, item)
				// connection problem, closed?
				provider.Conns.CloseConn(provider, connitem) // close conn on error
				log.Printf("WARN CMD_POST failed seg.Id='%s' @ '%s' err='%v' re-queued", item.segment.Id, provider.Name, err)
				time.Sleep(time.Second * 5)
				segmentChansReups[provider.Group] <- item
				return err
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

		case 435:
			// article unwanted
			unwanted = true
			// pass

		case 436:
			// retry later
			retry = true
			// pass

		case 437:
			// rejected
			unwanted = true
			// pass

		default:
			if code == 0 || err != nil {
				// connection problem, closed?
				memlim.MemReturn("MemRetOnERR 'CMD_IHAVE':"+who, item)
				provider.Conns.CloseConn(provider, connitem) // close conn on error
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
		for id, prov := range providerList {
			if prov.Group != provider.Group {
				if prov.capabilities.post || prov.capabilities.ihave {
					if item.missingOn[id] && !item.unwantedOn[id] {
						moreProvider = true
					}
				}
				continue
			}
			item.unwantedOn[id] = true
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
		item.mux.Lock()
		item.lines = []string{}
		item.mux.Unlock()
		memlim.MemReturn(who, item)
	} else {
		log.Printf("WARN ReupsRoutine NO clearmem seg.Id='%s'", item.segment.Id)
	}

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) ReupsRoutine quit '%s'", wid, provider.Name)
	}

	provider.Conns.ParkConn(provider, connitem)
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
