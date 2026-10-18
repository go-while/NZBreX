package main

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// GoBootWorkers boots up all workers for a session
func (s *SESSION) GoBootWorkers(waitDivider *sync.WaitGroup, workerWGconnReady *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup, byteSize int64) {
	defer waitDivider.Done() // allows WorkDivider to run when this releases, also on the early returns
	if s == nil {
		dlog(always, "ERROR in GoBootWorkers: session is nil")
		return
	}
	// preBoot/active are guarded by s.mux, like in LaunchSession and closeSession
	s.mux.Lock()
	if s.active || !s.preBoot {
		dlog(always, "ERROR in GoBootWorkers: session %d already booted... s.preBoot=%t s.active=%t", s.sessId, s.preBoot, s.active)
		s.mux.Unlock()
		return
	}
	s.preBoot = false
	s.active = true
	s.mux.Unlock()

	if cacheON && cfg.opt.CheckCacheOnBoot {
		cached := 0
		for _, item := range s.segmentList {
			if cache.CheckCache(item) {
				cached++
				//dlog( "Cached: seg.Id='%s'", item.segment.ID)
			}
		}
		dlog(always, "Cached: %d/%d", cached, len(s.segmentList))
	}

	// -chansize <= 0 sizes the channels to this NZB. Kept local: writing it
	// back to cfg.opt fixed the first job's size for every later job (plan 6.2).
	chanSize := cfg.opt.ChanSize
	if chanSize <= 0 {
		chanSize = len(s.segmentList)
	}

	s.makeSegmentChans(chanSize)

	// loop over the providerList and boot up anonymous workers for each provider
	for _, provider := range s.providerList {

		dlog(cfg.opt.DebugWorker, "GoBootWorkers list=%d check provider='%#v' ", len(s.providerList), provider.Name)

		if !provider.Enabled || provider.MaxConns <= 0 {
			// loadProviderList already filters these; release its Add(1) to keep the count balanced
			dlog(cfg.opt.Verbose, "ignored provider: '%s'", provider.Name)
			workerWGconnReady.Done()
			continue
		}

		// boot up a worker for this provider in an anonymous go routine
		go func(provider *Provider, workerWGconnReady *sync.WaitGroup, waitWorker *sync.WaitGroup, waitPool *sync.WaitGroup) {
			// releases at end of func when provider capabilities have been checked and all GoWorker() booted
			defer workerWGconnReady.Done() // release #1 for every provider in go func
			dlog(cfg.opt.Debug, "GoBootWorkers: Booting Provider '%s' group '%s'", provider.Name, provider.Group)

			dlog(cfg.opt.Verbose, "Connecting to Provider '%s' MaxConns=%d SSL=%t", provider.Name, provider.MaxConns, provider.SSL)

			if !cfg.opt.CheckOnly && !provider.NoUpload {
				dlog(cfg.opt.Debug, "GoBootWorkers: get conn to check capabilities for provider '%s'", provider.Name)

				// get a connection item from the provider's connection pool
				connitem, err := provider.ConnPool.GetConn()
				if err != nil {
					dlog(always, "ERROR Boot Provider '%s' err='%v'", provider.Name, err)
					// Release waitWorker and waitPool counters for all workers that won't be started
					s.checkersExit(provider, provider.MaxConns) // their check routines never start
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

// makeSegmentChans builds the check/downs/reups channel of every provider group
// and starts one feeder per group. It runs before any GoWorker or the divider
// starts, so the maps are complete before anyone reads them and stay read-only
// afterwards (they used to be written by each provider's boot goroutine while
// other groups' workers already read them: fatal concurrent map read and write).
// provider.Group is defaulted to provider.Name in loadProviderList.
func (s *SESSION) makeSegmentChans(chanSize int) {
	check := make(map[string]chan *segmentChanItem)
	downs := make(map[string]chan *segmentChanItem)
	reups := make(map[string]chan *segmentChanItem)
	checkers := make(map[string]*checkerGroup)
	for _, provider := range s.providerList {
		if !provider.Enabled || provider.MaxConns <= 0 {
			continue
		}
		if checkers[provider.Group] == nil {
			checkers[provider.Group] = &checkerGroup{gone: make(chan struct{})}
		}
		checkers[provider.Group].left.Add(int64(provider.MaxConns)) // one check routine per conn
		if check[provider.Group] != nil {
			continue
		}
		dlog(cfg.opt.Debug, "Creating chans for group '%s' chanSize=%d", provider.Group, chanSize)
		check[provider.Group] = make(chan *segmentChanItem, chanSize)
		downs[provider.Group] = make(chan *segmentChanItem, chanSize)
		reups[provider.Group] = make(chan *segmentChanItem, chanSize)
	}
	s.mux.Lock()
	s.segmentChansCheck = check
	s.segmentChansDowns = downs
	s.segmentChansReups = reups
	s.checkers = checkers
	s.mux.Unlock()
	for group, ch := range check {
		go s.feedCheckChan(group, ch, checkers[group].gone) // the feeder owns ch and is the only one closing it
	}
} // end func makeSegmentChans

// checkerGroup counts the check routines of one provider group that have not exited yet,
// including the ones that will never start because their worker failed to connect.
// gone is closed when the count reaches 0: nothing will read the group's check channel again.
type checkerGroup struct {
	left     atomic.Int64
	gone     chan struct{}
	goneOnce sync.Once
}

// exit marks n check routines of the group as finished (or never started).
func (g *checkerGroup) exit(n int) {
	if g == nil || n <= 0 {
		return
	}
	if g.left.Add(-int64(n)) <= 0 {
		g.goneOnce.Do(func() { close(g.gone) })
	}
}

// checkersExit is checkerGroup.exit for a provider's group. The map is read-only once published.
func (s *SESSION) checkersExit(provider *Provider, n int) {
	s.checkers[provider.Group].exit(n)
}

// allCheckersGone reports whether every check routine of every group has exited:
// from then on no item's checkedOn changes anymore.
func (s *SESSION) allCheckersGone() bool {
	for _, g := range s.checkers {
		select {
		case <-g.gone:
		default:
			return false
		}
	}
	return len(s.checkers) > 0
}

// feedCheckChan pushes every segment of the session into the check channel of one group,
// waits for the check routines to empty it, then closes it.
// If all check routines of the group are gone (none could connect, or all died) it stops
// feeding and closes the channel right away: nobody will read it anymore.
func (s *SESSION) feedCheckChan(group string, segmentChanCheck chan *segmentChanItem, checkersGone <-chan struct{}) {
	start := time.Now()
	fed := 0
	abandoned := func() {
		dlog(always, "WARN feeder group '%s': all check routines are gone, %d/%d items fed, %d left in chan never checked here",
			group, fed, len(s.segmentList), len(segmentChanCheck))
	}
feed:
	for _, item := range s.segmentList {
		select {
		case segmentChanCheck <- item: // is allowed to block inside feeder routine
			fed++
		case <-checkersGone:
			abandoned()
			break feed
		case <-s.stopChan: // session stopped (web job stop): feed no more
			break feed
		}
	}
	if fed == len(s.segmentList) {
		tick := time.NewTicker(time.Second)
	waitEmpty:
		for len(segmentChanCheck) > 0 { // wait for the check routines to empty out the chan
			select {
			case <-tick.C:
			case <-checkersGone:
				if len(segmentChanCheck) > 0 {
					abandoned()
				}
				break waitEmpty
			case <-s.stopChan:
				break waitEmpty
			}
		}
		tick.Stop()
	}
	dlog(cfg.opt.Verbose, " | Done feeding items=%d/%d -> segmentChanCheck Group '%s' took='%.0f sec'", fed, len(s.segmentList), group, time.Since(start).Seconds())
	s.mux.Lock()
	// set checkFeedDone only signals that feeding to the channel is done
	// the check routine may still be activly checking!
	s.checkFeedDone = true
	s.mux.Unlock()
	close(segmentChanCheck) // close the chan to signal no more items will come
} // end func feedCheckChan

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
			s.checkersExit(provider, 1) // its check routine never starts
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
		noConn := 0 // items in a row that got no connection
		defer waitWorker.Done()
		defer thisWorkerWG.Done()
		defer s.checkersExit(provider, 1)
		timeOut := time.Now().Add(15 * time.Second)
		lastGood := time.Now()
		tick := time.NewTicker(3 * time.Second) // was a goroutine per routine that never stopped
		defer tick.Stop()
		toChan := tick.C
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
				if s.stopped() {
					continue forGoCheckRoutine // session stopped: drain without checking until the feeder closes the chan
				}
				switch cfg.opt.ByPassSTAT {
				case false:
					code, err := s.checkItem(wid, provider, item, sharedCC)
					item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoCheckRoutine: code=%d", code))
					if err != nil && !provider.isDown() { // recorded as error on this group by checkItem
						dlog(always, "ERROR in GoCheckRoutine err='%v'", err)
					}
					if errors.Is(err, errNoConn) && !provider.isDown() { // down: checkItem fails items at once, keep draining
						if noConn++; noConn >= maxCheckNoConn {
							dlog(always, "WARN CheckRoutine (%d@'%s'): no connection for %d items in a row, quitting", wid, provider.Name, noConn)
							break forGoCheckRoutine
						}
					} else {
						noConn = 0
					}
				case true:
					log.Fatal("you should not be here! Quitting...") // FIXME TODO: remove this fatal error
				}
				processed++
				s.workDone.Add(1)
				// the idle timeout counts from now: one slow item (connect retries) must
				// not make the routine quit while items are waiting in the channel
				timeOut = time.Now().Add(15 * time.Second)
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
		tick := time.NewTicker(3 * time.Second) // was a goroutine per routine that never stopped
		defer tick.Stop()
		toChan := tick.C
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
				if s.stopped() {
					// the divider quit: drop what is still queued instead of working it
					item.mux.Lock()
					item.flaginDL = false
					item.mux.Unlock()
					DecreaseDLQueueCnt()
					continue forGoDownsRoutine
				}
				timeOut = time.Now().Add(15 * time.Second)
				lastGood = time.Now()
				dlog(cfg.opt.DebugWorker, "GoDownsRoutine: (%d@'%s') start seg.Id='%s'", wid, provider.Name, item.segment.ID)

				start := time.Now()
				who := fmt.Sprintf("DR=%d@'%s'#'%s' seg.Id='%s'", wid, provider.Name, provider.Group, item.segment.ID)
				if !memlim.MemLockWait(item, who, s.stopChan) { // gets memlock here
					// session is stopping: give the item back without downloading it
					item.mux.Lock()
					item.flaginDL = false
					item.mux.Unlock()
					DecreaseDLQueueCnt()
					dlog(cfg.opt.DebugWorker, "GoDownsRoutine: (%d@'%s') stop while waiting for a mem slot, skip seg.Id='%s'", wid, provider.Name, item.segment.ID)
					continue forGoDownsRoutine
				}
				dlog(cfg.opt.DebugWorker && cfg.opt.DebugMemlim, "GoDownsRoutine got MemCheckWait who='%s' waited=(%d ms)", who, time.Since(start).Milliseconds())
				errStr := ""
				StartDowns := time.Now()
				code, err := s.GoDownsRoutine(wid, provider, item, sharedCC)
				item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoDownsRoutine: code=%d", code))
				processed++
				s.workDone.Add(1)
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

				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, "GoDownsRoutine: segId='%s' (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", mode, wid, item.segment.ID, provider.Name, time.Since(StartDowns), speedInKBytes)

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
		tick := time.NewTicker(3 * time.Second) // was a goroutine per routine that never stopped
		defer tick.Stop()
		toChan := tick.C
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
					segCR <- nil // refill the nil so others will die too
					break forGoReupsRoutine
				}
				if s.stopped() {
					// the divider quit: drop what is still queued (its mem slot goes back in releaseMemSlots)
					item.mux.Lock()
					item.flaginUP = false
					item.mux.Unlock()
					DecreaseUPQueueCnt()
					continue forGoReupsRoutine
				}
				timeOut = time.Now().Add(15 * time.Second)
				lastGood = time.Now()
				StartReUps := lastGood
				code, err := s.GoReupsRoutine(wid, provider, item, sharedCC)
				item.PrintItemFlags(cfg.opt.DebugFlags, true, fmt.Sprintf("post-GoReupsRoutine: code=%d", code))
				processed++
				s.workDone.Add(1)
				DecreaseUPQueueCnt()
				if err != nil || code == 0 {
					errStr := fmt.Sprintf("ERROR in GoReupsRoutine code='%d' err='%v' no retry", code, err)
					dlog(!provider.isDown() || cfg.opt.DebugUR, "%s", errStr)
					// Keep the mem slot: the item was flagged and is either re-pushed (retry, another
					// group) or retired by the divider (retireNoUploadTarget returns the slot).
					// Returning it here let downloads take every slot while the item, queued again
					// without one, waited in MemLockWait behind items that held them: deadlock.
					// Whatever is still held at session end goes back in releaseMemSlots.
					continue forGoReupsRoutine
				}
				speedInKBytes := (float64(item.size) / 1024) / float64(time.Since(StartReUps).Seconds())
				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, "ReupsRoutine: finished item (wid=%d) seg.Id='%s' @ '%s' took='%v' speedInKBytes=%.2f", wid, item.segment.ID, provider.Name, time.Since(StartReUps), speedInKBytes)

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
const (
	// checkAttempts: a STAT whose connection broke is tried this often (each on a fresh conn).
	// No retry if no connection could be had at all: GetConn already retried connecting.
	checkAttempts = 2
	// maxCheckNoConn: a check routine quits after this many items in a row got no connection.
	// When all of a group's check routines are gone, the feeder and the divider give up on it.
	maxCheckNoConn = 3
)

// checkItem runs GoCheckRoutine and retries a check whose connection broke. A check that
// still fails is recorded as an error on the provider's group, so the item still
// counts as checked there; otherwise the divider waits forever for checked == todo.
func (s *SESSION) checkItem(wid int, provider *Provider, item *segmentChanItem, sharedCC chan *ConnItem) (code int, err error) {
	for attempt := 1; ; attempt++ {
		code, err = s.GoCheckRoutine(wid, provider, item, sharedCC)
		if code != 0 || err == nil {
			return code, err
		}
		if errors.Is(err, errNoConn) || attempt >= checkAttempts || s.stopped() {
			break
		}
		dlog(always, "WARN check seg.Id='%s' @ '%s' attempt %d/%d failed, retry: %v", item.segment.ID, provider.Name, attempt, checkAttempts, err)
	}
	s.markCheckFailed(item, provider)
	return code, err
} // end func checkItem

// markCheckFailed records a check that could not be done on the provider's group:
// the item counts as checked there, but its state there is unknown, so it is neither
// downloaded from nor uploaded to that group (errorOn, ignoreDlOn; not missingOn).
func (s *SESSION) markCheckFailed(item *segmentChanItem, provider *Provider) {
	item.mux.Lock()
	for id, prov := range s.providerList {
		if prov.Group != provider.Group {
			continue
		}
		item.checkedOn++
		item.errorOn[id] = true
		item.ignoreDlOn[id] = true
	}
	item.mux.Unlock()
	// a down provider was logged once by markDown
	dlog(!provider.isDown() || cfg.opt.DebugCR, "WARN check seg.Id='%s' failed on group '%s', marked as error there", item.segment.ID, provider.Group)
} // end func markCheckFailed

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

// maxItemFails: after this many errors an upload target is given up for the item.
const maxItemFails = 3

func (item *segmentChanItem) FlagError(providerId int) {
	log.Printf("FlagError called for providerId=%d on segment.ID='%s' waiting to lock item mutex", providerId, item.segment.ID)
	item.mux.Lock()
	defer item.mux.Unlock()
	//item.ignoreDlOn[providerId] = true
	//item.ignoreUlOn[providerId] = true
	//item.errorOn[providerId] = true
	wasUP := item.flaginUP
	if item.flaginUP {
		if item.fails < maxItemFails-1 {
			// retry this provider in 5s (was .Second(), the 0-59 seconds field, and retryOn was never set: no delay)
			item.retryIn = time.Now().Add(5 * time.Second).Unix()
			item.retryOn[providerId] = true
		}
		item.flaginUP = false
	}

	item.flaginDL = false
	item.fails++
	if wasUP && item.fails >= maxItemFails {
		// stop re-pushing it there: matchThisUP/pushUP don't look at fails.
		// With no other target the divider retires it (notarget).
		item.errorOn[providerId] = true
		dlog(always, "WARN upload of '%s' failed %d times, giving up on providerId=%d", item.segment.ID, item.fails, providerId)
	}
	delete(item.availableOn, providerId)
	dlog(always, "Flagged item error, will not retry '%s' on providerId=%d", item.segment.ID, providerId)
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

// hasUploadTarget reports whether some provider the item is missing on could
// still take an upload for it (same rules as pushUP; a pending retry counts).
// The caller holds item.mux (read or write).
// onAllProviders reports whether every provider has the item: confirmed
// there (availableOn, from STAT/download) or uploaded there by this session
// (uploadedTo). availableOn stays "confirmed": pushDL picks download sources
// from it, and a fresh upload may not be visible yet. Caller holds item.mux.
func (s *SESSION) onAllProviders(item *segmentChanItem) bool {
	for pid := range s.providerList {
		if !item.availableOn[pid] && !item.uploadedTo[pid] {
			return false
		}
	}
	return true
}

func (s *SESSION) hasUploadTarget(item *segmentChanItem) bool {
	for pid, miss := range item.missingOn {
		if !miss {
			continue
		}
		p := s.providerList[pid]
		if p.NoUpload || (!p.capabilities.ihave && !p.capabilities.post) || p.isDown() {
			continue
		}
		if item.ignoreUlOn[pid] || item.uploadedTo[pid] || item.availableOn[pid] ||
			item.unwantedOn[pid] || item.dmcaOn[pid] || item.errorOn[pid] {
			continue
		}
		return true
	}
	return false
} // end func hasUploadTarget

// retireNoUploadTarget returns the mem slot of an item that will never be uploaded,
// and drops its article when nothing else needs it (cache written or off, no yenc output).
func (s *SESSION) retireNoUploadTarget(item *segmentChanItem) {
	item.mux.Lock()
	locked := item.memlocked > 0
	if locked && !cfg.opt.YencWrite && !item.flaginYenc && (!cacheON || item.flagCache) {
		item.article = nil // free memory
	}
	item.mux.Unlock()
	if locked {
		memlim.MemReturn("DV:noUploadTarget", item)
	}
} // end func retireNoUploadTarget

// releaseMemSlots returns the mem slots still held by this session's items.
// memlim is global: a slot leaked by one session would be lost for every later one.
// Call only after all workers have exited.
func (s *SESSION) releaseMemSlots() {
	released := 0
	for _, item := range s.segmentList {
		item.mux.RLock()
		locked := item.memlocked > 0
		item.mux.RUnlock()
		if locked {
			memlim.MemReturn("session end", item)
			released++
		}
	}
	dlog(released > 0, "releaseMemSlots: returned %d mem slots still held at session end", released)
} // end func releaseMemSlots

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
		dlog(cfg.opt.DebugWorker && cfg.opt.BUG, " | [DV-pushDL] (nodl) matchNoDL#1 seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t len(article)=%d", item.segment.ID, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP, len(item.article))
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
			dlog(always, " | [DV-pushDL] (nodl) skip seg.Id='%s' @ #'%s' not availableOn ", item.segment.ID, s.providerList[pid].Group)
			continue providerDl
		}
		if item.ignoreDlOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) ignoreDlOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerDl
		}
		if item.missingOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) item missingOn but should be avail!? seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerDl
		}
		if item.errorOn[pid] {
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] (nodl) item errorOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerDl
		}
		if s.providerList[pid].NoDownload || s.providerList[pid].isDown() {
			nodl++
			item.ignoreDlOn[pid] = true
			continue providerDl
		}
		dlog(cfg.opt.BUG, " | [DV-pushDL] try push chan <- down seg.Id='%s' @ #'%s' item.flaginDL=%t item.flaginDLMEM=%t", item.segment.ID, s.providerList[pid].Group, item.flaginDL, item.flaginDLMEM)
		/* pushDL sends download request only to 1.
		* this one should get it and update availableOn/missingOn list
		 */
		/*
			dst := ""
			if cfg.opt.CheckFirst && !cfg.opt.ByPassSTAT {
				if !checkDone && !item.flaginDLMEM {
					item.flaginDLMEM = true // set flag to release the quaken later
				}
				dlog(cfg.opt.DebugWorker && cfg.opt.BUG, " | [DV-pushDL] flagged as memDL seg.Id='%s' @ #'%s' item.flaginDL=%t item.flaginDLMEM=%t", item.segment.ID, s.providerList[pid].Group, item.flaginDL, item.flaginDLMEM)
				continue providerDl // continue with next provider in item.availableOn

			} else if !cfg.opt.ByPassSTAT {
		*/
		// if we don't check first and did not bypass STAT, we can push items to download queue for provider-group
		IncreaseDLQueueCnt() // before the send: the worker may Decrease as soon as it has the item
		select {
		case s.segmentChansDowns[s.providerList[pid].Group] <- item: // non-blocking
			pushed = true
			item.flaginDL = true
			item.pushedDL++ // mark as pushed to download queue (in pushDL)
			GCounter.Incr("TOTAL_dlQueueCnt")
			dlog(cfg.opt.DebugWorker, " | [DV-pushDL] pushed to dlchan seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			return pushed, 0, nil // return after 1st push!
		default:
			DecreaseDLQueueCnt() // not queued
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
		//dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) nomatch seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t", item.segment.ID, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP)
		return false, 1, 0, nil // not a match, item is already in DL or UP or has article
	}
	dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (tryup) passed matchNoUP seg.Id='%s' item.flagisDL=%t item.flaginDL=%t item.flaginDLMEM=%t item.flaginUP=%t item.flagisUP=%t", item.segment.ID, item.flagisDL, item.flaginDL, item.flaginDLMEM, item.flaginUP, item.flagisUP)
	// looks like we have fetched the segment and did not upload the item yet
providerUp:
	for pid, miss := range item.missingOn {
		if !miss {
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) skip seg.Id='%s' @ #'%s' not missingOn ", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		//s.providerList[pid].mux.RLock() // FIXME TODO #b8bd287b: dynamic capas
		flagNoUp := (s.providerList[pid].NoUpload || (!s.providerList[pid].capabilities.ihave && !s.providerList[pid].capabilities.post) || s.providerList[pid].isDown())
		//s.providerList[pid].mux.RUnlock()
		if flagNoUp {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) flagNoUp seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.ignoreUlOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) ignoreUlOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.uploadedTo[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) uploadedTo seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.availableOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) availableOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.unwantedOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) unwantedOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.dmcaOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) dmcaOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.errorOn[pid] {
			noup++
			dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) errorOn seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			continue providerUp
		}
		if item.retryOn[pid] {
			if item.retryIn > time.Now().Unix() {
				inretry++
				dlog(always, " | [DV-pushUP] (noup) retryOn seg.Id='%s' @ #'%s' retryIn=%d > now=%d, continue", item.segment.ID, s.providerList[pid].Group, item.retryIn, time.Now().Unix())
				continue providerUp
			} else {
				dlog(cfg.opt.DebugWorker, " | [DV-pushUP] (noup) retryOn seg.Id='%s' @ #'%s' retryIn=%d <= now=%d, pass", item.segment.ID, s.providerList[pid].Group, item.retryIn, time.Now().Unix())
				delete(item.retryOn, pid) // remove retryOn flag for this provider
				// pass
			}
		}

		/* push upload request only to 1.
		this one should up it and usenet should distribute it within minutes
		*/
		IncreaseUPQueueCnt() // before the send: the worker may Decrease as soon as it has the item
		select {
		case s.segmentChansReups[s.providerList[pid].Group] <- item: // non-blocking
			// pass
			item.flaginUP = true
			item.pushedUP++ // mark as pushed to upload queue (in pushUP)
			GCounter.Incr("TOTAL_upQueueCnt")
			//GCounter.IncrMax("upQueueCnt", uint64(len(s.segmentList)), "pushUP")
			//GCounter.IncrMax("TOTAL_upQueueCnt", uint64(len(s.segmentList)), "pushUP")
			dlog(cfg.opt.DebugWorker, " | pushUP: in chan seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
			return true, 0, 0, nil // return after 1st push to a group!
		default:
			DecreaseUPQueueCnt() // not queued
			//dlog(cfg.opt.BUG "DEBUG SPAM pushUP: chan is full @ '%s'", s.providerList[pid].Name)
			// chan is full for this group, try next provider-group or not? FIXME TODO REVIEW
			noup++
			return false, noup, inretry, fmt.Errorf("pushUP: chan is full for seg.Id='%s' @ #'%s'", item.segment.ID, s.providerList[pid].Group)
		}
	} // end for providerUp
	return false, noup, inretry, fmt.Errorf("pushUP: no provider found for seg.Id='%s'", item.segment.ID)
} // end func pushUP

type WrappedItem struct {
	wItem *segmentChanItem
	src   string // source of the item, e.g. "WorkDividerChan"
	//dst   string // destination of the item, e.g. "CR" for CheckRoutine, "DR" for DownsRoutine
}

const testing = false // set to true to test the worker loop
// GoWorkDivider is the main worker loop that processes segments
// noProgressTimeout ends a session whose counters (and finished attempts, workDone)
// did not move for this long. Note: with UseNoDeadline conns have no read deadline,
// so a routine blocked on a silent server still holds up waitWorker after this fires.
const noProgressTimeout = 3 * time.Minute

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
	// watchdog: quit if no counter moved for noProgressTimeout (any -verbose setting)
	var lastProgress uint64
	lastProgressAt := time.Now()

forever:
	for {

		dvlastRunTook := time.Since(startLoop)
		pushed := pushedDL + pushedUP

		microsleep = AdjustMicroSleep(microsleep, pushed, todo, dvlastRunTook, minsleep, maxsleep)
		if cfg.opt.DebugWorker {
			dlog(cfg.opt.DebugWorker, " | [DV] lastRunTook='%d ms' '%v microsleep=%v fetchedtoUP=%d fetchedtoDL=%d backlogDL=%d refillUP=%d", dvlastRunTook.Milliseconds(), dvlastRunTook, microsleep, fetchedtoUP, fetchedtoDL, backlogDL, refillUP)
		}
		time.Sleep(time.Duration((dvlastRunTook.Milliseconds() * 2)) + (time.Duration(microsleep) * time.Millisecond))

		if s.stopped() {
			// stop() before the work is done: a web job was stopped. The routines drop
			// what is queued, the feeders stop, LaunchSession tears down as usual.
			dlog(always, "GoWorkDivider: session %d stopped, quitting", s.sessId)
			closeCase = "stopped"
			break forever
		}

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
		var notarget uint64 // checked items no provider can take an upload for (retired)

		// Tnodl, Tnoup counts segments that not have been downloaded
		// or uploaded because provider has config flat NoDownload or NoUpload set
		var Tnodl, Tnoup uint64

		startLoop = time.Now()
		checkersGone := s.allCheckersGone() // before counting: then the counts below are final for checks

	forsegmentList:
		for _, item := range s.segmentList {
			item.mux.PrintStatus(false)

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

			if len(item.missingOn) == 0 && s.onAllProviders(item) {
				// on every provider: confirmed there, or uploaded there by us.
				// (An upload sets uploadedTo, not availableOn, so this used to
				// count uploaded items as "no upload target" and the run only
				// ended through close case #8.)
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
			if allowUp && !s.hasUploadTarget(item) && (item.flagisDL || item.flagCache || !(cacheON || cfg.opt.YencWrite)) {
				// missing only where nothing can upload it (NoUpload, no IHAVE/POST, errors, ...):
				// no upload will ever return its mem slot. Retire it: give the slot back,
				// don't download it, and count it as finished.
				notarget++
				item.mux.RUnlock() // RUNLOCKS HERE #824d
				s.retireNoUploadTarget(item)
				continue forsegmentList
			}
			item.mux.RUnlock() // RUNLOCKS HERE #824d

			pushedUp, nNoUp, nInRetry, a1err := s.pushUP(allowUp, item)
			if a1err != nil {
				dlog(cfg.opt.DebugWorker, "ERROR pushUP err='%v' (seg.Id='%s')", a1err, item.segment.ID)
				continue forsegmentList
			}
			if pushedUp {
				inup++
				dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDup seg.Id='%s' pushedUp=%t inup=%d", item.segment.ID, pushedUp, inup)
			}
			noup += nNoUp
			inretry += nInRetry

			if !pushedUp && allowDl {
				pushedDl, nNoDl, b1err := s.pushDL(allowDl, item)
				if b1err != nil {
					dlog(always, "ERROR pushDL err='%v' (seg.Id='%s')", b1err, item.segment.ID)
					continue forsegmentList
				}
				nodl += nNoDl
				Tnodl += uint64(len(item.ignoreDlOn))
				if pushedDl {
					indl++
					//if cfg.opt.BUG {
					dlog(cfg.opt.DebugWorker, " | [DV] PUSHEDdl seg.Id='%s' pushedDl=%t indl=%d", item.segment.ID, pushedDl, indl)
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

		s.progress.checked.Store(checked)
		s.progress.done.Store(done)
		s.progress.dead.Store(dead)
		s.progress.cached.Store(cached)
		s.progress.downloaded.Store(isdl)
		s.progress.uploaded.Store(isup)
		s.progress.notarget.Store(notarget)

		// workDone also moves on failed attempts: slow failing work (a dead provider) is not stuck
		if progress := checked + isdl + isup + isyenc + cached + done + dead + fails + notarget + s.workDone.Load(); progress != lastProgress {
			lastProgress, lastProgressAt = progress, time.Now()
		} else if time.Since(lastProgressAt) > noProgressTimeout {
			dlog(always, "WARN [DV] no progress for %v, giving up: checked=%d/%d done=%d isdl=%d isup=%d indl=%d inup=%d dlQ=%d upQ=%d",
				noProgressTimeout, checked, todo, done, isdl, isup, indl, inup, dlQ, upQ)
			closeCase = "noProgress"
			break forever
		}

		// checkedOn only changes in check routines: once all are gone, unchecked items stay unchecked.
		// Finish what is in flight, then quit instead of waiting for checked == todo forever.
		if checked != todo && checkersGone && inup == 0 && indl == 0 && inretry == 0 && inyenc == 0 && dlQ == 0 && upQ == 0 && yeQ == 0 {
			dlog(always, "WARN [DV] all check routines are gone, %d/%d items were not checked on every provider: giving up on them", todo-checked, todo)
			closeCase = "checkersGone"
			break forever
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
		// items that can't be uploaded anywhere are finished too
		closeCase8 := notarget > 0 && (dead+done+notarget == todo || dead+done+fails+notarget == todo)
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

		} else if closeCase8 {
			closeWait--
			closeCase = closeCase + "|Debug#8"

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
