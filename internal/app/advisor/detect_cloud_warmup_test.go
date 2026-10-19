package advisor

import (
	"encoding/json"
	"testing"
)

// Unreported Cloud warmup is not warmup off: Warmbly Cloud may well be warming the mailbox.
func TestWarmupOffWhileSendingSkipsUnreportedCloudWarmup(t *testing.T) {
	m := healthyMailbox()
	m.WarmupActive, m.WarmupCloud, m.WarmupUnknown = false, true, true
	if _, fired := findingsByKey(Detect(snapshotOf(m), defaults()))["warmup_off_while_sending"]; fired {
		t.Fatal("advised turning on warmup Cloud has not reported")
	}
}

// Cloud refuses stop, so the undo of turning Cloud warmup on has to be a pause.
func TestWarmupOffWhileSendingCloudUndoPauses(t *testing.T) {
	m := healthyMailbox()
	m.WarmupActive, m.WarmupCloud = false, true
	f, fired := findingsByKey(Detect(snapshotOf(m), defaults()))["warmup_off_while_sending"]
	if !fired {
		t.Fatal("Cloud mailbox with warmup off sending cold raised nothing")
	}
	if f.Action == nil || f.Action.Undo == nil {
		t.Fatal("finding offers no undo")
	}
	var undo map[string]any
	if err := json.Unmarshal(f.Action.Undo.Args, &undo); err != nil || undo["action"] != "pause" {
		t.Fatalf("undo would stop Cloud warmup, which Cloud refuses: %s", f.Action.Undo.Args)
	}
}
