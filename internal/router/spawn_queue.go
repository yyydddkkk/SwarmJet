package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// triggerAgentID identifies one rule activation independently of webhook
// redelivery. Revision rules already use revisionAgentID in routeTrigger.
func triggerAgentID(hiveID string, e Event, rule eventRule) string {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		hiveID, e.Type, e.Action, e.Number, e.SHA, rule.Event, rule.Role, e.DeliveryID)
	sum := sha256.Sum256([]byte(key))
	return "trigger-" + hex.EncodeToString(sum[:16])
}
