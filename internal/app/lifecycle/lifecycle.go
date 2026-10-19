// Package lifecycle decides when a cold mailbox rests and when it may return.
// Resting removes it from cold rotation while warmup keeps its reputation
// alive; it returns only after a clean probation.
package lifecycle

import (
	"time"

	"github.com/warmbly/warmbly/internal/models"
)

// Decision is what the rebalancer should do with one mailbox.
type Decision struct {
	// Next is the state to move to, equal to the current one when nothing
	// changes.
	Next   models.SendLifecycle
	Reason string
	// RestartProbation asks the caller to re-stamp the clock without changing
	// state. Probation has to measure HEALTHY time: a mailbox that sat resting
	// and unhealthy for three days would otherwise resume on its first healthy
	// tick, having served no clean time at all.
	RestartProbation bool
}

// Changed reports whether the mailbox should move.
func (d Decision) Changed(current models.SendLifecycle) bool { return d.Next != current }

// Decide maps warmup health onto the cold lifecycle. Rests only at quarantined
// and blocked: watch and throttled are spam placement, which slows cold volume
// through the send budget and never takes a mailbox out of rotation.
// health is real evidence only; cloudUnknown means Warmbly Cloud has no current report.
func Decide(current models.SendLifecycle, since *time.Time, health models.WarmupHealthState, cloudUnknown bool, now time.Time) Decision {
	if !current.AutoManaged() {
		return Decision{Next: current}
	}

	switch health {
	case models.WarmupHealthQuarantined, models.WarmupHealthBlocked:
		// Still out: the probation clock starts once it is back in.
		return Decision{Next: models.SendLifecycleResting, RestartProbation: current == models.SendLifecycleResting,
			Reason: "warmup health is " + string(health) + "; out of cold rotation until it recovers"}
	}

	// A missing report never starts a rest, and time without one is not probation.
	if cloudUnknown {
		return Decision{Next: current, RestartProbation: current == models.SendLifecycleResting,
			Reason: "Warmbly Cloud has not reported a current status"}
	}

	// No pool, no signal: never rest, and let a resting clock run rather than strand the mailbox.
	if health == "" {
		if current == models.SendLifecycleResting {
			state := models.SendLifecycleState{State: current, Since: since}
			if state.ReadyToResume(now) {
				return Decision{Next: models.SendLifecycleActive,
					Reason: "warmup is not running, so there was nothing to recover on; returned after the rest window"}
			}
		}
		return Decision{Next: current}
	}

	// Back in the pool. A resting mailbox returns only after a probation, so
	// one good hour cannot bounce it straight back into cold rotation; a watch
	// or throttle still dampens its volume once it is back.
	if current == models.SendLifecycleResting {
		state := models.SendLifecycleState{State: current, Since: since}
		if state.ReadyToResume(now) {
			return Decision{Next: models.SendLifecycleActive, Reason: "recovered and served its rest"}
		}
		return Decision{Next: current}
	}
	return Decision{Next: models.SendLifecycleActive}
}
