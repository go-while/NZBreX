package main

import (
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/Tensai75/nzbparser"
	"github.com/go-while/go-loggedrwmutex"
)

func testItem(id string) *segmentChanItem {
	return &segmentChanItem{
		mux:     &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-ITEM-" + id},
		segment: &nzbparser.NzbSegment{Id: id},
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
