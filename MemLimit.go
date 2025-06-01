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
	"github.com/go-while/go-loggedrwmutex"
	//"sync"
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
		value = 1 // can't have 0 objects in ram...
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
	retbool = (m.waiting <= m.mem_max)
	m.mux.RUnlock()
	return
}

func (m *MemLimiter) MemLockWait(item *segmentChanItem, who string) {

	GCounter.Incr("TOTAL_MemLockWait")
	GCounter.Incr("MemLockWait")
	defer GCounter.Decr("MemLockWait")

	m.mux.Lock()
	if m.memdata[item] {
		m.mux.Unlock()
		dlog(always, "ERROR ! MemLimit tried to lock an item already in mem! seg.Id='%s' who='%s'", item.segment.Id, who)
		return
	}
	m.memdata[item] = true // flag as in mem
	//if cfg.opt.Debug && m.waiting > 0 {
	dlog(cfg.opt.DebugMemlim, "MemLockWait avail=%d/%d m.waiting=%d who='%s'", len(m.memchan), m.mem_max, m.waiting, who)
	//}
	m.waiting++
	m.mux.Unlock()
	/*
		for {
			m.mux.Lock()
			if !m.memdata[item] {
				m.memdata[item] = true // flag as in mem
				m.mux.Unlock()
				break
			}
			m.mux.Unlock()
			time.Sleep(time.Second) // infinite wait for memlim. if this trigger something is wrong!

		} // end for waithere
	*/
	<-m.memchan // infinite wait to get a slot from chan

	m.mux.Lock()
	m.waiting--
	dlog(cfg.opt.DebugMemlim, "NewMemLock seg.Id='%s' m.waiting=%d who='%s'", item.segment.Id, m.waiting, who)
	m.mux.Unlock()

	dlog(cfg.opt.DebugMemlim, "MemLockWait got SLOT seg.Id='%s' who='%s'", item.segment.Id, who)
} // end func memlim.MemLockWait

func (m *MemLimiter) MemReturn(who string, item *segmentChanItem) {
	dlog(cfg.opt.DebugMemlim, "MemReturn enter seg.Id='%s' who='%s'", item.segment.Id, who)
	GCounter.Incr("WAIT_MemReturn")
	defer GCounter.Decr("WAIT_MemReturn")

	select {
	case m.memchan <- struct{}{}: // return mem slot into chan
		//pass
	default:
		// wtf chan is full?? that's a bug!
		dlog(always, "ERROR on MemReturn chan is full seg.Id='%s' who='%s'", item.segment.Id, who)
		return
	}
	m.mux.Lock()
	delete(m.memdata, item)
	m.mux.Unlock()

	GCounter.Incr("TOTAL_MemReturned")
	dlog(cfg.opt.DebugMemlim, "MemReturned seg.Id='%s' who='%s'", item.segment.Id, who)
} // end func memlim.MemReturn
