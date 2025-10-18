package main

import (
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// GoBootWorkers boots up all workers for a session
func (s *SESSION) GoBootWorkers(waitDivider *sync.WaitGroup, workerWGconnReady *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup, byteSize int64) {
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
	globalmux.Unlock()

	defer waitDivider.Done() // allows WorkDivider to run when this releases

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

	if cfg.opt.ChanSize <= 0 {
		cfg.opt.ChanSize = len(s.segmentList)
	}

	// loop over the providerList and boot up anonymous workers for each provider
	for _, provider := range s.providerList {

		dlog(cfg.opt.DebugWorker, "GoBootWorkers list=%d check provider='%#v' ", len(s.providerList), provider.Name)

		if !provider.Enabled || provider.MaxConns <= 0 {
			dlog(cfg.opt.Verbose, "ignored provider: '%s'", provider.Name)
			continue
		}

		// boot up a worker for this provider in an anonymous go routine
		go func(provider *Provider, workerWGconnReady *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup) {
			// releases at end of func when provider capabilities have been checked and all GoWorker() booted
			defer workerWGconnReady.Done() // release #1 for every provider in go func
			if provider.Group == "" {
				provider.Group = provider.Name
			}

			dlog(cfg.opt.Debug, "GoBootWorkers: Booting Provider '%s' group '%s'", provider.Name, provider.Group)

			// create once if not exists
			globalmux.Lock()
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

				// launch anonymous go routine to feed the segmentChanCheck with pointers to segmentChanItem
				go func(segmentChanCheck chan *segmentChanItem) {

					start := time.Now()
					for _, item := range s.segmentList {
						segmentChanCheck <- item // is allowed to block inside feeder routine
					}
					for {
						time.Sleep(time.Second) // wait for check routine to empty out the chan
						if len(segmentChanCheck) == 0 {
							break
						}
					}
					dlog(cfg.opt.Verbose, " | Done feeding items=%d -> segmentChanCheck Group '%s' took='%.0f sec'", len(s.segmentList), provider.Group, time.Since(start).Seconds())
					s.mux.Lock()
					// set checkFeedDone only signals that feeding to the channel is done
					// the check routine may still be activly checking!
					s.checkFeedDone = true
					s.mux.Unlock()
					close(segmentChanCheck) // close the chan to signal no more items will come
				}(s.segmentChansCheck[provider.Group])
			}
			globalmux.Unlock()

			dlog(cfg.opt.Verbose, "Connecting to Provider '%s' MaxConns=%d SSL=%t", provider.Name, provider.MaxConns, provider.SSL)

			if !cfg.opt.CheckOnly && !provider.NoUpload {
				dlog(cfg.opt.Debug, "GoBootWorkers: get conn to check capabilities for provider '%s'", provider.Name)

				// get a connection item from the provider's connection pool
				connitem, err := provider.ConnPool.GetConn()
				if err != nil {
					dlog(always, "ERROR Boot Provider '%s' err='%v'", provider.Name, err)
					// Release waitWorker and waitPool counters for all workers that won't be started
					for i := 0; i < provider.MaxConns; i++ {
						waitWorker.Done() // 3 times per worker
						waitWorker.Done()
						waitWorker.Done()
						waitPool.Done() // 1 time per worker
					}
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
				// GoWorker connecting....
				go s.GoWorker(wid, provider, waitWorker, waitPool)
				time.Sleep(time.Duration(rand.Intn(50*provider.MaxConns)) * time.Millisecond) // random sleep to avoid all workers connecting at the same time
			}

		}(provider, workerWGconnReady, waitWorker, waitPool) // end go func
	} // end for s.providerList
	dlog(cfg.opt.DebugWorker, "Waiting for Workers to connect")
	workerWGconnReady.Wait() // wait for all others to boot up
	// TODO this is not correct, we should check if all workers are connected
	dlog(cfg.opt.DebugWorker, "GoBootWorkers: all workers connected (or died if no conn could be established)")
} // end func GoBootWorkers

func (s *SESSION) GoWorker(wid int, provider *Provider, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup) {
	dlog(cfg.opt.DebugWorker, "GoWorker (%d) launching routines '%s'", wid, provider.Name)
	var thisWorkerWG sync.WaitGroup
	thisWorkerWG.Add(3) // we have 3 routines per worker
	// Obtain a connection for this worker and share it among the check, download, and reupload routines.
	// The connection is not returned to the pool until all three routines have finished.
	var sharedCC chan *ConnItem = nil
	var err error
	if UseSharedCC {
		dlog(cfg.opt.DebugWorker, "GoWorker (%d) trying to get shared connection channel for '%s'", wid, provider.Name)
		// Get a shared connection channel for the provider pre filled with an established connection
		sharedCC, err = GetNewSharedConnChannel(wid, provider)
		if err != nil || len(sharedCC) == 0 {
			// release for the 3 routines we won't start
			waitWorker.Done()
			waitWorker.Done()
			waitWorker.Done()
			// release this GoWorker from the pool
			waitPool.Done()
			dlog(always, "ERROR GoWorker (%d) failed to get shared conn for '%s' err='%v' close worker", wid, provider.Name, err)
			return
		}
	}
	s.mux.Lock()
	s.bootedWorkers++
	s.mux.Unlock()
	/* new worker code CheckRoutine */
	segCC := s.segmentChansCheck[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, thisWorkerWG *sync.WaitGroup, sharedCC chan *ConnItem, segCC chan *segmentChanItem) {
		var processed uint64
		defer waitWorker.Done()
		defer thisWorkerWG.Done()
		timeOut := time.Now().Add(15 * time.Second)
		lastGood := time.Now()
		toChan := make(chan struct{}, 1)
		go func() {
			for {
				time.Sleep(3 * time.Second)
				select {
				case toChan <- struct{}{}:
				default:
				}
			}
		}()
	forGoCheckRoutine:
		for {
			select {
			case <-toChan:
				if time.Now().After(timeOut) {
					dlog(always, "CheckRoutine: (%d@'%s') timeout reached, exiting. lastGood: %v", wid, provider.Name, time.Since(lastGood))
					break forGoCheckRoutine
				}
				dlog(time.Since(lastGood) > 9*time.Second, "CheckRoutine: (%d@'%s') waiting on segCC len=%d timeout in: %v (lastGood: %v)", wid, provider.Name, len(segCC), time.Until(timeOut), time.Since(lastGood))
			case item, ok := <-segCC:
				if !ok {
					dlog(cfg.opt.DebugWorker, "CheckRoutine: channel closed (%d@'%s')", wid, provider.Name)
					break forGoCheckRoutine // channel is closed, exit the routine
				}
				if item == nil {
					dlog(always, "CheckRoutine received a nil pointer to quit")
					segCC <- nil // refill the nil so others will die too
					break forGoCheckRoutine
				}
				timeOut = time.Now().Add(15 * time.Second)
				lastGood = time.Now()
				switch cfg.opt.ByPassSTAT {
				case false:
					code, err := s.GoCheckRoutine(wid, provider, item, sharedCC)
					item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoCheckRoutine: code=%d", code))
					if err != nil { // re-queue?
						dlog(always, "ERROR in GoCheckRoutine err='%v'", err)
					}
				case true:
					log.Fatal("you should not be here! Quitting...") // FIXME TODO: remove this fatal error
				}
				processed++
				continue forGoCheckRoutine
			}
		} // end forGoCheckRoutine
		dlog(processed > 0, "CheckRoutine exiting (%d@'%s') processed: %d", wid, provider.Name, processed)
	}(wid, provider, waitWorker, &thisWorkerWG, sharedCC, segCC) // end go func()

	/* new worker code DownsRoutine */
	segCD := s.segmentChansDowns[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, thisWorkerWG *sync.WaitGroup, sharedCC chan *ConnItem, segCD chan *segmentChanItem) {
		var processed uint64
		defer waitWorker.Done()
		defer thisWorkerWG.Done()
		timeOut := time.Now().Add(15 * time.Second)
		lastGood := time.Now()
		toChan := make(chan struct{}, 1)
		go func() {
			for {
				time.Sleep(3 * time.Second)
				select {
				case toChan <- struct{}{}:
				default:
				}
			}
		}()
	forGoDownsRoutine:
		for {
			dlog(cfg.opt.DebugWorker, "GoDownsRoutine: (%d@'%s') wait on segCD len=%d", wid, provider.Name, len(segCD))
			select {
			case <-toChan:
				s.mux.RLock()
				segcheckdone := s.segcheckdone // get the segcheckdone state
				s.mux.RUnlock()
				if !segcheckdone {
					timeOut = time.Now().Add(15 * time.Second)
					lastGood = time.Now() // reset lastGood if segcheck is not done
					continue forGoDownsRoutine
				}
				if segcheckdone && time.Now().After(timeOut) {
					dlog(always, "GoDownsRoutine: (%d@'%s') timeout reached, exiting. lastGood: %v", wid, provider.Name, time.Since(lastGood))
					break forGoDownsRoutine
				}
				dlog(time.Since(lastGood) > 9*time.Second && always && segcheckdone, "GoDownsRoutine: (%d@'%s') waiting on segCD len=%d timeout in: %v (lastGood: %v)", wid, provider.Name, len(segCD), time.Until(timeOut), time.Since(lastGood))
			case item, ok := <-segCD:
				if !ok {
					dlog(cfg.opt.DebugWorker, "GoDownsRoutine: channel closed (%d@'%s')", wid, provider.Name)
					break forGoDownsRoutine
				}
				if item == nil {
					dlog(cfg.opt.DebugWorker, "DownsRoutine received a nil pointer to quit")
					segCD <- nil // refill the nil so others will die too
					break forGoDownsRoutine
				}
				timeOut = time.Now().Add(15 * time.Second)
				lastGood = time.Now()
				dlog(cfg.opt.DebugWorker, "GoDownsRoutine: (%d@'%s') start seg.Id='%s'", wid, provider.Name, item.segment.Id)

				start := time.Now()
				who := fmt.Sprintf("DR=%d@'%s'#'%s' seg.Id='%s'", wid, provider.Name, provider.Group, item.segment.Id)
				memlim.MemLockWait(item, who) // gets memlock here
				dlog(cfg.opt.DebugWorker && cfg.opt.DebugMemlim, "GoDownsRoutine got MemCheckWait who='%s' waited=(%d ms)", who, time.Since(start).Milliseconds())
				errStr := ""
				StartDowns := time.Now()
				code, err := s.GoDownsRoutine(wid, provider, item, sharedCC)
				item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoDownsRoutine: code=%d", code))
				processed++
				DecreaseDLQueueCnt()
				if err != nil || (code != 220 && code != 920) {
					if code != 430 {
						// 430 is a normal error code for GoDownsRoutine, so we don't log it as an error
						errStr = fmt.Sprintf("ERROR in GoDownsRoutine code='%d' err='%v'. no retry", code, err)
						dlog(always, "%s", errStr)
					}
					memlim.MemReturn("MemRetOnERR: "+errStr, item) // memfree GoDownsRoutine on error
					continue forGoDownsRoutine
				}
				var speedInKBytes float64
				mode := "downloaded"
				if item.size > 0 {
					speedInKBytes = (float64(item.size) / 1024) / float64(time.Since(StartDowns).Seconds())
				}
				switch code {
				case 220:
					// pass

				case 920: // 920 is a special code for GoDownsRoutine to indicate that the item has been read from cache
					mode = "cache read"
					// Memory was already returned in GoDownsRoutine for cache reads
				}

				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, "GoDownsRoutine: segId='%s' (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", mode, wid, item.segment.Id, provider.Name, time.Since(StartDowns), speedInKBytes)

				// back to top
			} // end select
		} // end forGoDownsRoutine
		dlog(processed > 0, "GoDownsRoutine exiting (%d@'%s') processed: %d", wid, provider.Name, processed)
	}(wid, provider, waitWorker, &thisWorkerWG, sharedCC, segCD) // end go func()

	/* new worker code ReupsRoutine */
	segCR := s.segmentChansReups[provider.Group]
	go func(wid int, provider *Provider, waitWorker *sync.WaitGroup, thisWorkerWG *sync.WaitGroup, sharedCC chan *ConnItem, segCR chan *segmentChanItem) {
		var processed uint64
		defer waitWorker.Done()
		defer thisWorkerWG.Done()
		timeOut := time.Now().Add(15 * time.Second)
		lastGood := time.Now()
		toChan := make(chan struct{}, 1)
		go func() {
			for {
				time.Sleep(3 * time.Second)
				select {
				case toChan <- struct{}{}:
				default:
				}
			}
		}()
	forGoReupsRoutine:
		for {
			dlog(cfg.opt.DebugWorker, "ReupsRoutine: (%d@'%s') wait on segCD len=%d", wid, provider.Name, len(segCD))
			select {
			case <-toChan:
				s.mux.RLock()
				segcheckdone := s.segcheckdone // get the segcheckdone state
				s.mux.RUnlock()
				if !segcheckdone {
					timeOut = time.Now().Add(15 * time.Second)
					lastGood = time.Now() // reset lastGood if segcheck is not done
					continue forGoReupsRoutine
				}
				if segcheckdone && time.Now().After(timeOut) {
					dlog(always, "ReupsRoutine: (%d@'%s') timeout reached, exiting. lastGood: %v", wid, provider.Name, time.Since(lastGood))
					break forGoReupsRoutine
				}
				dlog(time.Since(lastGood) > 9*time.Second, "ReupsRoutine: (%d@'%s') waiting on segCR len=%d timeout in: %v (lastgood: %v)", wid, provider.Name, len(segCR), time.Until(timeOut), time.Since(lastGood))

			case item, ok := <-segCR:
				if !ok {
					dlog(cfg.opt.DebugWorker, "ReupsRoutine: channel closed (%d@'%s')", wid, provider.Name)
					break forGoReupsRoutine
				}
				if item == nil {
					dlog(cfg.opt.DebugWorker, "ReupsRoutine received a nil pointer to quit")
					s.segmentChansReups[provider.Group] <- nil // refill the nil so others will die too
					break forGoReupsRoutine
				}
				timeOut = time.Now().Add(15 * time.Second)
				lastGood = time.Now()
				StartReUps := lastGood
				code, err := s.GoReupsRoutine(wid, provider, item, sharedCC)
				item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoReupsRoutine: code=%d", code))
				processed++
				DecreaseUPQueueCnt()
				if err != nil || code == 0 {
					errStr := fmt.Sprintf("ERROR in GoReupsRoutine code='%d' err='%v' no retry", code, err)
					dlog(always, "%s", errStr)
					memlim.MemReturn("MemRetOnERR: "+errStr, item) // memfree GoReupsRoutine on error
					continue forGoReupsRoutine
				}
				speedInKBytes := (float64(item.size) / 1024) / float64(time.Since(StartReUps).Seconds())
				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, "ReupsRoutine: finished item (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", wid, item.segment.Id, provider.Name, time.Since(StartReUps), speedInKBytes)

				memlim.MemReturn("UR", item) // memfree GoReupsRoutine on success
				// back to top
			} // end select
		} // end forGoReupsRoutine
		dlog(processed > 0, "ReupsRoutine exiting: (%d@'%s') processed: %d", wid, provider.Name, processed)
	}(wid, provider, waitWorker, &thisWorkerWG, sharedCC, segCR) // end go func()

	dlog(cfg.opt.DebugWorker, "GoWorker (%d) waitWorker.Wait for routines to complete '%s'", wid, provider.Name)
	thisWorkerWG.Wait() // wait for all 3 routines to finish
	dlog(cfg.opt.DebugWorker, "GoWorker closing (%d@'%s')", wid, provider.Name)

	if sharedCC != nil {
		select {
		case connitem := <-sharedCC:
			if connitem != nil {
				dlog(cfg.opt.DebugWorker, "GoWorker (%d) parked sharedConn @ '%s'", wid, provider.Name)
				provider.ConnPool.ParkConn(wid, connitem, "GoWorker:sharedCC:finalpark")
			}
		default:
			// no conn there?
			//KillConnPool(provider *Provider)
			dlog(cfg.opt.DebugWorker, "GoWorker (%d) no sharedConn there...? @ '%s' .. ciao", wid, provider.Name)
		}
	}
	s.mux.Lock()
	s.bootedWorkers--
	s.mux.Unlock()
	dlog(cfg.opt.DebugWorker, "GoWorker (%d@'%s') done with routines, releasing pool", wid, provider.Name)
	waitPool.Done() // release this GoWorker from the pool
	waitWorker.Wait()
	dlog(cfg.opt.DebugWorker, "GoWorker quit (%d@'%s')  ", wid, provider.Name)
} // end func GoWorker

// matchThisDL checks if the item is a candidate for download
// it returns true if the item is a candidate for download
func matchThisDL(item *segmentChanItem) bool {
	// item is a candidate for download if it has no size, is not in DL, not in UP, and not in DLMEM
	switch cacheON {
	case false:
		// if cache is off...
		return (len(item.article) == 0 && !item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)

	case true:
		if (len(item.article) > 0 || item.flagCache) && !item.flagisUP && !item.flaginUP && !item.flaginDLMEM {
			return (!item.flaginDL && !item.flagisDL)
		}
		return (len(item.article) == 0 && !item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)
	}

	return (!item.flaginDL && !item.flagisDL && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM)
} // end func matchThisDL

func (item *segmentChanItem) FlagError(providerId int) {
	log.Printf("FlagError called for providerId=%d on segment.Id='%s' waiting to lock item mutex", providerId, item.segment.Id)
	item.mux.Lock()
	defer item.mux.Unlock()
	//item.ignoreDlOn[providerId] = true
	//item.ignoreUlOn[providerId] = true
	//item.errorOn[providerId] = true
	if item.flaginUP {
		if item.fails < 3 {
			item.retryIn = int64(time.Now().Add(5 * time.Second).Second())
		}
		item.flaginUP = false
	}

	item.flaginDL = false
	item.fails++
	delete(item.availableOn, providerId)
	dlog(always, "Flagged item error, will not retry '%s' on providerId=%d", item.segment.Id, providerId)
}

func (item *segmentChanItem) FlagDLFailed(providerList []*Provider, providerGroup string) {
	item.mux.Lock()
	defer item.mux.Unlock()
	for id, prov := range providerList {
		if prov.Group != providerGroup {
			continue
		}
		item.ignoreDlOn[id] = true
		item.missingOn[id] = true
		item.errorOn[id] = true
		delete(item.availableOn, id)
	}
}

// matchThisUP checks if the item is a candidate for upload
// it returns true if the item is a candidate for upload
func matchThisUP(item *segmentChanItem) bool {
	return (len(item.article) > 0 && (item.flagisDL || item.flagCache) && !item.flaginUP && !item.flagisUP && !item.flaginDLMEM && !item.flaginDL)
} // end func matchThisUP

// pushDL tries to push the item to the download queue
func (s *SESSION) pushDL(allowDl bool, item *segmentChanItem) (pushed bool, nodl uint64, err error) {
	if !allowDl {
		return
	}

	item.mux.Lock() // LOCK item for the duration of this function
	defer item.mux.Unlock()

	if cfg.opt.CheckFirst {
		s.mux.RLock()
		defer s.mux.RUnlock()
		switch s.segcheckdone {
		case false:
			if !item.flaginDLMEM {
				item.flaginDLMEM = true // set flag to wait and release the quaken later
			}
			return false, 1, nil
		case true:
			if item.flaginDLMEM {
				item.flaginDLMEM = false // release the quaken if we are done with the check
			}
			// pass
		}
	}

	if !matchThisDL(item) {
		item.PrintItemFlags(cfg.opt.DebugFlags, false, "pushDL")
		dlog(cfg.opt.DebugWorker && cfg.opt.BUG, " | [DV-pushDL] (nodl) matchNoDL#1 seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t len(article)=%d", item.segment.Id, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP, len(item.article))
		return false, 1, nil // not a match, item is already in DL or UP or has article
	}
	if !memlim.MemAvail() {
		return false, 1, nil // not enough memory available to download
	}
	// if we are here, we are allowed to push the item to download queue
	// loop over the availableOn map and check if we can push the item to download
providerDl:
	for pid, avail := range item.availableOn {
		if !avail {
			dlog(always, " | [DV-pushDL] (nodl) skip seg.Id='%s' @ #'%s' not availableOn ", item.segment.Id, s.providerList[pid].Group)
			continue providerDl
		}
		if item.ignoreDlOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) ignoreDlOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerDl
		}
		if item.missingOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) item missingOn but should be avail!? seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerDl
		}
		if item.errorOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) item errorOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerDl
		}
		if s.providerList[pid].NoDownload {
			nodl++
			item.ignoreDlOn[pid] = true
			continue providerDl
		}
		dlog(cfg.opt.BUG, " | [DV-pushDL] try push chan <- down seg.Id='%s' @ #'%s' item.flaginDL=%t item.flaginDLMEM=%t", item.segment.Id, s.providerList[pid].Group, item.flaginDL, item.flaginDLMEM)
		/* pushDL sends download request only to 1.
		* this one should get it and update availableOn/missingOn list
		 */
		/*
			dst := ""
			if cfg.opt.CheckFirst && !cfg.opt.ByPassSTAT {
				if !checkDone && !item.flaginDLMEM {
					item.flaginDLMEM = true // set flag to release the quaken later
				}
				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, " | [DV-pushDL] flagged as memDL seg.Id='%s' @ #'%s' item.flaginDL=%t item.flaginDLMEM=%t", item.segment.Id, s.providerList[pid].Group, item.flaginDL, item.flaginDLMEM)
				continue providerDl // continue with next provider in item.availableOn

			} else if !cfg.opt.ByPassSTAT {
		*/
		// if we don't check first and did not bypass STAT, we can push items to download queue for provider-group
		select {
		case s.segmentChansDowns[s.providerList[pid].Group] <- item: // non-blocking
			pushed = true
			item.flaginDL = true
			item.pushedDL++ // mark as pushed to download queue (in pushDL)
			IncreaseDLQueueCnt()
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] pushed to dlchan seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			return pushed, 0, nil // return after 1st push!
		default:
			dlog(cfg.opt.BUG, "DEBUG SPAM pushDL: chan is full @ #'%s', retry next", s.providerList[pid].Group)
			// chan is full means we cannot push the item to the download queue to this provider group
			// either app is blocked or we're just checking faster than we can download at all...
			//err = fmt.Errorf(" | [DV-pushDL] chans full @ '%s'#'%s'", s.providerList[pid].Name, s.providerList[pid].Group)
			// FIXME TODO: should we return here or continue with next provider-group?
			return false, 0, nil
		} // end select
		//}
	} // end for providerDl
	return false, 1, nil
} // end func pushDL

func (s *SESSION) pushUP(allowUp bool, item *segmentChanItem) (pushed bool, noup uint64, inretry uint64, err error) {
	if !allowUp {
		return false, 1, 0, fmt.Errorf("pushUP not allowed")
	}

	s.mux.RLock()
	segcheckdone := s.segcheckdone // get the segcheckdone state
	s.mux.RUnlock()
	if cfg.opt.CheckFirst && !segcheckdone {
		return false, 1, 0, nil // still checking, do not push to upload yet
	}

	item.mux.Lock() // LOCK item for the duration of this function
	defer item.mux.Unlock()

	if !matchThisUP(item) {
		item.PrintItemFlags(cfg.opt.DebugFlags, false, "pushUP")
		//dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) nomatch seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t", item.segment.Id, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP)
		return false, 1, 0, nil // not a match, item is already in DL or UP or has article
	}
	dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (tryup) passed matchNoUP seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t", item.segment.Id, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP)
	// looks like we have fetched the segment and did not upload the item yet
providerUp:
	for pid, miss := range item.missingOn {
		if !miss {
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) skip seg.Id='%s' @ #'%s' not missingOn ", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		//s.providerList[pid].mux.RLock() // FIXME TODO #b8bd287b: dynamic capas
		flagNoUp := (s.providerList[pid].NoUpload || (!s.providerList[pid].capabilities.ihave && !s.providerList[pid].capabilities.post))
		//s.providerList[pid].mux.RUnlock()
		if flagNoUp {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) flagNoUp seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.ignoreUlOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) ignoreUlOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.uploadedTo[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) uploadedTo seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.availableOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) availableOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.unwantedOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) unwantedOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.dmcaOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) dmcaOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.errorOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) errorOn seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			continue providerUp
		}
		if item.retryOn[pid] {
			if item.retryIn > time.Now().Unix() {
				inretry++
				dlog(always, " | [DV-pushUP] (noup) retryOn seg.Id='%s' @ #'%s' retryIn=%d > now=%d, continue", item.segment.Id, s.providerList[pid].Group, item.retryIn, time.Now().Unix())
				continue providerUp
			} else {
				dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) retryOn seg.Id='%s' @ #'%s' retryIn=%d <= now=%d, pass", item.segment.Id, s.providerList[pid].Group, item.retryIn, time.Now().Unix())
				delete(item.retryOn, pid) // remove retryOn flag for this provider
				// pass
			}
		}

		/* push upload request only to 1.
		this one should up it and usenet should distribute it within minutes
		*/
		select { // non-blocking
		case s.segmentChansReups[s.providerList[pid].Group] <- item: // non-blocking
			// pass
			item.flaginUP = true
			item.pushedUP++      // mark as pushed to upload queue (in pushUP)
			IncreaseUPQueueCnt() // increment upQueueCnt counter
			//GCounter.IncrMax("upQueueCnt", uint64(len(s.segmentList)), "pushUP")
			//GCounter.IncrMax("TOTAL_upQueueCnt", uint64(len(s.segmentList)), "pushUP")
			dlog(cfg.opt.DebugWorker, " | pushUP: in chan seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
			return true, 0, 0, nil // return after 1st push to a group!
		default:
			//dlog(cfg.opt.BUG "DEBUG SPAM pushUP: chan is full @ '%s'", s.providerList[pid].Name)
			// chan is full for this group, try next provider-group or not? FIXME TODO REVIEW
			noup++
			return false, noup, inretry, fmt.Errorf("pushUP: chan is full for seg.Id='%s' @ #'%s'", item.segment.Id, s.providerList[pid].Group)
		}
	} // end for providerUp
	return false, noup, inretry, fmt.Errorf("pushUP: no provider found for seg.Id='%s'", item.segment.Id)
} // end func pushUP

type WrappedItem struct {
	wItem *segmentChanItem
	src   string // source of the item, e.g. "WorkDividerChan"
	//dst   string // destination of the item, e.g. "CR" for CheckRoutine, "DR" for DownsRoutine
}

const testing = false // set to true to test the worker loop
// GoWorkDivider is the main worker loop that processes segments
func (s *SESSION) GoWorkDivider(waitDivider *sync.WaitGroup, waitDividerDone *sync.WaitGroup) {
	dlog(cfg.opt.DebugWorker, "go GoWorkDivider() waiting on waitDivider.Wait()")
	waitDivider.Wait() // waits for all conns to establish
	defer waitDividerDone.Done()
	dlog(cfg.opt.DebugWorker, "go GoWorkDivider() Starting!")

	//segcheckdone := false
	closeWait, closeCase := 1, ""
	todo := uint64(len(s.segmentList))
	providersCnt := len(s.providerList)
	// some values we count on in every loop to check how far processing
	nextLogPrint := time.Now().Unix() + cfg.opt.PrintStats

	allowDl := (!cfg.opt.CheckOnly)
	allowUp := (!cfg.opt.CheckOnly && GCounter.GetValue("postProviders") > 0)
	dlog(cfg.opt.DebugWorker, "DEBUG WORK DIVIDER: allowDl=%t, allowUp=%t", allowDl, allowUp)

	// strings
	var logstring, log00, log01, log02, log03, log04, log05, log06, log07, log08, log09, log10, log11, log99 string

	// loops forever over the s.segmentList and checks if there is anything to do for an item
	var minsleep int64 = 1000
	var baseline int64 = 1500
	var maxsleep int64 = 6000
	microsleep := baseline
	fetchedtoDL, fetchedtoUP, backlogDL, refillUP, pushedDL, pushedUP := uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0)
	startLoop := time.Now()
	deadWorkersDeadline := 9
	deadcounter := 10
	var capture_done, capture_segm, capture_isdl, capture_isup float64

forever:
	for {

		dvlastRunTook := time.Since(startLoop)
		pushed := pushedDL + pushedUP

		microsleep = AdjustMicroSleep(microsleep, pushed, todo, dvlastRunTook, minsleep, maxsleep)
		if cfg.opt.DebugWorker {
			dlog(cfg.opt.DebugWorker, " | [DV] lastRunTook='%d ms' '%v microsleep=%v fetchedtoUP=%d fetchedtoDL=%d backlogDL=%d refillUP=%d", dvlastRunTook.Milliseconds(), dvlastRunTook, microsleep, fetchedtoUP, fetchedtoDL, backlogDL, refillUP)
		}
		time.Sleep(time.Duration((dvlastRunTook.Milliseconds() * 2)) + (time.Duration(microsleep) * time.Millisecond))

		if deadWorkersDeadline <= 0 {
			dlog(always, "GoWorkDivider: no booted workers for too long, exiting")
			closeCase = "noworkers"
			break forever
		}
		s.mux.Lock()
		dlog(cfg.opt.DebugWorker, "GoWorkDivider: booted workers: %d", s.bootedWorkers)

		if s.bootedWorkers == 0 {
			deadWorkersDeadline--
			dlog(always, "GoWorkDivider: no booted workers, deadWorkersDeadline=%d", deadWorkersDeadline)
		} else {
			deadWorkersDeadline = 9 // reset
		}
		s.mux.Unlock()

		// uint64
		var segm, allOk, done, fails, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ uint64

		// Tnodl, Tnoup counts segments that not have been downloaded
		// or uploaded because provider has config flat NoDownload or NoUpload set
		var Tnodl, Tnoup uint64

		startLoop = time.Now()

	forsegmentList:
		for _, item := range s.segmentList {
			item.mux.PrintStatus(false)

			if deadcounter <= 0 {
				dlog(always, "GoWorkDivider: counters not increasing for too long, exiting")
				closeCase = "noProgress"
				break forever
			}

			item.mux.RLock() // RLOCKS HERE #824d
			if item.flagCache {
				cached++
			}

			if item.checkedOn != providersCnt {
				// ignore item, will retry next run
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				continue forsegmentList
			}
			checked++

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
			if item.fails >= 3 {
				fails++
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
				// this segment is on all providers or should be ...
				// we add provider.pid to item.availableOn after re-upload
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

			pushedUp, nNoUp, nInRetry, a1err := s.pushUP(allowUp, item)
			if a1err != nil {
				dlog(always, "ERROR pushUP err='%v' (seg.Id='%s')", a1err, item.segment.Id)
				continue forsegmentList
			}
			if pushedUp {
				inup++
				dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDup seg.Id='%s' pushedUp=%t inup=%d", item.segment.Id, pushedUp, inup)
			}
			noup += nNoUp
			inretry += nInRetry

			if !pushedUp && allowDl {
				pushedDl, nNoDl, b1err := s.pushDL(allowDl, item)
				if b1err != nil {
					dlog(always, "ERROR pushDL err='%v' (seg.Id='%s')", b1err, item.segment.Id)
					continue forsegmentList
				}
				nodl += nNoDl
				Tnodl += uint64(len(item.ignoreDlOn))
				if pushedDl {
					indl++
					//if cfg.opt.BUG {
					dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDdl seg.Id='%s' pushedDl=%t indl=%d", item.segment.Id, pushedDl, indl)
					//}
				}
			} // end pushDL

		} // end for forsegmentList
		//dlog(cfg.opt.DebugWorker, " | [DV] lastRunTook='%d ms' '%v", lastRunTook.Milliseconds(), lastRunTook)
		s.mux.RLock()
		checkFeedDone := s.checkFeedDone // read value of s.checkDone
		segcheckdone := s.segcheckdone   // read value of s.segcheckdone
		s.mux.RUnlock()

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

			if segm_perc > capture_segm {
				capture_segm = segm_perc
				deadcounter = 10 // reset deadcounter
			} else if segm_perc < 100 && segm_perc == capture_segm {
				deadcounter--
				dlog(always, "GoWorkDivider: segm_perc not increasing, deadcounter=%d", deadcounter)

			} else if segcheckdone && done_perc > capture_done {
				capture_done = done_perc
				deadcounter = 10 // reset deadcounter
			} else if segcheckdone && done_perc < 100 && done_perc == capture_done {
				deadcounter--
				dlog(always, "GoWorkDivider: done_perc not increasing, deadcounter=%d", deadcounter)

			} else if segcheckdone && isdl_perc > capture_isdl {
				capture_isdl = isdl_perc
				deadcounter = 10 // reset deadcounter
			} else if segcheckdone && isdl_perc < 100 && isdl_perc == capture_isdl {
				deadcounter--
				dlog(always, "GoWorkDivider: isdl_perc not increasing, deadcounter=%d", deadcounter)

			} else if segcheckdone && isup_perc > capture_isup {
				capture_isup = isup_perc
				deadcounter = 10 // reset deadcounter
			} else if segcheckdone && isup_perc < 100 && isup_perc == capture_isup {
				deadcounter--
				dlog(always, "GoWorkDivider: isup_perc not increasing, deadcounter=%d", deadcounter)
			}

			//log00 = fmt.Sprintf(" TODO: %d ", todo)

			if (cfg.opt.CheckFirst || cfg.opt.CheckOnly) || (checked > 0 && checked != segm && checked != done) {
				if check_perc <= 99 {
					log01 = fmt.Sprintf(" | STAT:[%03.1f%%] (%"+s.D+"d/%"+s.D+"d)", check_perc, checked, todo)
				} else if check_perc >= 99 && check_perc < 100 {
					log01 = fmt.Sprintf(" | STAT:[%03.3f%%] (%"+s.D+"d/%"+s.D+"d)", check_perc, checked, todo)
				} else {
					// if we have checked all segments, we print it as done
					log01 = fmt.Sprintf(" | STAT:[done] (%"+s.D+"d/%"+s.D+"d)", checked, todo)
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
			if fails > 0 || inretry > 0 {
				log07 = fmt.Sprintf(" | ERRS:[%d] (inRetry: %"+s.D+"d)", fails, inretry)
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

			} else if cfg.opt.DebugWorker || cfg.opt.Debug {
				memdata := memlim.ViewData()
				if cfg.opt.BUG {
					log11 = fmt.Sprintf("\n memdata=%d:\n memdata='\n%#v\n' \n | ",
						len(memdata), memdata)
				} else {
					log11 = fmt.Sprintf(" | memdata=%d: ", len(memdata))
				}
				log11 = log11 + fmt.Sprintf("MEM:%d/%d [Cr=%d|Dr=%d|Ur=%d] ",
					used_slots, max_slots, CNTc, CNTd, CNTu)

				log11 = log11 + fmt.Sprintf("[MemLockWait=%d|TOTAL_MemLock=%d|TOTAL_MemReturned=%d|MemOpen=%d] ",
					GCounter.GetValue("MemLockWait"), GCounter.GetValue("TOTAL_MemLock"), GCounter.GetValue("TOTAL_MemReturned"),
					GCounter.GetValue("TOTAL_MemLock")-GCounter.GetValue("TOTAL_MemReturned"))

				log11 = log11 + fmt.Sprintf("(indl=%d|inup=%d) CP:(parked=%d|get=%d|new=%d|dis=%d|open=%d|wait=%d)",
					indl, inup, GCounter.GetValue("TOTAL_ParkedConns"), GCounter.GetValue("TOTAL_GetConns"), GCounter.GetValue("TOTAL_NewConns"), GCounter.GetValue("TOTAL_DisConns"), GCounter.GetValue("TOTAL_NewConns")-GCounter.GetValue("TOTAL_DisConns"), GCounter.GetValue("WaitingGetConns"))

			}

			// build stats log string ...
			logstring = log00 + log01 + log02 + log03 + log04 + log05 + log06 + log07 + log08 + log09 + log10 + log11 + log99
			if cfg.opt.Verbose && cfg.opt.PrintStats >= 0 && logstring != "" && nextLogPrint < time.Now().Unix() {
				nextLogPrint = time.Now().Unix() + cfg.opt.PrintStats
				dlog(always, "%s", logstring)
			}
		} // print some stats ends here

		if !segcheckdone && checked == todo && checkFeedDone {
			s.mux.Lock()
			s.segmentCheckEndTime = time.Now()
			took := time.Since(s.segmentCheckStartTime)
			s.segmentCheckTook = took
			s.segcheckdone = true
			s.mux.Unlock()
			dlog(always, " | Segment Check Done: took='%.1f sec'", took.Seconds())
		}

		// continue as long as any of this triggers because stuff is still in queues and processing
		if checked != todo || inup > 0 || indl > 0 || inretry > 0 || inyenc > 0 || dlQ > 0 || upQ > 0 || yeQ > 0 {
			dlog(cfg.opt.DebugWorker, "\n[DV] continue [ allOk=%d | checked=%d !=? todo=%d | done=%d | TupQ=%d !=? isup=%d || TdlQ=%d !=? isdl=%d || inup=%d > 0? || indl=%d > 0? || inretry=%d > 0? || dlQ=%d > 0? || upQ=%d > 0? || yeQ=%d > 0? ]\n",
				allOk, checked, todo, done, TupQ, isup, TdlQ, isdl, inup, indl, inretry, dlQ, upQ, yeQ)
			continue forever
		}

		if closeWait <= 0 {
			if closeCase != "" {
				dlog(always, " | [DV] closeCase='%s'", closeCase)
			}
			dlog(cfg.opt.DebugWorker, " | [DV] quit all 0? inup=%d indl=%d inretry=%d inyenc=%d dlQ=%d upQ=%d yeQ=%d", inup, indl, inretry, inyenc, dlQ, upQ, yeQ)
			break forever
		}

		// figure out if all jobs are done
		globalmux.RLock()
		closeCase0 := (done == todo || fails == todo || fails+done == todo)
		closeCase1 := (cfg.opt.CheckOnly)
		closeCase2 := (cacheON && (GCounter.GetValue("postProviders") == 0 && cached == todo))
		closeCase3 := (cacheON && (dead+cached == todo && dead+isup == todo))
		closeCase4 := (isup == todo)
		closeCase5 := (dead+isup == todo || dead+isup+fails == todo)
		closeCase6 := (dead+done == todo || dead+done+fails == todo)
		closeCase7 := false // placeholder
		globalmux.RUnlock()

		if closeCase0 {
			closeWait--
			closeCase = closeCase + "|Debug#0"

		} else if closeCase1 {
			closeWait--
			closeCase = closeCase + "|Debug#1"

		} else if closeCase2 {
			closeWait--
			closeCase = closeCase + "|Debug#2"

		} else if closeCase3 {
			closeWait--
			closeCase = closeCase + "|Debug#3"

		} else if closeCase4 {
			closeWait--
			closeCase = closeCase + "|Debug#4"

		} else if closeCase5 {
			closeWait--
			closeCase = closeCase + "|Debug#5"

		} else if closeCase6 {
			closeWait--
			closeCase = closeCase + "|Debug#6"

		} else if closeCase7 {
			closeWait--
			closeCase = closeCase + "|Debug#7"

		} else {
			if closeWait >= 10 {
				errmsg := fmt.Sprintf("WARN [DV] hit impossible closeCase ... kill in %d more loops", 15-closeWait)
				errstr := GetImpossibleCloseCaseVariablesToString(segm, allOk, done, dead, isdl, indl, inup, isup, checked, dmca, nodl, noup, cached, inretry, inyenc, isyenc, dlQ, upQ, yeQ)
				errmsg = errmsg + "\n" + errstr
				dlog(always, "%s", errmsg)
			}
			closeWait++
			if closeWait >= 15 {
				dlog(always, "... force quit ...")
				return
			}
			closeCase = ""
		}
		if closeCase != "" {
			dlog(always, " | DV closeCase='%s' closeWait=%d", closeCase, closeWait)
		}
	} // end forever

	dlog(cfg.opt.DebugWorker, "%s\n   WorkDivider quit: closeCase='%s'", logstring, closeCase)

} // end func WorkDivider
