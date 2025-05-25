package main

import (
	"log"
	"sync"
)

// Counter_uint64_uint64 is safe to use concurrently.
type Counter_uint64 struct {
	m   map[string]uint64
	mux sync.RWMutex
}

func NewCounter() *Counter_uint64 {
	return &Counter_uint64{m: make(map[string]uint64)}
} // end func NewCounter

func (c *Counter_uint64) init(k string) (retbool bool) {
	c.mux.Lock()
	if _, hasKey := c.m[k]; !hasKey {
		c.m[k] = 0
		retbool = true
	}
	c.mux.Unlock()
	return
} // end func Counter.init

func (c *Counter_uint64) reset(k string) {
	c.mux.Lock()
	c.m[k] = 0
	c.mux.Unlock()
} // end func Counter.reset

func (c *Counter_uint64) incr(k string) uint64 {
	c.mux.Lock()
	c.m[k] += 1
	retval := c.m[k]
	c.mux.Unlock()
	/*
		if k == "yencQueueCnt" {
			log.Printf("DEBUG counter.incr k=yencQueueCnt=%d", retval)
		}
	*/
	return retval
} // end func Counter.incrCounter

func (c *Counter_uint64) incrMax(k string, vmax uint64) (bool, uint64) {
	var retval uint64
	c.mux.Lock()
	if c.m[k] < vmax {
		c.m[k] += 1
		retval = c.m[k]
	}
	c.mux.Unlock()
	if retval > 0 {
		return true, retval
	}
	return false, 0
} // end func Counter.incrMax

func (c *Counter_uint64) decr(k string) uint64 {
	var retval uint64
	c.mux.Lock()
	if c.m[k] > 0 {
		c.m[k] -= 1
		retval = c.m[k]
		if c.m[k] == 0 {
			delete(c.m, k)
		}
	} else {
		log.Printf("ERROR in Counter_uint64.decr: key='%s' is already 0!", k)
	}
	c.mux.Unlock()
	/*
		if k == "yencQueueCnt" {
			log.Printf("DEBUG counter.decr k=yencQueueCnt=%d", retval)
		}
	*/
	return retval
} // end func Counter.decr

func (c *Counter_uint64) add(k string, v uint64) {
	c.mux.Lock()
	c.m[k] += v
	c.mux.Unlock()
} // end func Counter.add

func (c *Counter_uint64) sub(k string, v uint64) {
	c.mux.Lock()
	if c.m[k] > 0 {
		c.m[k] -= v
		if c.m[k] == 0 {
			delete(c.m, k)
		}
	} else {
		log.Printf("ERROR in Counter_uint64.sub: key='%s' value=%d is already 0!", k, v)
	}
	c.mux.Unlock()
} // end func Counter.sub

func (c *Counter_uint64) get(k string) uint64 {
	c.mux.RLock()
	retval := c.m[k]
	c.mux.RUnlock()
	return retval
} // end func Counter.get

func (c *Counter_uint64) getReset(k string) uint64 {
	c.mux.Lock()
	retval := c.m[k]
	c.m[k] = 0
	c.mux.Unlock()
	return retval
} // end func Counter.getReset

func (c *Counter_uint64) set(k string, v uint64) {
	c.mux.Lock()
	c.m[k] = v
	c.mux.Unlock()
} // end func Counter.setCounter

func (c *Counter_uint64) delete(k string) {
	c.mux.Lock()
	delete(c.m, k)
	c.mux.Unlock()
} // end func Counter.delCounter

func (c *Counter_uint64) resetAll(reset bool) {
	if !reset {
		return
	}
	c.mux.Lock()
	c.m = make(map[string]uint64)
	c.mux.Unlock()
} // end func Counter.resetAll
