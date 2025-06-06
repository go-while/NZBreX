package main

import (
	"fmt"
	"log"
	"sync"
)

// Counter_uint64 is safe to use concurrently.
type Counter_uint64 struct {
	m   map[string]uint64
	mux sync.RWMutex
}

func NewCounter(initsize int) *Counter_uint64 {
	return &Counter_uint64{m: make(map[string]uint64, initsize)}
} // end func NewCounter

func (c *Counter_uint64) KillCounter() {
	c.mux.Lock()
	clear(c.m)
	c.m = nil
	c.mux.Unlock()
} // end func Counter.KillCounter

func (c *Counter_uint64) ClearCounter() {
	c.mux.Lock()
	clear(c.m)
	c.mux.Unlock()
} // end func Counter.ResetCounter

func (c *Counter_uint64) Init(k string) (retbool bool) {
	c.mux.Lock()
	if _, hasKey := c.m[k]; !hasKey {
		c.m[k] = 0
		retbool = true
	}
	c.mux.Unlock()
	return
} // end func Counter.init

func (c *Counter_uint64) ResetKey(k string) {
	c.mux.Lock()
	c.m[k] = 0
	c.mux.Unlock()
} // end func GCounter.reset

func (c *Counter_uint64) Incr(k string) uint64 {
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
} // end func GCounter.IncrCounter

func (c *Counter_uint64) IncrMax(k string, vmax uint64, src string) (retbool bool, retval uint64, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	if c.m[k] < vmax {
		c.m[k] += 1
		retval = c.m[k]
		retbool = true
	} else {
		err = fmt.Errorf("ERROR in Counter_uint64.IncrMax: key='%s' value=%d is already at max=%d! src='%s'", k, c.m[k], vmax, src)
	}
	return
} // end func GCounter.IncrMax

func (c *Counter_uint64) Decr(k string) uint64 {
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
} // end func GCounter.Decr

func (c *Counter_uint64) Add(k string, v uint64) {
	c.mux.Lock()
	c.m[k] += v
	c.mux.Unlock()
} // end func GCounter.Add

func (c *Counter_uint64) Sub(k string, v uint64) {
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
} // end func GCounter.sub

func (c *Counter_uint64) GetValue(k string) uint64 {
	c.mux.RLock()
	retval := c.m[k]
	c.mux.RUnlock()
	return retval
} // end func GCounter.GetValue

func (c *Counter_uint64) GetReset(k string) uint64 {
	c.mux.Lock()
	retval := c.m[k]
	c.m[k] = 0
	c.mux.Unlock()
	return retval
} // end func GCounter.GetReset

func (c *Counter_uint64) SetValue(k string, v uint64) {
	c.mux.Lock()
	c.m[k] = v
	c.mux.Unlock()
} // end func GCounter.setCounter

func (c *Counter_uint64) DeleteKey(k string) {
	c.mux.Lock()
	delete(c.m, k)
	c.mux.Unlock()
} // end func GCounter.delCounter
