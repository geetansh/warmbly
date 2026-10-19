package models

import (
	"testing"
	"time"
)

func TestResolveEffectiveWarmup(t *testing.T) {
	started := time.Now().Add(-48 * time.Hour)
	paused := time.Now().Add(-time.Hour)
	diagnostic, off := TestParticipationDiagnostic, TestParticipationOff
	state := func(s WarmupState) *WarmupState { return &s }
	observed := time.Now()
	for _, tc := range []struct {
		name   string
		email  *Email
		cloud  *CloudLinkWarmup
		source WarmupSource
		want   WarmupState
	}{
		{"local never started", &Email{}, nil, WarmupSourceLocal, WarmupStateOff},
		{"local running", &Email{Warmup: &started}, nil, WarmupSourceLocal, WarmupStateActive},
		{"local paused", &Email{Warmup: &started, WarmupPausedAt: &paused}, nil, WarmupSourceLocal, WarmupStatePaused},
		{"local held by diagnostic participation", &Email{Warmup: &started, TestMode: &diagnostic}, nil, WarmupSourceLocal, WarmupStatePaused},
		{"local sending through diagnostic participation", &Email{Warmup: &started, TestMode: &diagnostic, TestSendEnabled: true}, nil, WarmupSourceLocal, WarmupStateActive},
		{"local out of tests", &Email{Warmup: &started, TestMode: &off}, nil, WarmupSourceLocal, WarmupStatePaused},
		// The regression this file exists for: Cloud warms it while the local column stays NULL.
		{"cloud warming with no local warmup", &Email{}, &CloudLinkWarmup{EnrollmentState: "active", State: state(WarmupStateActive), ObservedAt: &observed}, WarmupSourceCloud, WarmupStateActive},
		{"cloud paused", &Email{}, &CloudLinkWarmup{EnrollmentState: "active", State: state(WarmupStatePaused)}, WarmupSourceCloud, WarmupStatePaused},
		{"cloud off", &Email{}, &CloudLinkWarmup{EnrollmentState: "active", State: state(WarmupStateOff)}, WarmupSourceCloud, WarmupStateOff},
		{"cloud not reported yet is unknown, never off", &Email{}, &CloudLinkWarmup{EnrollmentState: "active"}, WarmupSourceCloud, WarmupStateUnknown},
		{"cloud enrolling", &Email{}, &CloudLinkWarmup{EnrollmentState: "pending_enroll"}, WarmupSourceCloud, WarmupStateUnknown},
		{"cloud wins over a stale local column", &Email{Warmup: &started}, &CloudLinkWarmup{EnrollmentState: "active", State: state(WarmupStatePaused)}, WarmupSourceCloud, WarmupStatePaused},
		{"leaving cloud hands warmup back", &Email{Warmup: &started}, &CloudLinkWarmup{EnrollmentState: "pending_remove", State: state(WarmupStateActive)}, WarmupSourceLocal, WarmupStateActive},
		{"nil mailbox", nil, nil, WarmupSourceLocal, WarmupStateOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveEffectiveWarmup(tc.email, tc.cloud)
			if got.Source != tc.source || got.State != tc.want {
				t.Fatalf("got %s/%s, want %s/%s", got.Source, got.State, tc.source, tc.want)
			}
		})
	}
}

// The same table as cloudWarmupPaused in web/src/lib/cloudWarmup.test.ts: the two rules must not drift.
func TestPoolLinkMailboxStateCloudWarmupState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state PoolLinkMailboxState
		want  WarmupState
	}{
		{"not reported", PoolLinkMailboxState{}, WarmupStateOff},
		{"warming", PoolLinkMailboxState{Warmup: &WarmupStatusInfo{Enabled: true}}, WarmupStateActive},
		{"paused", PoolLinkMailboxState{Warmup: &WarmupStatusInfo{Enabled: true, Paused: true}}, WarmupStatePaused},
		{"participation off", PoolLinkMailboxState{Warmup: &WarmupStatusInfo{Enabled: true}, Participation: &DiagnosticParticipation{Mode: TestParticipationOff}}, WarmupStatePaused},
		{"diagnostic without send", PoolLinkMailboxState{Warmup: &WarmupStatusInfo{Enabled: true}, Participation: &DiagnosticParticipation{Mode: TestParticipationDiagnostic, Receive: true}}, WarmupStatePaused},
		{"diagnostic with send", PoolLinkMailboxState{Warmup: &WarmupStatusInfo{Enabled: true}, Participation: &DiagnosticParticipation{Mode: TestParticipationDiagnostic, Send: true}}, WarmupStateActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.CloudWarmupState(); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCloudWarmupObservedCarriesVolumes(t *testing.T) {
	started := time.Now().Add(-72 * time.Hour)
	o := PoolLinkMailboxState{Warmup: &WarmupStatusInfo{StartedAt: started, CurrentVolume: 7, TargetVolume: 12}}.CloudWarmupObserved()
	if o.State != WarmupStateActive || o.StartedAt == nil || !o.StartedAt.Equal(started) || o.Volume == nil || *o.Volume != 7 || o.Target == nil || *o.Target != 12 {
		t.Fatalf("observation lost the report: %+v", o)
	}
	if o := (PoolLinkMailboxState{}).CloudWarmupObserved(); o.State != WarmupStateOff || o.StartedAt != nil || o.Volume != nil {
		t.Fatalf("unreported warmup invented values: %+v", o)
	}
}
