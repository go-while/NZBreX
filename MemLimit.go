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
	"sync/atomic"

	"github.com/go-while/go-loggedrwmutex"
)

type MemLimiter struct {
	mem_max int
	waiting atomic.Int64 // callers inside MemLockWait waiting for a slot; atomic so MemAvail needs no lock
	memchan chan struct{}
	// memdata holds the items that own a slot. It is the only record of who owns
	// one: it changes together with item.memlocked, and both only under mux.
	memdata map[*segmentChanItem]bool
	// pending holds the items still waiting for a slot. A second MemLockWait on
	// such an item waits for the first one instead of taking a second slot.
	pending map[*segmentChanItem]chan struct{}
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
		pending: make(map[*segmentChanItem]chan struct{}),
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
		data = append(data, item.segment.ID)
	}
	m.mux.RUnlock()
	return
} // end func memlim.ViewData

// MemAvail reports whether fewer than mem_max callers are waiting for a slot.
// pushDL uses it to bound the downloads queued behind a full limiter. It does
// not mean a slot is free: queued downloads wait in MemLockWait, holding no
// memory, so they take a slot the moment one comes back. Gating on free slots
// instead (plan 3.8) cost ~25% throughput with -mem 4: every freed slot then
// waited for the next divider pass before a download was queued for it.
//
// It takes no lock: pushDL calls it while holding item.mux.
func (m *MemLimiter) MemAvail() bool {
	return m.waiting.Load() < int64(m.mem_max)
}

// MemLockWait takes a memory slot for item. It waits until a slot is free or
// until stop is closed (a nil stop waits forever). It returns false if it gave
// up because of stop: the item then holds no slot and must not be downloaded.
// It returns true if the item holds a slot (also when it already held one).
//
// Lock order: m.mux before item.mux. Callers must not hold item.mux.
func (m *MemLimiter) MemLockWait(item *segmentChanItem, who string, stop <-chan struct{}) bool {
	GCounter.Incr("MemLockWait")
	defer GCounter.Decr("MemLockWait")

	for {
		m.mux.Lock()
		if m.memdata[item] {
			m.mux.Unlock()
			dlog(cfg.opt.DebugMemlim, "MemLockWait called on already memlocked item seg.Id='%s' who='%s'", item.segment.ID, who)
			return true
		}
		other := m.pending[item]
		if other == nil {
			break // we wait for the slot, still holding m.mux
		}
		m.mux.Unlock()
		// someone else is already waiting for a slot for this item: wait for them, then look again
		dlog(always, "MemLockWait: seg.Id='%s' is already waiting for a slot, who='%s' waits for it", item.segment.ID, who)
		select {
		case <-other:
		case <-stop:
			return false
		}
	}
	done := make(chan struct{})
	m.pending[item] = done
	waiting := m.waiting.Add(1)
	dlog(cfg.opt.DebugMemlim, "MemLockWait avail=%d/%d m.waiting=%d who='%s'", len(m.memchan), m.mem_max, waiting, who)
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

	m.mux.Lock()
	m.waiting.Add(-1)
	delete(m.pending, item)
	if got {
		m.memdata[item] = true
		item.mux.Lock()
		item.memlocked = 1
		item.mux.Unlock()
	}
	close(done)
	m.mux.Unlock()

	if !got {
		GCounter.Incr("TOTAL_MemLockAborted")
		dlog(cfg.opt.DebugMemlim, "MemLockWait aborted by stop seg.Id='%s' who='%s'", item.segment.ID, who)
		return false
	}
	GCounter.Incr("TOTAL_MemLock")
	dlog(cfg.opt.DebugMemlim, "NewMemLock gotSLOT seg.Id='%s' who='%s'", item.segment.ID, who)
	return true
} // end func memlim.MemLockWait

// MemReturn gives the item's slot back. It is called after the item has been
// uploaded, written to cache, or retired. Calling it for an item that holds no
// slot (or calling it twice) is logged and does nothing.
func (m *MemLimiter) MemReturn(who string, item *segmentChanItem) {
	m.memReturn(who, item, false)
}

// MemReturnIfHeld is MemReturn for paths that may both retire an item (the
// download worker, the cache writer, the divider): no log if the slot is gone.
func (m *MemLimiter) MemReturnIfHeld(who string, item *segmentChanItem) {
	m.memReturn(who, item, true)
}

func (m *MemLimiter) memReturn(who string, item *segmentChanItem, quiet bool) {
	m.mux.Lock()
	if !m.memdata[item] {
		m.mux.Unlock()
		dlog(!quiet, "MemReturn called on non-memlocked item seg.Id='%s' who='%s'", item.segment.ID, who)
		return // not memlocked, nothing to do
	}
	delete(m.memdata, item)
	item.mux.Lock()
	item.memlocked = 0
	item.mux.Unlock()
	m.mux.Unlock()

	// return the slot
	select {
	case m.memchan <- struct{}{}: // return mem slot into chan
		GCounter.Incr("TOTAL_MemReturned")
	default:
		// only one owner per slot, so the chan can't be full here
		dlog(always, "BUG: MemReturn: slot chan is full seg.Id='%s' who='%s'", item.segment.ID, who)
	}
	dlog(cfg.opt.DebugMemlim, "MemReturned seg.Id='%s' who='%s'", item.segment.ID, who)
} // end func memlim.memReturn
