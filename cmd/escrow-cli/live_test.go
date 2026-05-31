package main

import "testing"

func TestLiveMatch(t *testing.T) {
	e := liveEvent{Ecosystem: "npm", Action: "allow", Kind: "downloaded"}
	if !liveMatch(e, "", "all") {
		t.Error("all should match")
	}
	if !liveMatch(e, "npm", "downloaded") {
		t.Error("npm/downloaded should match")
	}
	if liveMatch(e, "pypi", "all") {
		t.Error("pypi filter should exclude npm")
	}
	if liveMatch(e, "", "scanned") {
		t.Error("scanned filter should exclude a downloaded event")
	}
	if !liveMatch(liveEvent{Action: "block"}, "", "blocked") {
		t.Error("blocked filter should match a block event")
	}
	if liveMatch(liveEvent{Action: "block"}, "", "scanned") {
		t.Error("scanned filter should exclude a block event")
	}
}
