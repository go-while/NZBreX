package main

import (
	"fmt"
	//"github.com/Tensai75/cmpb"
	"log"
	"sync"
	"time"
)

func GoBootWorkers(waitDivider *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup, byteSize int64) {
	globalmux.Lock()
	if segmentChansCheck != nil {
		globalmux.Unlock()
		log.Print("Error in GoBootWorkers: already booted?!")
		return
	}
	segmentChansCheck = make(map[string]chan *segmentChanItem, len(providerList))
	segmentChansDowns = make(map[string]chan *segmentChanItem, len(providerList))
	segmentChansReups = make(map[string]chan *segmentChanItem, len(providerList))
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

	for _, provider := range providerList {
		//log.Printf("BootWorkers list=%d provider='%#v' ", len(providerList), provider)
		if !provider.Enabled || provider.MaxConns <= 0 {
			if cfg.opt.Verbose {
				log.Printf("!enabled provider: '%s' MaxConns=%d", provider.Name, provider.MaxConns)
			}
			continue
		}
		globalmux.Lock()
		workerWGconnEstablish.Add(1)
		globalmux.Unlock()
		go func(provider *Provider, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup) {
			defer workerWGconnEstablish.Done()
			//log.Printf("Boot Provider '%s'", provider.Name)
			if provider.Group == "" {
				provider.Group = provider.Name
			}
			//log.Printf("Mapping Provider '%s' to group '%s'", provider.Name, provider.Group)

			globalmux.Lock()
			if segmentChansCheck[provider.Group] == nil {
				// create channels once if not exists
				segmentChansCheck[provider.Group] = make(chan *segmentChanItem, len(segmentList))
				segmentChansDowns[provider.Group] = make(chan *segmentChanItem, len(segmentList))
				segmentChansReups[provider.Group] = make(chan *segmentChanItem, len(segmentList))
				segmentChanCheck := segmentChansCheck[provider.Group]
				// fill check channel for provider group with pointers

				for _, item := range segmentList {
					segmentChanCheck <- item
				}

			}
			globalmux.Unlock()

			//log.Printf("Connecting to Provider '%s' MaxConns=%d srvuri='%s'", provider.Name, provider.MaxConns, srvuri)
			if !cfg.opt.CheckOnly && !provider.NoUpload {

				// check of capabilities
				//srvtp, conn, err := connectBackend(0, srvuri, provider.SkipSslCheck)
				connitem, err := provider.Conns.GetConn(0, provider)
				if err != nil {
					log.Printf("ERROR Boot Provider '%s' err='%v'", provider.Name, err)
					return
				}
				if err := checkCapabilities(provider, connitem); err != nil {
					log.Printf("WARN Provider '%s' force NoUpload=true err=\"%v\" ", provider.Name, err)
					provider.NoUpload = true
				}
			}
			// fires up 1 go routine for every provider conn
			for wid := 1; wid <= provider.MaxConns; wid++ {
				if cfg.opt.Debug {
					log.Printf("Booting Provider '%s' wid=%d/%d", provider.Name, wid, provider.MaxConns)
				}
				globalmux.Lock()
				workerWGconnEstablish.Add(1)
				globalmux.Unlock()
				go GoWorker(wid, provider, waitWorker, workerWGconnEstablish)
				// give workers some space in time to start and connect
				// 50 conns will need 1.25s to boot
				time.Sleep(25 * time.Millisecond)
			}
			time.Sleep(time.Duration(provider.MaxConns) * 25 * time.Millisecond)
		}(provider, workerWGconnEstablish, waitWorker) // end go func
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

func GoWorker(wid int, provider *Provider, waitWorker *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup) {
	if cfg.opt.BUG {
		log.Printf("GoWorker (%d) launching routines '%s'", wid, provider.Name)
	}
	globalmux.Lock()
	waitWorker.Add(3)
	segCC := segmentChansCheck[provider.Group]
	segCD := segmentChansDowns[provider.Group]
	segCR := segmentChansReups[provider.Group]
	workerWGconnEstablish.Done()
	globalmux.Unlock()

	/* new worker code CheckRoutine */
	go func(wid int, provider *Provider, segmentChanCheck chan *segmentChanItem, waitWorker *sync.WaitGroup) {
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
				//log.Printf("WorkerCheck: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
				if err := GoCheckRoutine(wid, provider, item); err != nil { // re-queue
					log.Printf("ERROR in GoCheckRoutine err='%v'", err)
					//time.Sleep(5 * time.Second)
					//segmentChanCheck <-item // re-queue
				}
			}
			continue forGoCheckRoutine
		} // end forGoCheckRoutine
	}(wid, provider, segCC, waitWorker) // end go func()

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
				//log.Printf("WorkerDown: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
				if err := GoDownsRoutine(wid, provider, item); err != nil {
					log.Printf("ERROR in GoDownsRoutine err='%v'", err)
					//time.Sleep(5 * time.Second)
					//segmentChanDowns <-item // re-queue
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
				//log.Printf("WorkerReup: (%d) process seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
				if err := GoReupsRoutine(wid, provider, item); err != nil {
					log.Printf("ERROR in GoReupsRoutine err='%v'", err)
					//time.Sleep(5 * time.Second)
					//segmentChanReups <-item // re-queue
				}
			}
			continue forGoReupsRoutine
		} // end forGoReupsRoutine
	}(wid, provider, segCR, waitWorker) // end go func()

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) waitWorker.Wait processing Provider '%s'", wid, provider.Name)
	}
	waitWorker.Wait()

	if cfg.opt.Debug {
		log.Printf("GoWorker (%d) quit @ '%s'", wid, provider.Name)
	}

} // end func GoWorker

func pushDL(allowDl bool, item *segmentChanItem) (pushed bool) {
	if !allowDl {
		return
	}

	item.mux.Lock()
	matchThis := (len(item.lines) == 0 && !item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)

	if matchThis {
		for pid, _ := range item.availableOn {
			if cfg.opt.Debug {
				log.Printf(" | [DV] push chan <- down seg.Id='%s' @ '%s'", item.segment.Id, providerList[pid].Name)
			}
			item.flaginDL = true
			if cfg.opt.CheckFirst {
				// catch items and release the quaken later
				item.flaginDLMEM = true
			}
			item.mux.Unlock()

			Counter.incr("dlQueueCnt")
			Counter.incr("TOTAL_dlQueueCnt")

			/* push download request only to 1.
			 * this one should get it and update availableOn/missingOn list
			 */
			if cfg.opt.CheckFirst {
				memDL[providerList[pid].Group] <- item
			} else {
				segmentChansDowns[providerList[pid].Group] <- item
			}
			pushed = true
			return
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
		for pid, _ := range item.missingOn {
			//providerList[pid].mux.RLock()
			flagNoUp := (providerList[pid].NoUpload || (!providerList[pid].capabilities.ihave && !providerList[pid].capabilities.post))
			//providerList[pid].mux.RUnlock()
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
			item.flaginUP = true
			if cfg.opt.UploadLater && cacheON {
				// catch items and release the quaken later
				item.flaginDLMEM = true
			}
			item.mux.Unlock()

			Counter.incr("upQueueCnt")
			Counter.incr("TOTAL_upQueueCnt")

			/* push upload request only to 1.
			this one should up it and usenet should distribute it within minutes
			*/
			if cfg.opt.UploadLater && cacheON {
				memUP[providerList[pid].Group] <- item
			} else {
				segmentChansReups[providerList[pid].Group] <- item
			}
			pushed = true
			return
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
	closeWait := 4
	closeCase := ""
	todo := uint64(len(segmentList))
	providersCnt := len(providerList)
	var allowDl, allowUp bool

	var logstring, log00, log01, log02, log03, log04, log05, log06, log07, log08, log09, log10, log11, log99 string

	if cfg.opt.CheckFirst {
		memDL = make(map[string]chan *segmentChanItem, len(providerList))
		for _, provider := range providerList {
			memDL[provider.Group] = make(chan *segmentChanItem, len(segmentList))
		}
	} // end if cfg.opt.CheckFirst

	if cfg.opt.UploadLater {
		memUP = make(map[string]chan *segmentChanItem, len(providerList))
		for _, provider := range providerList {
			memUP[provider.Group] = make(chan *segmentChanItem, len(segmentList))
		}
	} // end if cfg.opt.UploadLater

	nextLogPrint := time.Now().Unix() + cfg.opt.LogPrintEvery
	var lastRunTook time.Duration
	var segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, noup, cached, inretry uint64
	// loops forever over the segmentList and checks if there is anything to do for an item
forever:
	for {
		time.Sleep(time.Duration((lastRunTook.Milliseconds() * 2)) + (2555 * time.Millisecond))
		globalmux.RLock()
		allowDl = (!cfg.opt.CheckOnly)
		allowUp = (!cfg.opt.CheckOnly && Counter.get("postProviders") > 0)
		globalmux.RUnlock()

		segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, noup, cached, inretry = 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0
		startLoop := time.Now()
	forsegmentList:
		for _, item := range segmentList {

			item.mux.RLock() // RLOCKS HERE #824d
			if item.cached {
				cached++
			}
			if item.checkedAt != providersCnt {
				// ignore item, will retry next run
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				continue forsegmentList
			}

			// capture statistics of this momemt
			checked++

			if item.flagisDL {
				isdl++
			}
			if item.flagisUP {
				isup++
				//done++
			}

			doContinue := false
			if item.flaginDL {
				indl++
				doContinue = true
			}
			if item.flaginUP {
				inup++
				doContinue = true
			}

			if len(item.availableOn) > 0 {
				segm++ // counts overall availability of segments
			}

			if len(item.availableOn) == 0 && len(item.missingOn) == providersCnt {
				// segment is not available on any provider...
				dead++
				doContinue = true
			}

			if len(item.missingOn) == 0 && len(item.availableOn) == providersCnt {
				// segment is on all providers or should be ... we add provider to to item.availableOn after re-upload
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
			inretry += nInRetry
			if allowDl && !pushedUp {
				pushDL(allowDl, item)
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

		upQ := Counter.get("upQueueCnt")
		TupQ := Counter.get("TOTAL_upQueueCnt")
		dlQ := Counter.get("dlQueueCnt")
		TdlQ := Counter.get("TOTAL_dlQueueCnt")
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

			used_slots, max_slots := memlim.Usage()

			//log00 = fmt.Sprintf(" TODO: %d ", todo)

			extended_log := true
			switch extended_log {
			case true:

				if (cfg.opt.CheckFirst || cfg.opt.CheckOnly) || (checked > 0 && checked != segm && checked != done) {
					log01 = fmt.Sprintf(" |  STAT:[%.1f%%] (%"+D+"d)", check_perc, checked)
				}
				if done > 0 {
					if !cfg.opt.Debug {
						log02 = fmt.Sprintf(" | DONE:[%.3f%%] (%"+D+"d/%d)", done_perc, done, todo)
					} else {
						if done_perc >= 99 && done_perc < 100 {
							log02 = fmt.Sprintf(" | DONE:[%.9f%%] (%"+D+"d)", done_perc, done)
						} else {
							log02 = fmt.Sprintf(" | DONE:[%.5f%%] (%"+D+"d)", done_perc, done)
						}
					}
				}
				if segm > 0 && segm != done {
					log03 = fmt.Sprintf(" | SEGM:[%.3f%%] (%"+D+"d)", segm_perc, segm)
				}
				if dead > 0 {
					log05 = fmt.Sprintf(" | DEAD:[%.3f%%] (%"+D+"d)", dead_perc, dead)
				}
				if dmca > 0 {
					log06 = fmt.Sprintf(" | DMCA:[%.3f%%] (%"+D+"d)", dmca_perc, dmca)
				}
				if inretry > 0 {
					log07 = fmt.Sprintf(" | ERRS:(%"+D+"d)", inretry)
				}
				if indl > 0 || isdl > 0 || dlQ > 0 {
					log08 = fmt.Sprintf(" | DL:[%.3f%%] (%"+D+"d / %"+D+"d Q:%d)", isdl_perc, isdl, TdlQ, dlQ)
				}
				if cached > 0 {
					log09 = fmt.Sprintf(" | HD:[%.3f%%] (%"+D+"d)", cache_perc, cached)
				}
				if inup > 0 || isup > 0 || upQ > 0 || TupQ > 0 {
					log10 = fmt.Sprintf(" | UP:[%.3f%%] (%"+D+"d / %"+D+"d Q:%d)", isup_perc, isup, TupQ, upQ)
				}
				if used_slots >= 0 {

					if cfg.opt.Verbose && !cfg.opt.Debug {

						log11 = fmt.Sprintf(" | MEM:%d/%d [C=%d|D=%d|U=%d]", used_slots, max_slots, CNTc, CNTd, CNTu)

					} else if cfg.opt.Debug {

						CNTmw, CNTmr, CNTwmr := Counter.get("TOTAL_MemCheckWait"), Counter.get("TOTAL_MemReturned"), Counter.get("WAIT_MemReturn")
						CNTmo := CNTmw - CNTmr

						CNTnc, CNTgc, CNTdc, CNTpc, CNTwc := Counter.get("TOTAL_NewConns"), Counter.get("TOTAL_GetConns"), Counter.get("TOTAL_DisConns"), Counter.get("TOTAL_ParkedConns"), Counter.get("WaitingGetConns")
						CNToc := CNTnc - CNTdc
						memdata := memlim.ViewData()
						log11 = fmt.Sprintf("\n memdata=%d:\n %#v \n  | MEM:%d/%d [C=%d|D=%d|U=%d] [CNTmemWait=%d|CNTmemRet=%d|MemOpen=%d|MemWaitReturn=%d] (indl=%d|inup=%d) CP:(parked=%d|get=%d|new=%d|dis=%d|open=%d|wait=%d)",
							len(memdata), memdata, used_slots, max_slots, CNTc, CNTd, CNTu, CNTmw, CNTmr, CNTmo, CNTwmr, indl, inup, CNTpc, CNTgc, CNTnc, CNTdc, CNToc, CNTwc)
					}
				}
			case false:

				if checked > 0 && checked != segm && checked != done {
					log01 = fmt.Sprintf("| STAT: [%.1f%%] (%"+D+"d/%"+D+"d)  ", check_perc, checked, todo)
				}

				if done > 0 {
					log02 = fmt.Sprintf("| DONE: [%.1f%%] |", done_perc)
				}
				if segm > 0 && segm != done {
					log03 = fmt.Sprintf("| SEGM: [%.1f%%] |", segm_perc)
				}
				if dead > 0 {
					log05 = fmt.Sprintf("| DEAD: [%.1f%%] |", dead_perc)
				}
				if dmca > 0 {
					log06 = fmt.Sprintf("| DMCA: [%.1f%%] |", dmca_perc)
				}
				if inretry > 0 {
					log07 = fmt.Sprintf("| inRETRY (%"+D+"d) |", inretry)
				}
				if isdl > 0 {
					log08 = fmt.Sprintf("| GETS: [%.1f%%] |", isdl_perc)
				}
				if cached > 0 {
					log09 = fmt.Sprintf("| DISK: [%.1f%%] |", cache_perc)
				}
				if isup > 0 {
					log10 = fmt.Sprintf("| REUP: [%.1f%%] |", isup_perc)
				}
			}

			logstring = fmt.Sprintf("%s%s%s%s%s%s%s%s%s%s%s%s%s", log00, log01, log02, log03, log04, log05, log06, log07, log08, log09, log10, log11, log99)
			if cfg.opt.Verbose && cfg.opt.LogPrintEvery >= 0 && logstring != "" && nextLogPrint < time.Now().Unix() {
				nextLogPrint = time.Now().Unix() + cfg.opt.LogPrintEvery
				log.Print(logstring)
			}
		} // end if !argsBar

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
		//if (checked != todo || (TupQ > 0 && TupQ != isup) || (TdlQ > 0 && TdlQ != isdl) || inup > 0 || indl > 0) {
		if checked != todo || inup > 0 || indl > 0 || inretry > 0 || dlQ > 0 || upQ > 0 {
			if cfg.opt.Debug {
				log.Printf("\n[DV] continue [ TupQ=%d !=? isup=%d || TdlQ=%d !=? isdl=%d || inup=%d > 0? || indl=%d > 0? || inretry=%d > 0? ]", TupQ, isup, TdlQ, isdl, inup, indl, inretry)
			}
			continue forever
		}

		if closeWait == 0 {
			if !cfg.opt.Verbose {
				log.Print(logstring)
			}
			break forever
		}

		// figure out if all jobs are done
		globalmux.RLock()
		closeCase0 := (done == todo)
		closeCase1 := (cfg.opt.CheckOnly)
		closeCase2 := (cacheON && (Counter.get("postProviders") == 0 && TupQ == 0 && cached == todo))
		closeCase3 := (cacheON && (dead+cached == todo && dead+isup == todo))
		closeCase4 := (isup == todo)
		closeCase5 := (dead+isup == todo)
		closeCase6 := (dead+done == todo)
		closeCase7 := false // (dead + isdl == todo)
		globalmux.RUnlock()

		if closeCase0 {
			closeWait--
			closeCase = closeCase + "|Debug#0"
			continue
		} else if closeCase1 {
			closeWait--
			closeCase = closeCase + "|Debug#1"
			continue
		} else if closeCase2 {
			closeWait--
			closeCase = closeCase + "|Debug#2"
			continue
		} else if closeCase3 {
			closeWait--
			closeCase = closeCase + "|Debug#3"
			continue
		} else if closeCase4 {
			closeWait--
			closeCase = closeCase + "|Debug#4"
			continue
		} else if closeCase5 {
			closeWait--
			closeCase = closeCase + "|Debug#5"
			continue
		} else if closeCase6 {
			closeWait--
			closeCase = closeCase + "|Debug#6"
			continue
		} else if closeCase7 {
			closeWait--
			closeCase = closeCase + "|Debug#7"
			continue
		} else {
			log.Printf("WARN closeCase reset...")
			closeWait++
			closeCase = "|reset"
		}
	} // end forever

	if logstring != "" { // always prints final logstring
		log.Print(logstring)
	}
	if cfg.opt.Debug {
		log.Printf("%s\n   WorkDivider quit: closeCase='%s'", logstring, closeCase)
	}
} // end func WorkDivider
