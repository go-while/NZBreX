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
	"sync"
	"time"
)

type MemLimiter struct {
	mem_max int
	waiting int
	memchan chan struct{}
	memdata map[*segmentChanItem]bool
	mux     sync.RWMutex
}

func NewMemLimiter(value int) *MemLimiter {
	if value <= 0 {
		value = 1 // can't have 0 objects in ram...
	}
	memlim := &MemLimiter{
		memchan: make(chan struct{}, value),
		memdata: make(map[*segmentChanItem]bool, value),
		mem_max: value,
	}
	for i := 1; i <= value; i++ {
		// fills chan with empty structs
		//   so workers can suck here
		//     to get a slot out and refill when done
		memlim.memchan <- struct{}{}
	}
	if cfg.opt.Debug {
		log.Printf("NewMemLimiter: max=%d avail=%d", value, len(memlim.memchan))
	}
	return memlim
} // end func NewMemLimiter

func (m *MemLimiter) SetMaxMem(newmax int) {
	if newmax <= 0 {
		newmax = 1 // can't have 0 objects in ram...
	}
	m.mux.Lock()
	m.mem_max = newmax
	m.mux.Unlock()
} // end func SetMaxMem

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

func (m *MemLimiter) MemCheckWait(who string, item *segmentChanItem) {
	if cfg.opt.Debug {
		GCounter.Incr("TOTAL_MemCheckWait")
	}

	m.mux.Lock()
	if cfg.opt.Debug && m.waiting > 0 {
		log.Printf("MemCheckWait WAIT avail=%d/%d m.waiting=%d who='%s'", len(m.memchan), m.mem_max, m.waiting, who)
	}
	m.waiting++
	m.mux.Unlock()

	for {
		m.mux.Lock()
		if !m.memdata[item] {
			m.memdata[item] = true // flag as in mem
			m.mux.Unlock()
			break
		}
		m.mux.Unlock()
		time.Sleep(time.Second) // infinite wait for memlim
		log.Printf("ERROR! MemLimit tried to lock an item already in mem! seg.Id='%s'", item.segment.Id)
	} // end for waithere

	<-m.memchan // infinite wait to get a slot from chan

	m.mux.Lock()
	m.waiting--
	m.mux.Unlock()

	if cfg.opt.Debug {
		log.Printf("NewMemLock seg.Id='%s'", item.segment.Id)
	}
	//log.Printf("MemCheckWait SLOT chan=%d/%d", len(m.memchan), m.mem_max)
} // end func memlim.MemCheckWait

func (m *MemLimiter) MemReturn(who string, item *segmentChanItem) {
	if cfg.opt.Debug {
		GCounter.Incr("WAIT_MemReturn")
	}
	select {
	case m.memchan <- struct{}{}: // return mem slot into chan
		//pass
	default:
		// wtf chan is full?? that's a bug!
		log.Printf("ERROR MemReturn chan is full who='%s'", who)
	}
	m.mux.Lock()
	delete(m.memdata, item)
	m.mux.Unlock()
	if cfg.opt.Debug {
		GCounter.Decr("WAIT_MemReturn")
		GCounter.Incr("TOTAL_MemReturned")
	}
	//log.Printf("MemReturned who='%s'", who)
} // end func memlim.MemReturn
