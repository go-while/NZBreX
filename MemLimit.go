package main

/*
 * MemLimiter:
 *    Limits amount of objects in ram. does not count any bytes!
 *
 * in this app:
 *     aquires slot before downloading or reading from cache
 *     releases slot after upload
 *       or, if no provider has upload capabilities
 *         releases after writing to cache, if cache is enabled
 *
 * var memlim *MemLimiter
 * memlim = NewMemLimiter(100)
 */

import (
	"log"
	"os"

	"github.com/go-while/go-loggedrwmutex"
)

type MemLimiter struct {
	mem_max int
	waiting int
	memchan chan struct{}
	memdata map[*segmentChanItem]bool
	//mux     sync.RWMutex
	mux *loggedrwmutex.LoggedSyncRWMutex // debug mutex
}

func NewMemLimiter(value int) *MemLimiter {
	if value <= 0 {
		value = 2 // can't have 0 objects in ram...
	}
	memlim := &MemLimiter{
		memchan: make(chan struct{}, value),
		memdata: make(map[*segmentChanItem]bool, value),
		mem_max: value,
		mux:     &loggedrwmutex.LoggedSyncRWMutex{Name: "MemLimiter"},
	}
	for i := 1; i <= value; i++ {
		// fills chan with empty structs
		//   so workers can suck here
		//     to get a slot out and refill when done
		memlim.memchan <- struct{}{}
	}
	dlog(always, "NewMemLimiter: max=%d avail=%d", value, len(memlim.memchan))
	return memlim
} // end func NewMemLimiter

func (m *MemLimiter) Usage() (int, int) {
	used_slots := m.mem_max - len(m.memchan)
	return used_slots, m.mem_max
} // end func memlim.InUse

func (m *MemLimiter) ViewData() (data []string) {
	m.mux.RLock()
	for item := range m.memdata {
		data = append(data, item.segment.Id)
	}
	m.mux.RUnlock()
	return
} // end func memlim.ViewData

func (m *MemLimiter) MemAvail() (retbool bool) {
	m.mux.RLock()
	retbool = (m.waiting < m.mem_max)
	m.mux.RUnlock()
	return
}

// MemLockWait takes a memory slot for item. It waits until a slot is free or
// until stop is closed (a nil stop waits forever). It returns false if it gave
// up because of stop: the item then holds no slot and must not be downloaded.
// It returns true if the item holds a slot (also when it already held one).
func (m *MemLimiter) MemLockWait(item *segmentChanItem, who string, stop <-chan struct{}) bool {
	item.mux.Lock()
	if item.memlocked > 0 {
		item.mux.Unlock()
		dlog(always, "MemLockWait called on already memlocked item seg.Id='%s' who='%s'", item.segment.Id, who)
		return true
	}
	item.mux.Unlock()

	GCounter.Incr("MemLockWait")
	defer GCounter.Decr("MemLockWait")

	m.mux.Lock()
	if m.memdata[item] {
		dlog(always, "ERROR ! MemLimit tried to lock an item already in mem! seg.Id='%s' who='%s'", item.segment.Id, who)
		os.Exit(1) // this is a bug! we should never try to lock an item already in mem!
	}
	m.memdata[item] = true // flag as in mem
	dlog(cfg.opt.DebugMemlim, "MemLockWait avail=%d/%d m.waiting=%d who='%s'", len(m.memchan), m.mem_max, m.waiting, who)
	m.waiting++
	m.mux.Unlock()

	// take a free slot right away if there is one, so a closed stop never wins over a free slot
	got := false
	select {
	case <-m.memchan:
		got = true
	default:
		select {
		case <-m.memchan:
			got = true
		case <-stop:
		}
	}

	if !got {
		// roll back, or the next MemLockWait on this item hits the os.Exit above
		m.mux.Lock()
		m.waiting--
		delete(m.memdata, item)
		m.mux.Unlock()
		GCounter.Incr("TOTAL_MemLockAborted")
		dlog(cfg.opt.DebugMemlim, "MemLockWait aborted by stop seg.Id='%s' who='%s'", item.segment.Id, who)
		return false
	}

	item.mux.Lock()
	item.memlocked++
	item.mux.Unlock()
	GCounter.Incr("TOTAL_MemLock")

	m.mux.Lock()
	m.waiting--
	dlog(cfg.opt.DebugMemlim, "NewMemLock gotSLOT seg.Id='%s' m.waiting=%d who='%s'", item.segment.Id, m.waiting, who)
	m.mux.Unlock()
	return true
} // end func memlim.MemLockWait

// MemReturn removes the item from mem and returns the slot to the chan
// it is called after the item has been uploaded or written to cache
// it is also called if no upload and is written to cache
func (m *MemLimiter) MemReturn(who string, item *segmentChanItem) {
	//dlog(cfg.opt.DebugMemlim, "MemReturn free seg.Id='%s' who='%s'", item.segment.Id, who)
	item.mux.RLock()
	if item.memlocked == 0 {
		dlog(always, "MemReturn called on non-memlocked item seg.Id='%s' who='%s'", item.segment.Id, who)
		item.mux.RUnlock()
		return // not memlocked, nothing to do
	}
	item.mux.RUnlock()

	// remove map entry from mem
	m.mux.Lock()
	delete(m.memdata, item)
	m.mux.Unlock()

	// return the slot
	select {
	case m.memchan <- struct{}{}: // return mem slot into chan
		GCounter.Incr("TOTAL_MemReturned")
	default:
		// wtf chan is full?? that's a bug!
		// this is a bug! we should never return a slot to a full chan!
		log.Fatalf("ERROR on MemReturn chan is full seg.Id='%s' who='%s'", item.segment.Id, who)
	}
	item.mux.Lock()
	item.memlocked--
	item.mux.Unlock()
	dlog(cfg.opt.DebugMemlim, "MemReturned seg.Id='%s' who='%s'", item.segment.Id, who)
} // end func memlim.MemReturn
