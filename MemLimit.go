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
	memdata []*segmentChanItem
	mux     sync.RWMutex
}

func NewMemLimiter(value int) *MemLimiter {
	if value <= 0 {
		value = 1 // can't have 0 objects in ram...
	}
	memlim := &MemLimiter{
		memchan: make(chan struct{}, value),
		//memdata: make(map[string]bool, value),
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

func (m *MemLimiter) Usage() (int, int) {
	used_slots := m.mem_max - len(m.memchan)
	return used_slots, m.mem_max
} // end func memlim.InUse

func (m *MemLimiter) ViewData() (data []string) {
	m.mux.RLock()
	for _, inmem := range m.memdata {
		data = append(data, inmem.segment.Id)
	}
	m.mux.RUnlock()
	return
} // end func memlim.ViewData

func (m *MemLimiter) MemCheckWait(who string, item *segmentChanItem) {
	if cfg.opt.Debug {
		Counter.incr("TOTAL_MemCheckWait")
	}

	m.mux.Lock()
	m.waiting++
	m.mux.Unlock()
	/*
		if m.waiting > 0 {
			log.Printf("MemCheckWait WAIT avail=%d/%d m.waiting=%d who='%s'", len(m.memchan), m.mem_max, m.waiting, who)
		}
	*/

	waitHere := false
	for {
		m.mux.RLock()
		for _, inmem := range m.memdata {
			if inmem.segment.Id == item.segment.Id {
				// segment is alread locked inmem?!
				waitHere = true
				break
			}
		} // end for inmem
		m.mux.RUnlock()
		if !waitHere {
			break
		}
		time.Sleep(time.Second)
		log.Printf("WAIT! already inmem seg.Id='%s'", item.segment.Id)
		waitHere = false // reset for next run
	} // end for waithere

	<-m.memchan // gets a slot from chan

	m.mux.Lock()
	m.waiting--
	m.memdata = append(m.memdata, item)
	if cfg.opt.Debug {
		log.Printf("NewMemLock seg.Id='%s'", item.segment.Id)
	}
	m.mux.Unlock()
	//log.Printf("MemCheckWait SLOT chan=%d/%d", len(m.memchan), m.mem_max)
} // end func memlim.MemCheckWait

func (m *MemLimiter) MemReturn(who string, item *segmentChanItem) {
	if cfg.opt.Debug {
		Counter.incr("WAIT_MemReturn")
	}
	m.mux.Lock()
	var newdata []*segmentChanItem
	for _, inmem := range m.memdata {
		if inmem.segment.Id != item.segment.Id {
			newdata = append(newdata, inmem)
		} else {
			if cfg.opt.Debug {
				log.Printf("MemRet clear seg.Id='%s inmen='%s'", item.segment.Id, inmem.segment.Id)
			}
		}
	}
	m.memdata = newdata
	m.mux.Unlock()
	m.memchan <- struct{}{}
	if cfg.opt.Debug {
		Counter.decr("WAIT_MemReturn")
		Counter.incr("TOTAL_MemReturned")
	}
	//log.Printf("MemReturned who='%s'", who)
} // end func memlim.MemReturn
