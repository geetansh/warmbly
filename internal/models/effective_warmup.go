package models

import "time"

// WarmupSource names who warms a mailbox: this instance, or Warmbly Cloud.
type WarmupSource string

const (
	WarmupSourceLocal WarmupSource = "local"
	WarmupSourceCloud WarmupSource = "cloud"
)

// WarmupState is a mailbox's warmup as its warmer reports it.
type WarmupState string

const (
	WarmupStateActive WarmupState = "active"
	WarmupStatePaused WarmupState = "paused"
	WarmupStateOff    WarmupState = "off"
	// WarmupStateUnknown is a Cloud mailbox whose warmup Cloud has not reported yet; never read it as off.
	WarmupStateUnknown WarmupState = "unknown"
)

// CloudWarmupObserved is the warmup Warmbly Cloud last reported for one enrolled mailbox.
type CloudWarmupObserved struct {
	State     WarmupState
	StartedAt *time.Time
	Volume    *int
	Target    *int
}

// CloudLinkWarmup is the local record of a mailbox's Cloud enrollment and the warmup last mirrored for it.
type CloudLinkWarmup struct {
	EnrollmentState string
	State           *WarmupState
	StartedAt       *time.Time
	Volume          *int
	Target          *int
	ObservedAt      *time.Time
}

// Warms is whether Cloud, not this instance, warms the mailbox; a removal in flight hands it back.
func (c *CloudLinkWarmup) Warms() bool {
	return c != nil && c.EnrollmentState != "" && c.EnrollmentState != "pending_remove"
}

// EffectiveWarmup is the one answer to "is this mailbox warming", whoever does the warming.
type EffectiveWarmup struct {
	Source     WarmupSource `json:"source"`
	State      WarmupState  `json:"state"`
	StartedAt  *time.Time   `json:"started_at,omitempty"`
	ObservedAt *time.Time   `json:"observed_at,omitempty"`
}

// Active is true only for warmup that is running now.
func (w EffectiveWarmup) Active() bool { return w.State == WarmupStateActive }

// ResolveEffectiveWarmup is the only place a mailbox's warmup state is decided; email_accounts.warmup alone is wrong for a Cloud mailbox.
func ResolveEffectiveWarmup(e *Email, cloud *CloudLinkWarmup) EffectiveWarmup {
	if cloud.Warms() {
		w := EffectiveWarmup{Source: WarmupSourceCloud, State: WarmupStateUnknown, ObservedAt: cloud.ObservedAt}
		if cloud.State != nil {
			w.State = *cloud.State
			w.StartedAt = cloud.StartedAt
		}
		return w
	}
	w := EffectiveWarmup{Source: WarmupSourceLocal, State: WarmupStateOff}
	if e == nil || e.Warmup == nil {
		return w
	}
	w.StartedAt = e.Warmup
	if e.IsWarmingActive() && e.TestSendingAllowed() {
		w.State = WarmupStateActive
	} else {
		w.State = WarmupStatePaused
	}
	return w
}

// CloudWarmupState is the twin of cloudWarmupPaused in web/src/lib/cloudWarmup.ts; keep the two rules equal.
func (s PoolLinkMailboxState) CloudWarmupState() WarmupState {
	if s.Warmup == nil {
		return WarmupStateOff
	}
	if s.Warmup.Paused || s.Participation != nil && (s.Participation.Mode == TestParticipationOff || !s.Participation.Send) {
		return WarmupStatePaused
	}
	return WarmupStateActive
}

// CloudWarmupObserved is what this instance keeps of the Cloud's report on one mailbox.
func (s PoolLinkMailboxState) CloudWarmupObserved() CloudWarmupObserved {
	o := CloudWarmupObserved{State: s.CloudWarmupState()}
	if s.Warmup != nil {
		started, volume, target := s.Warmup.StartedAt, s.Warmup.CurrentVolume, s.Warmup.TargetVolume
		if !started.IsZero() {
			o.StartedAt = &started
		}
		o.Volume, o.Target = &volume, &target
	}
	return o
}
