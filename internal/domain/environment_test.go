package domain

import "testing"

func TestNetworkValidationRejectsEmbeddedSecrets(t *testing.T) {
	for _, proxy := range []string{"http://u:p@host:80", "socks5://host:1", "http://host/?secret=a", "http://host/other", "http://host\n"} {
		if e := (NetworkProfile{ID: "a", Name: "a", HTTPProxy: proxy}).Validate(); e == nil {
			t.Fatal(proxy)
		}
	}
	if e := (NetworkProfile{ID: "a", Name: "a", HTTPSProxy: "http://proxy:8080"}).Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestExplicitDirectAndNodeSelection(t *testing.T) {
	if e := (NetworkChoice{Mode: "direct", ID: "a"}).Validate(); e == nil {
		t.Fatal("ambiguous direct")
	}
	if e := (NodeSelection{Mode: "selected"}).Validate(); e == nil {
		t.Fatal("empty selected")
	}
	if (NodeSelection{Mode: "selected", IDs: []string{"a"}}).Allows("b") {
		t.Fatal("wrong node")
	}
}
