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
