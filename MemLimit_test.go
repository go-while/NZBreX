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
	waiting, inData := m.waiting, m.memdata[waiter]
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
