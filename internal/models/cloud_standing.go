package models

import "time"

// WarmupReasonCloudEvidenceUnavailable marks a missing Cloud report: it holds sends but is no verdict.
const WarmupReasonCloudEvidenceUnavailable = "cloud_evidence_unavailable"

// CloudStandingUnavailable is the standing shown while Warmbly Cloud has no current report.
func CloudStandingUnavailable() *WarmupHealthInfo {
	return &WarmupHealthInfo{State: string(WarmupHealthBlocked), Reason: WarmupReasonCloudEvidenceUnavailable, Source: WarmupHealthSourceCloud}
}

// CloudEvidenceUnavailable reports whether h is CloudStandingUnavailable rather than a real standing.
func (h *WarmupHealthInfo) CloudEvidenceUnavailable() bool {
	return h != nil && h.Reason == WarmupReasonCloudEvidenceUnavailable
}

func (m CloudLinkMailbox) EffectiveStanding(now time.Time) *WarmupHealthInfo {
	if h := m.Standing; h != nil && (h.State == string(WarmupHealthBlocked) || h.State == string(WarmupHealthQuarantined)) && (h.BlockedUntil == nil || h.BlockedUntil.After(now)) {
		return h
	}
	if m.EnrollmentState != "active" || m.Standing == nil || m.StandingObservedAt == nil || m.StandingObservedAt.After(now) || !m.StandingObservedAt.After(now.Add(-15*time.Minute)) {
		return CloudStandingUnavailable()
	}
	return m.Standing
}
