package main

import (
	"fmt"
	//"github.com/Tensai75/cmpb"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"
)

func GoBootWorkers(waitDivider *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup, byteSize int64) {
	globalmux.Lock()
	if segmentChansCheck != nil {
		globalmux.Unlock()
		log.Print("Error in GoBootWorkers: already booted?!")
		return
	}
	segmentChansCheck = make(map[string]chan *segmentChanItem)
	segmentChansDowns = make(map[string]chan *segmentChanItem)
	segmentChansReups = make(map[string]chan *segmentChanItem)
	waitWorker.Add(1)
	globalmux.Unlock()

	go GoSpeedMeter(byteSize, waitWorker)
	defer waitDivider.Done()

	if cacheON && cfg.opt.CheckCacheOnBoot {
		cached := 0
		for _, item := range segmentList {
			if cache.CheckCache(item) {
				cached++
				//log.Printf("Cached: seg.Id='%s'", item.segment.Id)
			}
		}
		log.Printf("Cached: %d/%d", cached, len(segmentList))
	}

	if cfg.opt.ChanSize > 0 {
		if len(segmentList) < cfg.opt.ChanSize {
			cfg.opt.ChanSize = len(segmentList)
		}
	} else {
		cfg.opt.ChanSize = DefaultChanSize
	}

	for _, provider := range providerList {
		//log.Printf("BootWorkers list=%d provider='%#v' ", providersCnt, provider)
		if !provider.Enabled || provider.MaxConns <= 0 {
			if cfg.opt.Verbose {
				log.Printf("!enabled provider: '%s' MaxConns=%d", provider.Name, provider.MaxConns)
			}
			continue
		}
		globalmux.Lock()
		workerWGconnEstablish.Add(1)
		globalmux.Unlock()
		go func(provider *Provider, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup) {
			defer workerWGconnEstablish.Done()
			//log.Printf("Boot Provider '%s'", provider.Name)
			if provider.Group == "" {
				provider.Group = provider.Name
			}
			//log.Printf("Mapping Provider '%s' to group '%s'", provider.Name, provider.Group)

			globalmux.Lock()
			if segmentChansCheck[provider.Group] == nil {
				// create channels once if not exists
				segmentChansCheck[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				segmentChansDowns[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				segmentChansReups[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				// fill check channel for provider group with pointers
				go func(segmentChanCheck chan *segmentChanItem) {
					start := time.Now()
					for _, item := range segmentList {
						segmentChanCheck <- item
					}
					for {
						time.Sleep(time.Second) // wait for check routine to empty out the chan
						if len(segmentChanCheck) == 0 {
							break
						}
					}
					if cfg.opt.Verbose {
						log.Printf(" | Done feeding items=%d -> segmentChanCheck Group '%s' took='%.0f sec'", len(segmentList), provider.Group, time.Since(start).Seconds())
					}
				}(segmentChansCheck[provider.Group])

			}
			globalmux.Unlock()

			//log.Printf("Connecting to Provider '%s' MaxConns=%d srvuri='%s'", provider.Name, provider.MaxConns, srvuri)
			if !cfg.opt.CheckOnly && !provider.NoUpload {

				// check of capabilities
				//srvtp, conn, err := connectBackend(0, srvuri, provider.SkipSslCheck)
				connitem, err := provider.Conns.GetConn()
				if err != nil {
					log.Printf("ERROR Boot Provider '%s' err='%v'", provider.Name, err)
					return
				}
				if err := checkCapabilities(provider, connitem); err != nil {
					log.Printf("WARN Provider '%s' force NoUpload=true err=\"%v\" ", provider.Name, err)
					provider.NoUpload = true
				}
			}
			if cfg.opt.Verbose {
				provider.mux.RLock()
				//log.Printf("Capabilities: [ IHAVE: %s | POST: %s | CHECK: %s | STREAM: %s ] @ '%s' NoDl=%t NoUp=%t MaxConns=%d",
				log.Printf("Capabilities: | IHAVE: %s | POST: %s | NoDL: %s | NoUP: %s | MaxC: %3d | @ '%s'",
					yesno(provider.capabilities.ihave),
					yesno(provider.capabilities.post),
					//yesno(provider.capabilities.check),
					//yesno(provider.capabilities.stream),
					yesno(provider.NoDownload),
					yesno(provider.NoUpload),
					provider.MaxConns,
					provider.Name)
				provider.mux.RUnlock()
			}
			// fires up 1 go routine for every provider conn
			for wid := 1; wid <= provider.MaxConns; wid++ {
				if cfg.opt.Debug {
					log.Printf("Booting Provider '%s' wid=%d/%d", provider.Name, wid, provider.MaxConns)
				}
				globalmux.Lock()
				workerWGconnEstablish.Add(1)
				globalmux.Unlock()
				// GoWorker connecting....
				go GoWorker(wid, provider, waitWorker, workerWGconnEstablish, waitPool)
				// all providers boot up at the same time
				// give workers some space in time to start and connect
				// 50 conns on a provider will need up to 2.5s to boot
				time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond)
			}
		}(provider, workerWGconnEstablish, waitWorker, waitPool) // end go func
	} // end for providerList
	if cfg.opt.Debug {
		log.Printf("Waiting for Workers to connect")
	}
	workerWGconnEstablish.Done() // releases 1 set before calling GoBootWorkers
	workerWGconnEstablish.Wait() // waits for the others to release when connections are established
	// check if we have at least one provider with IHAVE or POST capability
	if Counter.get("postProviders") == 0 && !cfg.opt.CheckOnly {
		cfg.opt.CheckOnly = true
		log.Print("WARN: no provider has IHAVE or POST capability: force CheckOnly")
	}
	if cfg.opt.Debug {
		log.Printf("OK all Workers connected")
	}
} // end func GoBootWorkers

func GoWorker(wid int, provider *Provider, waitWorker *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup, waitPool *sync.WaitGroup) {
	if cfg.opt.BUG {
		log.Printf("GoWorker (%d) launching routines '%s'", wid, provider.Name)
	}
	globalmux.Lock()
	waitWorker.Add(3)
	waitPool.Add(1)

	segCC := segmentChansCheck[provider.Group]
	segCD := segmentChansDowns[provider.Group]
	segCR := segmentChansReups[provider.Group]
	workerWGconnEstablish.Done()
	globalmux.Unlock()

	// gets a conn once for a worker and shares conn in check, down, reup routines
	// without parking it back to the pool as long as workers are running
	sharedConn := make(chan *ConnItem, 1)
	connitem, err := provider.Conns.GetConn()
	if err != nil {
		log.Printf("ERROR a GoWorker (%d) failed to connect '%s' err='%v'", wid, provider.Name, err)
		return
	}
	sharedConn <- connitem // puts connitem into sharedConn channel

	/* new worker code CheckRoutine */
	go func(wid int, provider *Provider, segmentChanCheck chan *segmentChanItem, segmentChanDown chan *segmentChanItem, waitWorker *sync.WaitGroup) {
		defer waitWorker.Done()
	forGoCheckRoutine:
		for {
			select {
			case item := <-segmentChanCheck:
				if item == nil {
					if cfg.opt.Debug {
						log.Print("CheckRoutine received a nil pointer to quit")
					}
					segmentChanCheck <- nil // refill the nil so others will die too
					break forGoCheckRoutine
				}
				item.mux.Lock() // we might get an item still locked, so we lock too and wait for upper layer to release first.
				item.mux.Unlock()

				switch cfg.opt.ByPassSTAT {
				case false:
					if cfg.opt.Debug {
						log.Printf("WorkerCheck: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
					}
					if err := GoCheckRoutine(wid, provider, item, sharedConn); err != nil { // re-queue?
						log.Printf("ERROR in GoCheckRoutine err='%v'", err)
					}
				case true:
					item.mux.Lock()
					item.flaginDL = true
					item.mux.Unlock()
					Counter.incr("dlQueueCnt")       // cfg.opt.ByPassSTAT
					Counter.incr("TOTAL_dlQueueCnt") //cfg.opt.ByPassSTAT
					segmentChanDown <- item
				}
			}
			continue forGoCheckRoutine
		} // end forGoCheckRoutine
	}(wid, provider, segCC, segCD, waitWorker) // end go func()

	/* new worker code DownsRoutine */
	go func(wid int, provider *Provider, segmentChanDowns chan *segmentChanItem, waitWorker *sync.WaitGroup) {
		defer waitWorker.Done()
	forGoDownsRoutine:
		for {
			select {
			case item := <-segmentChanDowns:
				if item == nil {
					if cfg.opt.Debug {
						log.Print("DownsRoutine received a nil pointer to quit")
					}
					segmentChanDowns <- nil // refill the nil so others will die too
					break forGoDownsRoutine
				}
				item.mux.Lock() // we might get an item still locked, so we lock too and wait for upper layer to release first.
				item.mux.Unlock()
				if cfg.opt.Debug {
					log.Printf("WorkerDown: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
				}
				if err := GoDownsRoutine(wid, provider, item, sharedConn); err != nil {
					log.Printf("ERROR in GoDownsRoutine err='%v'", err)
				}
			}
			continue forGoDownsRoutine
		} // end forGoDownsRoutine
	}(wid, provider, segCD, waitWorker) // end go func()

	/* new worker code ReupsRoutine */
	go func(wid int, provider *Provider, segmentChanReups chan *segmentChanItem, waitWorker *sync.WaitGroup) {
		defer waitWorker.Done()
	forGoReupsRoutine:
		for {
			select {
			case item := <-segmentChanReups:
				if item == nil {
					if cfg.opt.Debug {
						log.Print("ReupsRoutine received a nil pointer to quit")
					}
					segmentChanReups <- nil // refill the nil so others will die too
					break forGoReupsRoutine
				}
				item.mux.Lock() // we might get an item still locked, so we lock too and wait for upper layer to release first.
				item.mux.Unlock()
				if cfg.opt.Debug {
					log.Printf("WorkerReup: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
				}
				if err := GoReupsRoutine(wid, provider, item, sharedConn); err != nil {
					log.Printf("ERROR in GoReupsRoutine err='%v'", err)
				}
			}
			continue forGoReupsRoutine
		} // end forGoReupsRoutine
	}(wid, provider, segCR, waitWorker) // end go func()

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) waitWorker.Wait processing Provider '%s'", wid, provider.Name)
	}
	waitWorker.Wait()

	select {
	case connitem := <-sharedConn:
		if connitem != nil {
			//log.Printf("GoWorker (%d) parked sharedConn @ '%s'", wid, provider.Name)
			provider.Conns.ParkConn(connitem)
		}
	default:
		// no conn there?
		//KillConnPool(provider *Provider)
		log.Printf("GoWorker (%d) no sharedConn?! @ '%s'", wid, provider.Name)
	}

	waitPool.Done()
	KillConnPool(provider)
	waitPool.Wait()

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) quit @ '%s'", wid, provider.Name)
	}

} // end func GoWorker

func pushDL(allowDl bool, item *segmentChanItem) (pushed bool, nodl uint64) {
	if !allowDl {
		return
	}

	item.mux.Lock()
	matchThis := (len(item.lines) == 0 && !item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)

	if matchThis {
	providerDl:
		for pid, avail := range item.availableOn {
			if !avail {
				continue providerDl
			}
			if item.ignoreDlOn[pid] {
				if cfg.opt.Debug {
					log.Printf(" | [DV] ignoreDlOn seg.Id='%s' @ '%s'", item.segment.Id, providerList[pid].Name)
				}
				continue providerDl
			}
			if providerList[pid].NoDownload {
				nodl++
				item.ignoreDlOn[pid] = true
				continue providerDl
			}
			if cfg.opt.Debug {
				log.Printf(" | [DV] push chan <- down seg.Id='%s' @ '%s'", item.segment.Id, providerList[pid].Name)
			}
			/* push download request only to 1.
			 * this one should get it and update availableOn/missingOn list
			 */
			if cfg.opt.CheckFirst {
				select {
				case memDL[providerList[pid].Group] <- item:
					pushed = true
				default:
					// chan is full
				}
			} else if !cfg.opt.ByPassSTAT {
				select {
				case segmentChansDowns[providerList[pid].Group] <- item:
					pushed = true
				default:
					// chan is full
				}
			}
			if pushed {
				item.flaginDL = true
				if cfg.opt.CheckFirst {
					// catch items and release the quaken later
					item.flaginDLMEM = true
				}
			}
			item.mux.Unlock()
			if pushed {
				Counter.incr("dlQueueCnt")
				Counter.incr("TOTAL_dlQueueCnt")
			}
			return // return after 1st push!
		} // end for providerDl
	} // end if allowDl
	item.mux.Unlock()
	return
} // end func pushDL

func pushUP(allowUp bool, item *segmentChanItem) (pushed bool, noup uint64, inretry uint64) {
	if !allowUp {
		return
	}

	item.mux.Lock()
	matchThis := (len(item.lines) > 0 && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)

	if matchThis {
		// looks like we have fetched the segment and did not upload the item yet
	providerUp:
		for pid, miss := range item.missingOn {
			if !miss {
				continue providerUp
			}
			providerList[pid].mux.RLock() // FIXME TODO #b8bd287b: dynamic capas
			flagNoUp := (providerList[pid].NoUpload || (!providerList[pid].capabilities.ihave && !providerList[pid].capabilities.post))
			providerList[pid].mux.RUnlock()

			if flagNoUp {
				noup++
				continue providerUp
			}
			if item.unwantedOn[pid] {
				noup++
				continue providerUp
			}
			if item.retryOn[pid] {
				if item.retryIn > time.Now().Unix() {
					inretry++
					continue providerUp
				} else {
					delete(item.retryOn, pid)
				}
			}
			if cfg.opt.Debug {
				log.Printf(" | [DV] push chan <- reup seg.Id='%s' @ '%s'", item.segment.Id, providerList[pid].Name)
			}

			/* push upload request only to 1.
			this one should up it and usenet should distribute it within minutes
			*/
			if cfg.opt.UploadLater && cacheON {
				select {
				case memUP[providerList[pid].Group] <- item:
					// pass
					pushed = true
				default:
					// chan is full
				}
			} else {
				select {
				case segmentChansReups[providerList[pid].Group] <- item:
					// pass
					pushed = true
				default:
					// chan is full
				}
			}
			if pushed {
				item.flaginUP = true
				if cfg.opt.UploadLater && cacheON {
					// catch items and release the quaken later
					item.flaginDLMEM = true
				}
			}
			item.mux.Unlock()
			if pushed {
				Counter.incr("upQueueCnt")
				Counter.incr("TOTAL_upQueueCnt")
			}
			return // return after 1st push!
		} // end for providerUp
	}
	item.mux.Unlock()
	return
} // end func pushUP

func GoWorkDivider(waitDivider *sync.WaitGroup, waitDividerDone *sync.WaitGroup) {
	if cfg.opt.Debug {
		log.Print("go GoWorkDivider() waitDivider.Wait()")
	}
	waitDivider.Wait()
	defer waitDividerDone.Done()

	if cfg.opt.Debug {
		log.Print("go GoWorkDivider() Starting!")
	}

	segcheckdone := false
	closeWait, closeCase := 1, ""
	todo := uint64(len(segmentList))
	providersCnt := len(providerList)

	if cfg.opt.CheckFirst {
		memDL = make(map[string]chan *segmentChanItem)
		for _, provider := range providerList {
			if memDL[provider.Group] == nil {
				memDL[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
			}
		}
	} // end if cfg.opt.CheckFirst

	if cfg.opt.UploadLater {
		memUP = make(map[string]chan *segmentChanItem)
		for _, provider := range providerList {
			if memUP[provider.Group] == nil {
				memUP[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
			}
		}
	} // end if cfg.opt.UploadLater

	// some values we count on in every loop to check how far processing
	nextLogPrint := time.Now().Unix() + cfg.opt.PrintStats
	var lastRunTook time.Duration

	// strings
	var logstring, log00, log01, log02, log03, log04, log05, log06, log07, log08, log09, log10, log11, log99 string

	// loops forever over the segmentList and checks if there is anything to do for an item
forever:
	for {
		time.Sleep(time.Duration((lastRunTook.Milliseconds() * 2)) + (2555 * time.Millisecond))
		globalmux.RLock()
		allowDl := (!cfg.opt.CheckOnly)
		allowUp := (!cfg.opt.CheckOnly && Counter.get("postProviders") > 0)
		globalmux.RUnlock()

		// uint64
		var segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ uint64

		// Tnodl, Tnoup counts segments that not have been downloaded
		// or uploaded because provider has config flat NoDownload or NoUpload set
		var Tnodl, Tnoup uint64

		startLoop := time.Now()

	forsegmentList:
		for _, item := range segmentList {

			item.mux.RLock() // RLOCKS HERE #824d
			if item.cached {
				cached++
			}

			if /*!cfg.opt.ByPassSTAT &&*/ item.checkedAt != providersCnt {
				// ignore item, will retry next run
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				continue forsegmentList
			}
			if !cfg.opt.ByPassSTAT {
				checked++
			} else {
				checked += uint64(item.checkedAt)
			}

			// capture statistics of this momemt
			if item.flagisDL {
				isdl++
			}
			if item.flagisYenc {
				isyenc++
			}
			if item.flagisUP {
				isup++
			}

			doContinue := false
			if item.flaginDL {
				indl++
				doContinue = true
			}
			if item.flaginYenc {
				inyenc++
				doContinue = true
			}
			if item.flaginUP {
				inup++
				doContinue = true
			}

			if len(item.availableOn) > 0 {
				segm++ // counts overall availability of segments
			}

			//if len(item.availableOn) == 0 && len(item.missingOn) == providersCnt {
			if IsSegmentStupid(item) { // check me bug? NoDownload-flag !
				// segment is not available to download on any provider...
				dead++
				doContinue = true
			}

			if len(item.missingOn) == 0 && len(item.availableOn) == providersCnt {
				// segment is on all providers or should be ... we add provider to item.availableOn after re-upload
				allOk++
				done++
				doContinue = true
			} else {
				// segment is missing on providers
			}

			if doContinue {
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				continue forsegmentList
			}
			item.mux.RUnlock() // RUNLOCKS HERE #824d

			pushedUp, nNoUp, nInRetry := pushUP(allowUp, item)
			noup += nNoUp
			//Tnoup += len(item.ignoreDlOn)
			inretry += nInRetry
			if allowDl && !pushedUp {
				pushedDl, nNoDl := pushDL(allowDl, item)
				nodl += nNoDl
				Tnodl += uint64(len(item.ignoreDlOn))
				if pushedDl {
					indl++
				}
			}

		} // end for forsegmentList

		lastRunTook = time.Since(startLoop)

		if cfg.opt.Debug {
			log.Printf(" | [DV] lastRunTook='%d ms' '%v", lastRunTook.Milliseconds(), lastRunTook)
		}

		if !cfg.opt.CheckOnly && cfg.opt.CheckFirst && segcheckdone && checked == todo {
			//log.Printf("release the DL quaken")
			for _, provider := range providerList {
				dlq := len(memDL[provider.Group])
				if dlq > 0 {
					if cfg.opt.Verbose {
						log.Printf(" | [DV] | Feeding %d Downs to '%s'", dlq, provider.Group)
					}
				feedDL:
					for {
						select {
						case item := <-memDL[provider.Group]:
							segmentChansDowns[provider.Group] <- item
						default:
							// chan ran empty
							break feedDL
						}
					}
					if cfg.opt.Verbose {
						log.Printf(" | [DV] | Done feeding %d Downs to '%s'", dlq, provider.Group)
					}
				} // end if dlq
			}
		} // end if argCheckFirst

		if cacheON && !cfg.opt.CheckOnly && cfg.opt.UploadLater && ((isdl == todo) || (cached == todo)) {
			//log.Printf("release the UL quaken")
			for _, provider := range providerList {
				upq := len(memUP[provider.Group])
				if upq > 0 {
					if cfg.opt.Verbose {
						log.Printf(" | [DV] | Feeding %d Reups to '%s'", upq, provider.Group)
					}
				feedUP:
					for {
						select {
						case item := <-memUP[provider.Group]:
							segmentChansReups[provider.Group] <- item
						default:
							// chan ran empty
							break feedUP
						}
					}
				} // end if upq
			}
		} // end if argUploadLater

		upQ = Counter.get("upQueueCnt")
		TupQ := Counter.get("TOTAL_upQueueCnt")
		dlQ = Counter.get("dlQueueCnt")
		TdlQ := Counter.get("TOTAL_dlQueueCnt")
		yeQ = Counter.get("yencQueueCnt")
		TyeQ := Counter.get("TOTAL_yencQueueCnt")
		// print some stats and check if we're done
		if !cfg.opt.Bar && (cfg.opt.Verbose || cfg.opt.Debug) {
			CNTc, CNTd, CNTu := Counter.get("GoCheckRoutines"), Counter.get("GoDownsRoutines"), Counter.get("GoReupsRoutines")
			cache_perc := float64(cached) / float64(todo) * 100
			check_perc := float64(checked) / float64(todo) * 100
			segm_perc := float64(segm) / float64(todo) * 100
			done_perc := float64(done) / float64(todo) * 100
			dead_perc := float64(dead) / float64(todo) * 100
			isdl_perc := float64(isdl) / float64(todo) * 100
			isup_perc := float64(isup) / float64(todo) * 100
			dmca_perc := float64(dmca) / float64(todo) * 100
			yenc_perc := float64(isyenc) / float64(todo) * 100

			used_slots, max_slots := memlim.Usage()

			//log00 = fmt.Sprintf(" TODO: %d ", todo)

			if (cfg.opt.CheckFirst || cfg.opt.CheckOnly) || (checked > 0 && checked != segm && checked != done) {
				if check_perc != 100 {
					log01 = fmt.Sprintf(" | STAT:[%03.1f%%] (%"+D+"d)", check_perc, checked)
				} else {
					log01 = fmt.Sprintf(" | STAT:[done] (%"+D+"d)", checked)
				}
			}
			if done > 0 {
				if !cfg.opt.Debug {
					log02 = fmt.Sprintf(" | DONE:[%03.3f%%] (%"+D+"d/%d)", done_perc, done, todo)
				} else {
					if done_perc >= 99 && done_perc < 100 {
						log02 = fmt.Sprintf(" | DONE:[%03.9f%%] (%"+D+"d)", done_perc, done)
					} else {
						log02 = fmt.Sprintf(" | DONE:[%03.5f%%] (%"+D+"d)", done_perc, done)
					}
				}
			}
			if segm > 0 && segm != done {
				log03 = fmt.Sprintf(" | SEGM:[%03.3f%%] (%"+D+"d)", segm_perc, segm)
				if nodl > 0 {
					log03 = log03 + fmt.Sprintf(" nodl=%d/%d", nodl, Tnodl)
				}
				if noup > 0 {
					log03 = log03 + fmt.Sprintf(" noup=%d/%d", noup, Tnoup)
				}
			}
			if yenc_perc > 0 && yenc_perc != cache_perc {
				log04 = fmt.Sprintf(" | YENC:[%03.3f%%] (%"+D+"d / %"+D+"d Q:%d←%d)", yenc_perc, isyenc, TyeQ, inyenc, yeQ)
			}
			if dead > 0 {
				log05 = fmt.Sprintf(" | DEAD:[%03.3f%%] (%"+D+"d)", dead_perc, dead)
			}
			if dmca > 0 {
				log06 = fmt.Sprintf(" | DMCA:[%03.3f%%] (%"+D+"d)", dmca_perc, dmca)
			}
			if inretry > 0 {
				log07 = fmt.Sprintf(" | ERRS:(%"+D+"d)", inretry)
			}
			if indl > 0 || isdl > 0 || dlQ > 0 || TdlQ > 0 {
				log08 = fmt.Sprintf(" | DL:[%03.3f%%] (%"+D+"d / %"+D+"d  Q:%d=%d)", isdl_perc, isdl, TdlQ, dlQ, indl)
			}
			if cached > 0 {
				log09 = fmt.Sprintf(" | HD:[%03.3f%%] (%"+D+"d)", cache_perc, cached)
			}
			if inup > 0 || isup > 0 || upQ > 0 || TupQ > 0 {
				log10 = fmt.Sprintf(" | UP:[%03.3f%%] (%"+D+"d / %"+D+"d  Q:%d←%d)", isup_perc, isup, TupQ, upQ, inup)
			}

			if cfg.opt.Verbose && !cfg.opt.Debug {
				openConns, idleConns := 0, 0
				for _, prov := range providerList {
					oc, ic := prov.Conns.GetStats()
					openConns += oc
					idleConns += ic
				}
				log11 = fmt.Sprintf(" | MEM:%d/%d [Cr=%d|Dr=%d|Ur=%d] [oC:%d iC=%d) ", used_slots, max_slots, CNTc, CNTd, CNTu, openConns, idleConns)

			} else if cfg.opt.Debug {

				CNTmw, CNTmr, CNTwmr := Counter.get("TOTAL_MemCheckWait"), Counter.get("TOTAL_MemReturned"), Counter.get("WAIT_MemReturn")
				CNTmo := CNTmw - CNTmr

				CNTnc, CNTgc, CNTdc, CNTpc, CNTwc := Counter.get("TOTAL_NewConns"), Counter.get("TOTAL_GetConns"), Counter.get("TOTAL_DisConns"), Counter.get("TOTAL_ParkedConns"), Counter.get("WaitingGetConns")
				CNToc := CNTnc - CNTdc
				memdata := memlim.ViewData()
				log11 = fmt.Sprintf("\n memdata=%d:\n %#v \n  | MEM:%d/%d [Cr=%d|Dr=%d|Ur=%d] [CNTmemWait=%d|CNTmemRet=%d|MemOpen=%d|MemWaitReturn=%d] (indl=%d|inup=%d) CP:(parked=%d|get=%d|new=%d|dis=%d|open=%d|wait=%d)",
					len(memdata), memdata, used_slots, max_slots, CNTc, CNTd, CNTu, CNTmw, CNTmr, CNTmo, CNTwmr, indl, inup, CNTpc, CNTgc, CNTnc, CNTdc, CNToc, CNTwc)
			}

			// build stats log string ...
			logstring = log00 + log01 + log02 + log03 + log04 + log05 + log06 + log07 + log08 + log09 + log10 + log11 + log99
			if cfg.opt.Verbose && cfg.opt.PrintStats >= 0 && logstring != "" && nextLogPrint < time.Now().Unix() {
				nextLogPrint = time.Now().Unix() + cfg.opt.PrintStats
				log.Print(logstring)
			}
		} // print some stats

		if !segcheckdone && checked == todo {
			setTimerNow(&segmentCheckEndTime)
			took := getTimeSince(segmentCheckStartTime)
			globalmux.Lock()
			segmentCheckTook = took
			globalmux.Unlock()
			segcheckdone = true
			/*
				if cfg.opt.Bar {
					segmentBar.SetMessage("done") // segmentBar
				}
			*/
			if cfg.opt.Verbose {
				log.Printf(" | [DV] | Segment Check Done: took='%.1f sec'", took.Seconds())
			}
		}

		/*
			if cfg.opt.Bar && !cfg.opt.CheckOnly && segcheckdone && TdlQ > 0 {
				//log.Printf("dlBarStarted? TdlQ=%d", TdlQ)

				BarMutex.Lock()
				if !dlBarStarted {
					dlBarStarted = true
					dlBar = progressBars.NewBar("DOWN", int(TdlQ))
					dlBar.SetPreBar(cmpb.CalcSteps)
					dlBar.SetPostBar(cmpb.CalcTime)
				} else {
					dlBar.UpdateTotal(int(TdlQ))
				}
				BarMutex.Unlock()
				//segmentBar.SetMessage("done: start DL now")
			}

			if cfg.opt.Bar && !cfg.opt.CheckOnly && segcheckdone && TupQ > 0 {
				//log.Printf("upBarStarted? TupQ=%d", TupQ)

				BarMutex.Lock()
				if !upBarStarted {
					upBarStarted = true
					upBar = progressBars.NewBar("REUP", int(TupQ))
					upBar.SetPreBar(cmpb.CalcSteps)
					upBar.SetPostBar(cmpb.CalcTime)
				} else {
					upBar.UpdateTotal(int(TupQ))
				}
				BarMutex.Unlock()
				//segmentBar.SetMessage("done: start UP now")
			}
		*/

		// continue as long as any of this triggers because stuff is still in queues and processing
		if checked != todo || inup > 0 || indl > 0 || inretry > 0 || inyenc > 0 || dlQ > 0 || upQ > 0 || yeQ > 0 {
			if cfg.opt.Debug {
				log.Printf("\n[DV] continue [ TupQ=%d !=? isup=%d || TdlQ=%d !=? isdl=%d || inup=%d > 0? || indl=%d > 0? || inretry=%d > 0? ]", TupQ, isup, TdlQ, isdl, inup, indl, inretry)
			}
			continue forever
		}

		if closeWait <= 0 {
			if cfg.opt.Debug {
				if closeCase != "" {
					log.Printf(" | [DV] closeCase='%s'", closeCase)
				}
				log.Printf(" | [DV] quit all 0? inup=%d indl=%d inretry=%d inyenc=%d dlQ=%d upQ=%d yeQ=%d", inup, indl, inretry, inyenc, dlQ, upQ, yeQ)
			}
			break forever
		}

		// figure out if all jobs are done
		globalmux.RLock()
		closeCase0 := (done == todo)
		closeCase1 := (cfg.opt.CheckOnly)
		closeCase2 := (cacheON && (Counter.get("postProviders") == 0 && cached == todo))
		closeCase3 := (cacheON && (dead+cached == todo && dead+isup == todo))
		closeCase4 := (isup == todo)
		closeCase5 := (dead+isup == todo)
		closeCase6 := (dead+done == todo)
		closeCase7 := false // placeholder
		globalmux.RUnlock()

		if closeCase0 {
			closeWait--
			closeCase = closeCase + "|Debug#0@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase1 {
			closeWait--
			closeCase = closeCase + "|Debug#1@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase2 {
			closeWait--
			closeCase = closeCase + "|Debug#2@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase3 {
			closeWait--
			closeCase = closeCase + "|Debug#3@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase4 {
			closeWait--
			closeCase = closeCase + "|Debug#4@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase5 {
			closeWait--
			closeCase = closeCase + "|Debug#5@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase6 {
			closeWait--
			closeCase = closeCase + "|Debug#6@" + fmt.Sprintf("%d", time.Now().Unix())

		} else if closeCase7 {
			closeWait--
			closeCase = closeCase + "|Debug#7@" + fmt.Sprintf("%d", time.Now().Unix())

		} else {
			log.Printf("WARN [DV] hit impossible closeCase ... kill in %d more loops", 15-closeWait)
			closeWait++
			if closeWait >= 15 {
				log.Print("... force quit ...")
				os.Exit(1)
			}
			closeCase = ""
		}
		if closeCase != "" {
			log.Printf(" | DV closeCase='%s' closeWait=%d", closeCase, closeWait)
		}
	} // end forever

	/*
	if !cfg.opt.Bar && logstring != "" { // always prints final logstring
		log.Print("Final: "+logstring)
	}
	*/
	if cfg.opt.Debug {
		log.Printf("%s\n   WorkDivider quit: closeCase='%s'", logstring, closeCase)
	}
} // end func WorkDivider

func SharedConnGet(sharedCC chan *ConnItem) (connitem *ConnItem) {
	return <-sharedCC
} // end func SharedConnGet

func SharedConnReturn(sharedCC chan *ConnItem, connitem *ConnItem) {
	sharedCC <- connitem
} // end func SharedConnReturn
