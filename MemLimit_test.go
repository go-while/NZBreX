package main

import (
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/go-while/go-loggedrwmutex"
	"github.com/javi11/nzbparser"
)

func testItem(id string) *segmentChanItem {
	return &segmentChanItem{
		mux:     &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-ITEM-" + id},
		segment: &nzbparser.NzbSegment{ID: id},
	}
}

// Plan 3.6: MemLockWait used to block on <-memchan with no way out. A closed
// stop now aborts the wait and rolls back, so the item can be locked again later
// (the old code left memdata set and the next MemLockWait called os.Exit(1)).
func TestMemLockWaitAbortsOnStopAndRollsBack(t *gotesting.T) {
	testCfg()
	if GCounter == nil {
		GCounter = NewCounter(10)
	}
	m := NewMemLimiter(1)
	holder, waiter := testItem("holder"), testItem("waiter")
	if !m.MemLockWait(holder, "test", nil) {
		t.Fatal("first lock failed with a free slot")
	}

	stop := make(chan struct{})
	res := make(chan bool, 1)
	go func() { res <- m.MemLockWait(waiter, "test", stop) }()
	select {
	case <-res:
		t.Fatal("MemLockWait returned while no slot was free")
	case <-time.After(200 * time.Millisecond):
	}
	close(stop)
	select {
	case ok := <-res:
		if ok {
			t.Fatal("aborted wait reported a slot")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MemLockWait did not return after stop was closed")
	}
	m.mux.RLock()
	waiting, inData := m.waiting.Load(), m.memdata[waiter]
	m.mux.RUnlock()
	if waiting != 0 || inData || waiter.memlocked != 0 {
		t.Fatalf("no rollback: waiting=%d memdata=%t memlocked=%d", waiting, inData, waiter.memlocked)
	}

	// a free slot wins over a closed stop
	m.MemReturn("test", holder)
	if !m.MemLockWait(waiter, "test", stop) {
		t.Fatal("closed stop beat a free slot")
	}
	if used, _ := m.Usage(); used != 1 {
		t.Fatalf("used slots = %d, want 1", used)
	}
}

// Plan 3.8: MemReturn checked memlocked, then returned the slot. Two concurrent
// returns of one item both passed the check and handed back two slots.
func TestMemReturnTwiceReturnsOneSlot(t *gotesting.T) {
	testCfg()
	if GCounter == nil {
		GCounter = NewCounter(10)
	}
	const slots = 64
	m := NewMemLimiter(slots)
	for i := 0; i < 500; i++ {
		it := testItem("r")
		if !m.MemLockWait(it, "test", nil) {
			t.Fatal("lock failed with free slots")
		}
		start := make(chan struct{})
		done := make(chan struct{}, 2)
		for j := 0; j < 2; j++ {
			go func() { <-start; m.MemReturn("test", it); done <- struct{}{} }()
		}
		close(start)
		<-done
		<-done
		if used, _ := m.Usage(); used != 0 {
			t.Fatalf("round %d: used slots = %d after returning one item twice, want 0", i, used)
		}
		if got := len(m.memchan); got != slots {
			t.Fatalf("round %d: %d free slots, want %d", i, got, slots)
		}
		if it.memlocked != 0 {
			t.Fatalf("round %d: memlocked = %d, want 0", i, it.memlocked)
		}
	}
}

// Plan 3.8: a second MemLockWait on an item whose first lock was still waiting
// called os.Exit(1). It now waits for the first one and shares its slot.
func TestMemLockWaitSameItemTwiceSharesOneSlot(t *gotesting.T) {
	testCfg()
	if GCounter == nil {
		GCounter = NewCounter(10)
	}
	m := NewMemLimiter(1)
	holder, it := testItem("holder"), testItem("it")
	m.MemLockWait(holder, "test", nil)

	res := make(chan bool, 2)
	go func() { res <- m.MemLockWait(it, "first", nil) }()
	time.Sleep(100 * time.Millisecond) // let the first one start waiting
	go func() { res <- m.MemLockWait(it, "second", nil) }()
	time.Sleep(100 * time.Millisecond)

	m.MemReturn("test", holder)
	for i := 0; i < 2; i++ {
		select {
		case ok := <-res:
			if !ok {
				t.Fatal("lock reported no slot")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("second MemLockWait on the same item never returned")
		}
	}
	if used, _ := m.Usage(); used != 1 {
		t.Fatalf("used slots = %d, want 1", used)
	}
	m.MemReturn("test", it)
	if used, _ := m.Usage(); used != 0 {
		t.Fatalf("used slots = %d after return, want 0", used)
	}
}

// A download with nowhere to upload it gives its slot back once its article is
// on disk: right away if the cache writer was first, from the cache writer if
// the download finished first. Exactly once either way, and the article is freed.
func TestRetireAfterDownloadOrCacheWrite(t *gotesting.T) {
	testCfg()
	if GCounter == nil {
		GCounter = NewCounter(10)
	}
	oldMem, oldCache, oldOpt := memlim, cacheON, *cfg.opt
	defer func() { memlim, cacheON, *cfg.opt = oldMem, oldCache, oldOpt }()
	memlim, cacheON = NewMemLimiter(1), true
	cfg.opt.YencWrite = false
	s := &SESSION{}

	for _, cacheFirst := range []bool{true, false} {
		item := testItem("x")
		item.missingOn = map[int]bool{} // on every provider: no upload target
		item.article = []string{"line"}
		if !memlim.MemLockWait(item, "test", nil) {
			t.Fatal("lock failed")
		}
		cacheWrite := func() {
			item.mux.Lock()
			item.flagCache = true // what CacheWriter does
			item.mux.Unlock()
			afterCacheWrite(item)
		}
		if cacheFirst {
			cacheWrite()
			if used, _ := memlim.Usage(); used != 1 {
				t.Fatalf("cache first: slot returned before the download finished (used=%d)", used)
			}
			s.retireAfterDownload(item)
		} else {
			s.retireAfterDownload(item)
			if used, _ := memlim.Usage(); used != 1 {
				t.Fatalf("download first: slot returned before the article was on disk (used=%d)", used)
			}
			cacheWrite()
		}
		s.retireNoUploadTarget(item) // the divider may come by as well: no double return
		if used, _ := memlim.Usage(); used != 0 {
			t.Errorf("cacheFirst=%t: %d slot(s) still used, want 0", cacheFirst, used)
		}
		if item.article != nil {
			t.Errorf("cacheFirst=%t: article still in memory", cacheFirst)
		}
	}
}
