package main

import (
	"fmt"
	//"github.com/Tensai75/cmpb"
	"log"
	"os"
	"sync"
	"time"
)

// GoBootWorkers boots up all workers for a session
func (s *SESSION) GoBootWorkers(waitDivider *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup, byteSize int64) {
	globalmux.Lock()
	if s == nil {
		dlog(always, "ERROR in GoBootWorkers: session is nil... preBoot=%t ", s.preBoot)
		globalmux.Unlock()
		return
	} else if s.active || !s.preBoot {
		dlog(always, "ERROR in GoBootWorkers: session %d already booted... s.preBoot=%t s.active=%t", s.sessId, s.preBoot, s.active)
		globalmux.Unlock()
		return
	}
	s.preBoot = false
	s.active = true
	waitWorker.Add(1)
	globalmux.Unlock()

	go GoSpeedMeter(byteSize, waitWorker)
	defer waitDivider.Done()

	if cacheON && cfg.opt.CheckCacheOnBoot {
		cached := 0
		for _, item := range s.segmentList {
			if cache.CheckCache(item) {
				cached++
				//dlog( "Cached: seg.Id='%s'", item.segment.Id)
			}
		}
		dlog(always, "Cached: %d/%d", cached, len(s.segmentList))
	}

	if cfg.opt.ChanSize > 0 {
		if len(s.segmentList) < cfg.opt.ChanSize {
			cfg.opt.ChanSize = len(s.segmentList)
		}
	} else {
		cfg.opt.ChanSize = len(s.segmentList)
		//cfg.opt.ChanSize = DefaultChanSize
	}

	// loop over the providerList and boot up anonymous workers for each provider
	for _, provider := range s.providerList {

		dlog(cfg.opt.DebugWorker, "BootWorkers list=%d check provider='%#v' ", len(s.providerList), provider.Name)

		if !provider.Enabled || provider.MaxConns <= 0 {
			dlog(cfg.opt.Verbose, "ignored provider: '%s'", provider.Name)
			continue
		}
		globalmux.Lock()
		workerWGconnEstablish.Add(1)
		globalmux.Unlock()
		// boot up a worker for this provider in an anonymous go routine
		go func(provider *Provider, workerWGconnEstablish *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup) {
			defer workerWGconnEstablish.Done()

			if provider.Group == "" {
				provider.Group = provider.Name
			}

			dlog(cfg.opt.Verbose, "Worker  Boot Provider '%s' group '%s'", provider.Name, provider.Group)

			// create once if not exists
			//globalmux.Lock()
			s.mux.Lock()
			if s.segmentChansCheck == nil {
				dlog(cfg.opt.Debug, "Creating chans for group '%s' cfg.opt.ChanSize=%d", provider.Group, cfg.opt.ChanSize)
				if cfg.opt.Debug {
					time.Sleep(2 * time.Second) // sleeps 2 sec to print a debug line about creating channels for group
				}
				// create maps which holds the channels for every provider group
				s.segmentChansCheck = make(map[string]chan *segmentChanItem)
				s.segmentChansDowns = make(map[string]chan *segmentChanItem)
				s.segmentChansReups = make(map[string]chan *segmentChanItem)
			}
			if s.segmentChansCheck[provider.Group] == nil {
				// create channels once if not exists
				// since we run concurrently only the first worker hitting the lock will create the channels
				s.segmentChansCheck[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				s.segmentChansDowns[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				s.segmentChansReups[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
				// fill check channel for provider group with pointers

				go func(segmentChanCheck chan *segmentChanItem) {

					start := time.Now()
					for _, item := range s.segmentList {
						segmentChanCheck <- item
					}
					for {
						time.Sleep(time.Second) // wait for check routine to empty out the chan
						if len(segmentChanCheck) == 0 {
							break
						}
					}
					dlog(cfg.opt.Verbose, " | Done feeding items=%d -> segmentChanCheck Group '%s' took='%.0f sec'", len(s.segmentList), provider.Group, time.Since(start).Seconds())
					s.mux.Lock()
					s.checkDone = true // set checkDone to true so we can release the quaken to the downs routines
					s.mux.Unlock()
				}(s.segmentChansCheck[provider.Group])
			}
			s.mux.Unlock()
			//globalmux.Unlock()

			dlog(cfg.opt.Verbose, "Connecting to Provider '%s' MaxConns=%d SSL=%t", provider.Name, provider.MaxConns, provider.SSL)

			if !cfg.opt.CheckOnly && !provider.NoUpload {
				dlog(cfg.opt.Debug, "GoBootWorkers: get conn to check capabilities for provider '%s'", provider.Name)

				// get a connection item from the provider's connection pool
				connitem, err := provider.ConnPool.GetConn()
				if err != nil {
					dlog(always, "ERROR Boot Provider '%s' err='%v'", provider.Name, err)
					return
				}
				// check of capabilities
				dlog(cfg.opt.Debug, "GoBootWorkers: check capabilities for provider '%s' connitem='%#v'", provider.Name, connitem)
				if err := checkCapabilities(provider, connitem); err != nil {
					dlog(always, "WARN Provider '%s' force NoUpload=true err=\"%v\" ", provider.Name, err)
					provider.NoUpload = true
				}
			}
			if !cfg.opt.CheckOnly && cfg.opt.Verbose {
				provider.mux.RLock()
				//dlog( "Capabilities: [ IHAVE: %s | POST: %s | CHECK: %s | STREAM: %s ] @ '%s' NoDl=%t NoUp=%t MaxConns=%d",
				dlog(always, "Capabilities: | IHAVE: %s | POST: %s | NoDL: %s | NoUP: %s | MaxC: %3d | @ '%s'",
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
				if cfg.opt.DebugWorker {
					dlog(cfg.opt.Debug, "GoBootWorkers PreBoot Provider '%s' launch wid=%d/%d", provider.Name, wid, provider.MaxConns)
				}
				globalmux.Lock()
				workerWGconnEstablish.Add(1)
				globalmux.Unlock()
				// GoWorker connecting....
				go s.GoWorker(wid, provider, waitWorker, workerWGconnEstablish, waitPool)
				// all providers boot up at the same time
				// 50 conns on a provider will need up to 2.5s to boot
				//time.Sleep(time.Duration(rand.Intn(50)) * time.Millisecond) // // give workers some space in time to start and connect
			}
		}(provider, workerWGconnEstablish, waitWorker, waitPool) // end go func
	} // end for s.providerList
	dlog(cfg.opt.DebugWorker, "Waiting for Workers to connect")
	workerWGconnEstablish.Done() // releases 1. had been set before calling GoBootWorkers
	workerWGconnEstablish.Wait() // waits for the others to release when connections are established

	// TODO this is not correct, we should check if all workers are connected
	dlog(cfg.opt.DebugWorker, "GoBootWorkers: all workers connected (or died if no conn could be established)")
} // end func GoBootWorkers

func (s *SESSION) GoWorker(wid int, provider *Provider, waitWorker *sync.WaitGroup, workerWGconnEstablish *sync.WaitGroup, waitPool *sync.WaitGroup) {
	dlog(cfg.opt.DebugWorker, "GoWorker (%d) launching routines '%s'", wid, provider.Name)
	globalmux.Lock()
	waitWorker.Add(3) // for the 3 routines per worker
	waitPool.Add(1)   // for the worker itself

	workerWGconnEstablish.Done()
	globalmux.Unlock()

	// Obtain a connection for this worker and share it among the check, download, and reupload routines.
	// The connection is not returned to the pool until all three routines have finished.
	var sharedCC chan *ConnItem = nil
	var err error
	if UseSharedCC {
		// Get a shared connection channel for the provider}
		sharedCC, err = GetNewSharedConnChannel(wid, provider)
		if err != nil || len(sharedCC) == 0 {
			dlog(always, "ERROR GoWorker (%d) failed to get shared connection channel for '%s' err='%v'", wid, provider.Name, err)
			return
		}
	}

	/* new worker code CheckRoutine */
	segCC := s.segmentChansCheck[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, sharedCC chan *ConnItem, segCC chan *segmentChanItem) {
		defer waitWorker.Done()
	forGoCheckRoutine:
		for {
			item := <-segCC
			if item == nil {
				if cfg.opt.DebugWorker {
					log.Print("CheckRoutine received a nil pointer to quit")
				}
				segCC <- nil // refill the nil so others will die too
				break forGoCheckRoutine
			}

			// we might get an item still locked for setting flags, so we lock too and wait for upper layer to release first.
			/*
				start := time.Now()

					item.mux.Lock()
					if cfg.opt.DebugWorker {
						dlog( "WorkerCheck: got lock (wid=%d) seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
					}
					item.mux.Unlock()
					if cfg.opt.DebugWorker {
						dlog( "WorkerCheck: unlocked (wid=%d) (waited=%d µs), process seg.Id='%s' @ '%s'", wid, time.Since(start).Microseconds(), item.segment.Id, provider.Name)
					}
			*/

			switch cfg.opt.ByPassSTAT {
			case false:
				if err := s.GoCheckRoutine(wid, provider, item, sharedCC); err != nil { // re-queue?
					dlog(always, "ERROR in GoCheckRoutine err='%v'", err)
				}
			case true:
				item.mux.Lock()
				item.flaginDL = true

				if !item.pushedDL {
					GCounter.IncrMax("dlQueueCnt", uint64(len(s.segmentList)), "CheckRoutine:ByPassSTAT")       // cfg.opt.ByPassSTAT
					GCounter.IncrMax("TOTAL_dlQueueCnt", uint64(len(s.segmentList)), "CheckRoutine:ByPassSTAT") //cfg.opt.ByPassSTAT
				}
				item.pushedDL = true // mark as pushed to download queue ByPassSTAT
				item.mux.Unlock()
				s.segmentChansDowns[provider.Group] <- item
			}
			continue forGoCheckRoutine
		} // end forGoCheckRoutine
	}(wid, provider, waitWorker, sharedCC, segCC) // end go func()

	/* new worker code DownsRoutine */
	segCD := s.segmentChansDowns[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, sharedCC chan *ConnItem, segCD chan *segmentChanItem) {
		defer waitWorker.Done()
	forGoDownsRoutine:
		for {
			dlog(cfg.opt.DebugWorker, "GoDownsRoutine: wid=%d provider='%s' wait on segCD len=%d", wid, provider.Name, len(segCD))
			item := <-segCD
			if item == nil {
				if cfg.opt.DebugWorker {
					log.Print("DownsRoutine received a nil pointer to quit")
				}
				segCD <- nil // refill the nil so others will die too
				break forGoDownsRoutine
			}
			dlog(cfg.opt.DebugWorker, "GoDownsRoutine: wid=%d provider='%s' start seg.Id='%s'", wid, provider.Name, item.segment.Id)
			// we might get an item still locked for setting flags, so we lock too and wait for upper layer to release first.

			start := time.Now()
			item.mux.Lock()
			dlog(cfg.opt.DebugWorker, "WorkerDown: got lock (wid=%d) seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
			item.mux.Unlock()
			dlog(cfg.opt.DebugWorker, "WorkerDown: unlocked (wid=%d) (waited=%d µs), process seg.Id='%s' @ '%s'", wid, time.Since(start).Microseconds(), item.segment.Id, provider.Name)

			StartDowns := time.Now()
			if err := s.GoDownsRoutine(wid, provider, item, sharedCC); err != nil {
				dlog(always, "ERROR in GoDownsRoutine err='%v'", err)
			}
			speedInKBytes := (float64(item.size) / 1024) / float64(time.Since(StartDowns).Seconds())
			dlog(cfg.opt.DebugWorker, "GoDownsRoutine: downloaded (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", wid, item.segment.Id, provider.Name, time.Since(StartDowns), speedInKBytes)

			continue forGoDownsRoutine
		} // end forGoDownsRoutine
	}(wid, provider, waitWorker, sharedCC, segCD) // end go func()

	/* new worker code ReupsRoutine */
	segCR := s.segmentChansReups[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, sharedCC chan *ConnItem, segCR chan *segmentChanItem) {
		defer waitWorker.Done()
	forGoReupsRoutine:
		for {
			item := <-segCR
			if item == nil {
				if cfg.opt.DebugWorker {
					log.Print("ReupsRoutine received a nil pointer to quit")
				}
				s.segmentChansReups[provider.Group] <- nil // refill the nil so others will die too
				break forGoReupsRoutine
			}

			// we might get an item still locked for setting flags, so we lock too and wait for upper layer to release first.

			start := time.Now()
			item.mux.Lock()
			if cfg.opt.DebugWorker {
				dlog(cfg.opt.DebugWorker, "WorkerReup: got lock (wid=%d) seg.Id='%s' @ '%s'", wid, item.segment.Id, provider.Name)
			}
			item.mux.Unlock()
			if cfg.opt.DebugWorker {
				dlog(cfg.opt.DebugWorker, "WorkerReup: unlocked (wid=%d) (waited=%d µs), process seg.Id='%s' @ '%s'", wid, time.Since(start).Microseconds(), item.segment.Id, provider.Name)
			}

			StartReUps := time.Now()
			if err := s.GoReupsRoutine(wid, provider, item, sharedCC); err != nil {
				dlog(always, "ERROR in GoReupsRoutine err='%v'", err)
			}
			speedInKBytes := (float64(item.size) / 1024) / float64(time.Since(StartReUps).Seconds())
			dlog(cfg.opt.DebugWorker, "ReupsRoutine: finished item (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", wid, item.segment.Id, provider.Name, time.Since(StartReUps), speedInKBytes)

			continue forGoReupsRoutine
		} // end forGoReupsRoutine
	}(wid, provider, waitWorker, sharedCC, segCR) // end go func()

	// wait for all 3 routines to finish
	if cfg.opt.DebugWorker {
		dlog(cfg.opt.DebugWorker, "GoWorker (%d) waitWorker.Wait for routines to complete '%s'", wid, provider.Name)
	}
	waitWorker.Wait() // wait for all 3 routines to finish

	if sharedCC != nil {
		select {
		case connitem := <-sharedCC:
			if connitem != nil {
				if cfg.opt.DebugWorker {
					dlog(cfg.opt.DebugWorker, "GoWorker (%d) parked sharedConn @ '%s'", wid, provider.Name)
				}
				provider.ConnPool.ParkConn(wid, connitem, "GoWorker:sharedCC:finalpark")
			}
		default:
			// no conn there?
			//KillConnPool(provider *Provider)
			dlog(cfg.opt.DebugWorker, "GoWorker (%d) no sharedConn there...? @ '%s' .. ciao", wid, provider.Name)
		}
	}

	waitPool.Done()        // release the worker from the pool
	KillConnPool(provider) // close the connection pool for this provider
	waitPool.Wait()        // wait for others to release too

	dlog(cfg.opt.DebugWorker, "GoWorker (%d) quit @ '%s'", wid, provider.Name)

} // end func GoWorker

func (s *SESSION) pushDL(allowDl bool, item *segmentChanItem) (pushed bool, nodl uint64) {
	if !allowDl {
		return
	}

	if !memlim.MemAvail() {
		return
	}

	item.mux.Lock()
	defer item.mux.Unlock()

	matchThis := (len(item.article) == 0 && !item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)
	if !matchThis {
		return false, 1
	}
providerDl:
	for pid, avail := range item.availableOn {
		if !avail {
			continue providerDl
		}
		if item.ignoreDlOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV] ignoreDlOn seg.Id='%s' @ '%s'", item.segment.Id, s.providerList[pid].Name)
			continue providerDl
		}
		if s.providerList[pid].NoDownload {
			nodl++
			item.ignoreDlOn[pid] = true
			continue providerDl
		}
		dlog(cfg.opt.DebugWorker, " | [DV] push chan <- down seg.Id='%s' @ '%s' item.flaginDL=%t item.flaginDLMEM=%t", item.segment.Id, s.providerList[pid].Name, item.flaginDL, item.flaginDLMEM)
		/* push download request only to 1.
		* this one should get it and update availableOn/missingOn list
		 */
		dst := ""
		if cfg.opt.CheckFirst && !cfg.opt.ByPassSTAT {
			select {
			case s.memDL[s.providerList[pid].Group] <- item:
				item.flaginDLMEM = true
				pushed = true
				dst = "memDL"
			default:
				// chan is full
			}
		} else if !cfg.opt.ByPassSTAT {
			if testing {
				s.segmentChansDowns[s.providerList[pid].Group] <- item
				pushed = true
				dst = "segDown"
			} else {
				select {
				case s.segmentChansDowns[s.providerList[pid].Group] <- item:
					pushed = true
					dst = "segDown"
				/* TODO FIXME REVIEW ! TO BLOCK OR NOT TO BLOCK*/
				default:
					dlog(cfg.opt.BUG, "DEBUG SPAM pushDL: chan is full for seg.Id='%s' @ '%s'", item.segment.Id, s.providerList[pid].Name)
					// chan is full
				}
			}
		}
		if pushed {
			item.flaginDL = true
			// item has been pushed to download queue: segmentChansDowns[provider.Group] or is queued in memDL[provider.Group]
			// if we forget to decrement dlQueueCnt at the right place(es): we will never stop...
			if !item.pushedDL {
				// increment dlQueueCnt counter only if not already pushed
				GCounter.IncrMax("dlQueueCnt", uint64(len(s.segmentList)), "pushDL")       // increment temporary dlQueueCnt counter
				GCounter.IncrMax("TOTAL_dlQueueCnt", uint64(len(s.segmentList)), "pushDL") // increment TOTAL_dlQueueCnt counter
			}
			item.pushedDL = true // mark as pushed to download queue (in pushDL)
			dlog(cfg.opt.DebugWorker, " | pushDL: chan pushed=%t seg.Id='%s' @ '%s' testing=%t dst=%s", pushed, item.segment.Id, s.providerList[pid].Name, testing, dst)

			return // return after 1st push!
		} // end if pushed to download queue
	} // end for providerDl
	return
} // end func pushDL

func (s *SESSION) pushUP(allowUp bool, item *segmentChanItem) (pushed bool, noup uint64, inretry uint64) {
	if !allowUp {
		return
	}

	item.mux.Lock()
	defer item.mux.Unlock()

	matchThis := (len(item.article) > 0 && item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM && !item.flaginDL)

	if !matchThis {
		return
	}
	// looks like we have fetched the segment and did not upload the item yet
providerUp:
	for pid, miss := range item.missingOn {
		if !miss {
			continue providerUp
		}
		//s.providerList[pid].mux.RLock() // FIXME TODO #b8bd287b: dynamic capas
		flagNoUp := (s.providerList[pid].NoUpload || (!s.providerList[pid].capabilities.ihave && !s.providerList[pid].capabilities.post))
		//s.providerList[pid].mux.RUnlock()

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
				delete(item.retryOn, pid) // remove retryOn flag for this provider
				// pass
			}
		}

		/* push upload request only to 1.
		this one should up it and usenet should distribute it within minutes
		*/

		/* TODO FIXME REVIEW ! TO BLOCK OR NOT TO BLOCK*/
		if testing { // blocking
			s.segmentChansReups[s.providerList[pid].Group] <- item
			pushed = true
		} else {
			select { // non-blocking
			case s.segmentChansReups[s.providerList[pid].Group] <- item:
				// pass
				pushed = true
			default:
				//dlog( "DEBUG SPAM pushUP: chan is full for seg.Id='%s' @ '%s'", item.segment.Id, s.providerList[pid].Name)
				// chan is full

			}
		}

		if pushed {
			item.flaginUP = true
			if !item.pushedUP {
				GCounter.Incr("upQueueCnt")
				GCounter.Incr("TOTAL_upQueueCnt")
			}
			item.pushedUP = true // mark as pushed to upload queue (in pushUP)
			dlog(cfg.opt.DebugWorker, " | pushUP: chan pushed=%t seg.Id='%s' @ '%s'", pushed, item.segment.Id, s.providerList[pid].Name)
			return // return after 1st push!
		} // end if pushed to upload queue
	} // end for providerUp
	return
} // end func pushUP

type WrappedItem struct {
	wItem *segmentChanItem
	src   string // source of the item, e.g. "WorkDividerChan"
}

const testing = false // set to true to test the worker loop
// GoWorkDivider is the main worker loop that processes segments
func (s *SESSION) GoWorkDivider(waitDivider *sync.WaitGroup, waitDividerDone *sync.WaitGroup) {
	if cfg.opt.DebugWorker {
		log.Print("go GoWorkDivider() waitDivider.Wait()")
	}
	waitDivider.Wait() // waits for all conns to establish
	defer waitDividerDone.Done()

	if cfg.opt.DebugWorker {
		log.Print("go GoWorkDivider() Starting!")
	}

	segcheckdone := false
	closeWait, closeCase := 1, ""
	todo := uint64(len(s.segmentList))
	providersCnt := len(s.providerList)

	if cfg.opt.CheckFirst {
		s.memDL = make(map[string]chan *segmentChanItem)
		for _, provider := range s.providerList {
			if s.memDL[provider.Group] == nil {
				s.memDL[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
			}
		}
	} // end if cfg.opt.CheckFirst

	if cfg.opt.UploadLater {
		s.memUP = make(map[string]chan *segmentChanItem)
		for _, provider := range s.providerList {
			if s.memUP[provider.Group] == nil {
				s.memUP[provider.Group] = make(chan *segmentChanItem, cfg.opt.ChanSize)
			}
		}
	} // end if cfg.opt.UploadLater

	// some values we count on in every loop to check how far processing
	nextLogPrint := time.Now().Unix() + cfg.opt.PrintStats
	var lastRunTook time.Duration

	allowDl := (!cfg.opt.CheckOnly)
	allowUp := (!cfg.opt.CheckOnly && GCounter.GetValue("postProviders") > 0)
	dlog(cfg.opt.DebugWorker, "DEBUG WORK DIVIDER: allowDl=%t, allowUp=%t", allowDl, allowUp)

	// strings
	var logstring, log00, log01, log02, log03, log04, log05, log06, log07, log08, log09, log10, log11, log99 string
	/*
		go func(s *SESSION) {
			if !testing {
				return
			}
			//var funcmux sync.Mutex
			//inup, indl, nodl, noup, inretry := uint64(0), uint64(0), uint64(0), uint64(0), uint64(0)
			//replychan := make(chan bool, 1) // internal replychan for the worker loop
			for {
				wrappedItem := <-s.WorkDividerChan
				item := wrappedItem.wItem // unwrap the item from the channel
				item.mux.RLock()
				if item.checkedOn != providersCnt {
					//dlog( " | [DV] WorkDividerChan debug#1 received item seg.Id='%s' checkedOn=%d", item.segment.Id, item.checkedOn)
					item.mux.RUnlock()
					continue // ignore item, will retry next run
				}
				item.mux.RUnlock()
				if wrappedItem.src != "CR" {
					dlog( " | [DV] WorkDividerChan debug#2 received item seg.Id='%s' checkedOn=%d src=%s", item.segment.Id, item.checkedOn, wrappedItem.src)
				}

				switch wrappedItem.src {

				case "DR":
					go func(item *segmentChanItem) {
						for {
							pushedUp, nNoUp, nInRetry := s.pushUP(allowUp, item)
							if pushedUp {
								dlog( " | [DV] PUSHED to Up seg.Id='%s' pushedUp=%t nNoUp=%d nInRetry=%d", item.segment.Id, pushedUp, nNoUp, nInRetry)
								break
							}
							dlog( " | [DV] WorkDividerChan retrying pushUP seg.Id='%s'", item.segment.Id)
							time.Sleep(1000 * time.Millisecond) // wait a bit before retrying
						}
					}(item)

				case "CR":
					go func(item *segmentChanItem) {
						for {
							pushedDl, nNoDl := s.pushDL(allowDl, item)
							if pushedDl {
								if cfg.opt.BUG {
									dlog( " | [DV] PUSHED to DL seg.Id='%s' pushedDl=%t nNoDl=%d", item.segment.Id, pushedDl, nNoDl)
								}
								break
							}
							dlog( " | [DV] WorkDividerChan retrying pushDL seg.Id='%s'", item.segment.Id)
							time.Sleep(1000 * time.Millisecond) // wait a bit before retrying
						}
					}(item)
				} // end switch wrappedItem.src
			}
		}(s)
	*/
	// loops forever over the s.segmentList and checks if there is anything to do for an item
forever:
	for {
		time.Sleep(time.Duration((lastRunTook.Milliseconds() * 2)) + (2555 * time.Millisecond))

		// uint64
		var segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ uint64

		// Tnodl, Tnoup counts segments that not have been downloaded
		// or uploaded because provider has config flat NoDownload or NoUpload set
		var Tnodl, Tnoup uint64

		startLoop := time.Now()

	forsegmentList:
		for _, item := range s.segmentList {
			item.mux.PrintStatus(false)

			item.mux.RLock() // RLOCKS HERE #824d
			if item.cached {
				cached++
			}

			if /*!cfg.opt.ByPassSTAT &&*/ item.checkedOn != providersCnt {
				// ignore item, will retry next run
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				continue forsegmentList
			}
			if !cfg.opt.ByPassSTAT {
				checked++
			} else {
				checked += uint64(item.checkedOn)
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
			if s.IsSegmentStupid(item, false) { // check me bug? NoDownload-flag !
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

			if !testing {
				pushedUp, nNoUp, nInRetry := s.pushUP(allowUp, item)
				if pushedUp {
					dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDup seg.Id='%s' pushedUp=%t inup=%d", item.segment.Id, pushedUp, inup)
				}
				noup += nNoUp
				//Tnoup += len(item.ignoreDlOn)
				inretry += nInRetry
				if !pushedUp && allowDl {
					pushedDl, nNoDl := s.pushDL(allowDl, item)
					nodl += nNoDl
					Tnodl += uint64(len(item.ignoreDlOn))
					if pushedDl {
						indl++
						//if cfg.opt.BUG {
						dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDdl seg.Id='%s' pushedDl=%t indl=%d", item.segment.Id, pushedDl, indl)
						//}
					}
				}
			}

		} // end for forsegmentList

		lastRunTook = time.Since(startLoop)

		dlog(cfg.opt.DebugWorker, " | [DV] lastRunTook='%d ms' '%v", lastRunTook.Milliseconds(), lastRunTook)

		if !cfg.opt.CheckOnly && cfg.opt.CheckFirst && segcheckdone /* && checked == todo */ {
			// releasing the DL quaken
			for _, provider := range s.providerList {
				dlq := len(s.memDL[provider.Group])
				if dlq == 0 {
					continue
				}
				dlog(cfg.opt.Verbose, " | [DV] | Feeding %d Downs to '%s'", dlq, provider.Group)

			feedDL:
				for {
					select {
					case item := <-s.memDL[provider.Group]:
						item.mux.Lock()
						item.flaginDLMEM = false
						item.mux.Unlock()
						s.segmentChansDowns[provider.Group] <- item
					default:
						// chan ran empty
						break feedDL
					}
				}
				dlog(cfg.opt.Verbose, " | [DV] | Done feeding %d Downs to '%s'", dlq, provider.Group)

			}
		} // end if argCheckFirst

		if cacheON && !cfg.opt.CheckOnly && cfg.opt.UploadLater && ((isdl == todo) || (cached == todo)) {
			//dlog( "release the UL quaken")
			for _, provider := range s.providerList {
				upq := len(s.memUP[provider.Group])
				if upq == 0 {
					continue
				}
				dlog(cfg.opt.Verbose, " | [DV] | Feeding %d Reups to '%s'", upq, provider.Group)
			feedUP:
				for {
					select {
					case item := <-s.memUP[provider.Group]:
						s.segmentChansReups[provider.Group] <- item
					default:
						// chan ran empty
						break feedUP
					}
				}
			}
		} // end if argUploadLater

		// part to print stats begins here
		upQ = GCounter.GetValue("upQueueCnt")
		TupQ := GCounter.GetValue("TOTAL_upQueueCnt")
		dlQ = GCounter.GetValue("dlQueueCnt")
		TdlQ := GCounter.GetValue("TOTAL_dlQueueCnt")
		yeQ = GCounter.GetValue("yencQueueCnt")
		TyeQ := GCounter.GetValue("TOTAL_yencQueueCnt")
		// print some stats and check if we're done
		if !cfg.opt.Bar && (cfg.opt.Verbose || cfg.opt.Debug) {
			CNTc, CNTd, CNTu := GCounter.GetValue("GoCheckRoutines"), GCounter.GetValue("GoDownsRoutines"), GCounter.GetValue("GoReupsRoutines")
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
					log01 = fmt.Sprintf(" | STAT:[%03.1f%%] (%"+s.D+"d)", check_perc, checked)
				} else {
					log01 = fmt.Sprintf(" | STAT:[done] (%"+s.D+"d)", checked)
				}
			}
			if done > 0 {
				if !cfg.opt.Debug {
					log02 = fmt.Sprintf(" | DONE:[%03.3f%%] (%"+s.D+"d/%d)", done_perc, done, todo)
				} else {
					if done_perc >= 99 && done_perc < 100 {
						log02 = fmt.Sprintf(" | DONE:[%03.9f%%] (%"+s.D+"d)", done_perc, done)
					} else {
						log02 = fmt.Sprintf(" | DONE:[%03.5f%%] (%"+s.D+"d)", done_perc, done)
					}
				}
			}
			if segm > 0 && segm != done {
				log03 = fmt.Sprintf(" | SEGM:[%03.3f%%] (%"+s.D+"d)", segm_perc, segm)
				if nodl > 0 {
					log03 = log03 + fmt.Sprintf(" nodl=%d/%d", nodl, Tnodl)
				}
				if noup > 0 {
					log03 = log03 + fmt.Sprintf(" noup=%d/%d", noup, Tnoup)
				}
			}
			if yenc_perc > 0 && yenc_perc != cache_perc {
				log04 = fmt.Sprintf(" | YENC:[%03.3f%%] (%"+s.D+"d / %"+s.D+"d Q:%d←%d)", yenc_perc, isyenc, TyeQ, inyenc, yeQ)
			}
			if dead > 0 {
				log05 = fmt.Sprintf(" | DEAD:[%03.3f%%] (%"+s.D+"d)", dead_perc, dead)
			}
			if dmca > 0 {
				log06 = fmt.Sprintf(" | DMCA:[%03.3f%%] (%"+s.D+"d)", dmca_perc, dmca)
			}
			if inretry > 0 {
				log07 = fmt.Sprintf(" | ERRS:(%"+s.D+"d)", inretry)
			}
			if indl > 0 || isdl > 0 || dlQ > 0 || TdlQ > 0 {
				log08 = fmt.Sprintf(" | DL:[%03.3f%%] (%"+s.D+"d / %"+s.D+"d  Q:%d=%d)", isdl_perc, isdl, TdlQ, dlQ, indl)
			}
			if cached > 0 {
				log09 = fmt.Sprintf(" | HD:[%03.3f%%] (%"+s.D+"d)", cache_perc, cached)
			}
			if inup > 0 || isup > 0 || upQ > 0 || TupQ > 0 {
				log10 = fmt.Sprintf(" | UP:[%03.3f%%] (%"+s.D+"d / %"+s.D+"d  Q:%d←%d)", isup_perc, isup, TupQ, upQ, inup)
			}

			if cfg.opt.Verbose && !cfg.opt.Debug {
				openConns, idleConns := 0, 0
				for _, prov := range s.providerList {
					oc, ic := prov.ConnPool.GetStats()
					openConns += oc
					idleConns += ic
				}
				log11 = fmt.Sprintf(" | MEM:%d/%d [Cr=%d|Dr=%d|Ur=%d] [oC:%d iC=%d) ", used_slots, max_slots, CNTc, CNTd, CNTu, openConns, idleConns)

			} else if cfg.opt.DebugWorker {
				memdata := memlim.ViewData()

				log11 = fmt.Sprintf("\n memdata=%d:\n memdata='\n%#v\n' \n | ",
					len(memdata), memdata)

				log11 = log11 + fmt.Sprintf("MEM:%d/%d [Cr=%d|Dr=%d|Ur=%d] ",
					used_slots, max_slots, CNTc, CNTd, CNTu)

				log11 = log11 + fmt.Sprintf("[CNTmemWait=%d/%d|CNTmemRet=%d|MemOpen=%d|MemWaitReturn=%d] ",
					GCounter.GetValue("MemLockWait"), GCounter.GetValue("TOTAL_MemLockWait"), GCounter.GetValue("TOTAL_MemReturned"), GCounter.GetValue("TOTAL_MemLockWait")-GCounter.GetValue("TOTAL_MemReturned"), GCounter.GetValue("WAIT_MemReturn"))

				log11 = log11 + fmt.Sprintf("(indl=%d|inup=%d) CP:(parked=%d|get=%d|new=%d|dis=%d|open=%d|wait=%d)",
					indl, inup, GCounter.GetValue("TOTAL_ParkedConns"), GCounter.GetValue("TOTAL_GetConns"), GCounter.GetValue("TOTAL_NewConns"), GCounter.GetValue("TOTAL_DisConns"), GCounter.GetValue("TOTAL_NewConns")-GCounter.GetValue("TOTAL_DisConns"), GCounter.GetValue("WaitingGetConns"))

			}

			// build stats log string ...
			logstring = log00 + log01 + log02 + log03 + log04 + log05 + log06 + log07 + log08 + log09 + log10 + log11 + log99
			if cfg.opt.Verbose && cfg.opt.PrintStats >= 0 && logstring != "" && nextLogPrint < time.Now().Unix() {
				nextLogPrint = time.Now().Unix() + cfg.opt.PrintStats
				log.Print(logstring)
			}
		} // print some stats ends here

		s.mux.RLock()
		checkDone := s.checkDone // read value of s.checkDone
		s.mux.RUnlock()

		if !segcheckdone && (checked == todo || checkDone) {
			s.mux.Lock()
			s.segmentCheckEndTime = time.Now()
			took := time.Since(s.segmentCheckStartTime)
			s.segmentCheckTook = took
			s.mux.Unlock()
			segcheckdone = true
			/*
				if cfg.opt.Bar {
					segmentBar.SetMessage("done") // segmentBar
				}
			*/
			dlog(cfg.opt.Verbose, " | [DV] | Segment Check Done: took='%.1f sec'", took.Seconds())
		}

		// continue as long as any of this triggers because stuff is still in queues and processing
		if checked != todo || inup > 0 || indl > 0 || inretry > 0 || inyenc > 0 || dlQ > 0 || upQ > 0 || yeQ > 0 {
			dlog(cfg.opt.DebugWorker, "\n[DV] continue [ TupQ=%d !=? isup=%d || TdlQ=%d !=? isdl=%d || inup=%d > 0? || indl=%d > 0? || inretry=%d > 0? ]", TupQ, isup, TdlQ, isdl, inup, indl, inretry)
			continue forever
		}

		if closeWait <= 0 {
			if closeCase != "" {
				dlog(cfg.opt.DebugWorker, " | [DV] closeCase='%s'", closeCase)
			}
			dlog(cfg.opt.DebugWorker, " | [DV] quit all 0? inup=%d indl=%d inretry=%d inyenc=%d dlQ=%d upQ=%d yeQ=%d", inup, indl, inretry, inyenc, dlQ, upQ, yeQ)
			break forever
		}

		// figure out if all jobs are done
		globalmux.RLock()
		closeCase0 := (done == todo)
		closeCase1 := (cfg.opt.CheckOnly)
		closeCase2 := (cacheON && (GCounter.GetValue("postProviders") == 0 && cached == todo))
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
			dlog(always, "WARN [DV] hit impossible closeCase ... kill in %d more loops", 15-closeWait)
			closeWait++
			if closeWait >= 15 {
				log.Print("... force quit ...")
				os.Exit(1)
			}
			closeCase = ""
		}
		if closeCase != "" {
			dlog(always, " | DV closeCase='%s' closeWait=%d", closeCase, closeWait)
		}
	} // end forever

	/*
		if !cfg.opt.Bar && logstring != "" { // always prints final logstring
			log.Print("Final: "+logstring)
		}
	*/

	dlog(cfg.opt.DebugWorker, "%s\n   WorkDivider quit: closeCase='%s'", logstring, closeCase)

} // end func WorkDivider
