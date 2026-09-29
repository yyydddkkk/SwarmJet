package api

import "testing"

func TestValidGitLabTriggerMode(t *testing.T) {
	for _, mode := range []string{"off", "owner", "operator", "all"} {
		if !validGitLabTriggerMode(mode) {
			t.Errorf("rejected supported mode %q", mode)
		}
	}
	for _, mode := range []string{"", "any", "viewer"} {
		if validGitLabTriggerMode(mode) {
			t.Errorf("accepted unsupported mode %q", mode)
		}
	}
}
