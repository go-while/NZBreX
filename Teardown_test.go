package main

import (
	gotesting "testing" // Workers.go declares a const named testing
	"time"

	"github.com/go-while/go-loggedrwmutex"
)

func testCfg() {
	if cfg == nil {
		cfg = &Config{opt: &CFG{}}
	}
}

func closedWithin(ch chan *segmentChanItem, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// Plan 2.6 + 3.1 + 2.5: every segment channel has one close owner and the
// maps are built once per group, so teardown never closes a channel twice.
// StopRoutines used to close the downs/reups channels per provider and
// closeSession closed them (and the feeder-closed check channels) again:
// "close of closed channel".
func TestSessionTeardownClosesEachChannelOnce(t *gotesting.T) {
	testCfg()
	cfg.opt.ChanSize = 4
	prov := func(name, group string) *Provider {
		return &Provider{Name: name, Group: group, Enabled: true, MaxConns: 1,
			mux: &loggedrwmutex.LoggedSyncRWMutex{Name: name}}
	}
	s := &SESSION{
		mux:      &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-SESSION"},
		stopChan: make(chan struct{}),
		// two providers share group g: the old per-provider loop closed g twice
		providerList: []*Provider{prov("a", "g"), prov("b", "g"), prov("c", "h")},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("teardown panicked: %v", r)
		}
	}()

	s.makeSegmentChans()
	if len(s.segmentChansCheck) != 2 || len(s.segmentChansDowns) != 2 || len(s.segmentChansReups) != 2 {
		t.Fatalf("want one channel per group (2), got check=%d downs=%d reups=%d",
			len(s.segmentChansCheck), len(s.segmentChansDowns), len(s.segmentChansReups))
	}
	check, downs, reups := s.segmentChansCheck, s.segmentChansDowns, s.segmentChansReups

	// the feeders own the check channels: with no segments they close them after ~1s
	for g, ch := range check {
		if !closedWithin(ch, 5*time.Second) {
			t.Fatalf("feeder did not close check channel of group %q", g)
		}
	}

	s.StopRoutines()
	s.StopRoutines() // second call must be a no-op
	for g := range downs {
		if !closedWithin(downs[g], time.Second) || !closedWithin(reups[g], time.Second) {
			t.Fatalf("downs/reups channel of group %q not closed", g)
		}
	}
	if !s.stopped() {
		t.Fatal("stopChan not closed")
	}

	s.closeSession() // must not close anything again
	if s.segmentChansCheck != nil || s.segmentChansDowns != nil || s.segmentChansReups != nil {
		t.Fatal("closeSession did not drop the channel maps")
	}

	// sessions are single-use
	p := &PROCESSOR{mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-PROCESSOR"}}
	if err := p.LaunchSession(s, "", nil); err == nil {
		t.Fatal("LaunchSession accepted a session that already ran")
	}
}

// Plan 2.6: proxy/web sessions were built with a nil stopChan, so any
// StopRoutines on them panicked on close(nil).
func TestStopRoutinesWithoutChannels(t *gotesting.T) {
	testCfg()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StopRoutines panicked: %v", r)
		}
	}()
	s := &SESSION{mux: &loggedrwmutex.LoggedSyncRWMutex{Name: "TEST-PROXY"}, proxy: true}
	s.StopRoutines()
	s.StopRoutines()
	if s.stopped() {
		t.Fatal("nil stopChan reported as stopped")
	}
}
