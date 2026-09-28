package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ManualMergeEvidenceDigest binds the human checklist to the reviewed commit
// set and the reviewer's summary. Merged flags are excluded so a partially
// completed round can resume without invalidating the same acceptance.
func ManualMergeEvidenceDigest(t Task) string {
	summary := ""
	if review := t.Executions["review"].Result; review != nil {
		summary = review.Summary
	}
	payload, _ := json.Marshal(struct {
		ReviewDigest string
		Revisions    string
		Summary      string
		Checklist    *ManualAcceptance
	}{t.ReviewDigest, Digest(t.Workspace.Revisions), summary, t.HumanAcceptance})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
