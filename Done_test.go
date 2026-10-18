package main

import gotesting "testing"

// An item uploaded to a provider counts as there (it used to need availableOn,
// which an upload doesn't set, so uploaded items were never "done").
func TestOnAllProviders(t *gotesting.T) {
	s := &SESSION{providerList: []*Provider{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	item := &segmentChanItem{availableOn: map[int]bool{}, uploadedTo: map[int]bool{}}
	if s.onAllProviders(item) {
		t.Fatal("nothing known: reported on all providers")
	}
	item.availableOn[0], item.availableOn[1] = true, true
	if s.onAllProviders(item) {
		t.Fatal("missing on provider 2: reported on all providers")
	}
	item.uploadedTo[2] = true
	if !s.onAllProviders(item) {
		t.Fatal("available on 0 and 1, uploaded to 2: not reported on all providers")
	}
	delete(item.availableOn, 1)
	if s.onAllProviders(item) {
		t.Fatal("provider 1 dropped: still reported on all providers")
	}
}

// -dlmissing wants articles missing somewhere, -dlall every one; both only
// until it is downloaded or cached. Without them nothing is downloaded for
// its own sake.
func TestWantDownload(t *gotesting.T) {
	testCfg()
	defer func() { cfg.opt.DlMissing, cfg.opt.DlAll = false, false }()
	everywhere := &segmentChanItem{missingOn: map[int]bool{}}
	missing := &segmentChanItem{missingOn: map[int]bool{1: true}}
	cached := &segmentChanItem{missingOn: map[int]bool{1: true}, flagCache: true}
	for _, c := range []struct {
		name                        string
		dlMissing, dlAll            bool
		everywhere, missing, cached bool
	}{
		{"no flag", false, false, false, false, false},
		{"-dlmissing", true, false, false, true, false},
		{"-dlall", false, true, true, true, false},
	} {
		cfg.opt.DlMissing, cfg.opt.DlAll = c.dlMissing, c.dlAll
		if got := [3]bool{wantDownload(everywhere), wantDownload(missing), wantDownload(cached)}; got != [3]bool{c.everywhere, c.missing, c.cached} {
			t.Errorf("%s: everywhere/missing/cached = %v, want %v", c.name, got, [3]bool{c.everywhere, c.missing, c.cached})
		}
	}
}
