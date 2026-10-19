package analytics

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/warmbly/warmbly/internal/models"
)

// statusFor builds one mailbox's status the way the list and the drawer do.
func statusFor(t *testing.T, email *models.Email) *models.EmailAccountStatus {
	t.Helper()
	s := &analyticsService{}
	reads := s.loadStatusReads(t.Context(), []models.Email{*email}, nil)
	return s.assembleAccountStatus(t.Context(), email, &models.AccountDailyUsage{}, nil, nil, false, nil, false, reads)
}

// A mailbox Warmbly Cloud warms keeps a NULL local warmup column; its status must still say it is warming.
func TestAccountStatusReportsCloudWarmup(t *testing.T) {
	started := time.Now().Add(-3 * 24 * time.Hour)
	active, paused := models.WarmupStateActive, models.WarmupStatePaused
	volume, target := 6, 13
	email := &models.Email{ID: uuid.New(), Status: "active", WarmupMax: 40, WarmupReplyRate: 30, CloudWarmup: &models.CloudLinkWarmup{
		EnrollmentState: "active", State: &active, StartedAt: &started, Volume: &volume, Target: &target,
	}}
	w := statusFor(t, email).WarmupStatus
	if w == nil {
		t.Fatal("cloud-warmed mailbox reported no warmup")
	}
	if w.Source != models.WarmupSourceCloud || w.State != models.WarmupStateActive || !w.Enabled || w.Paused {
		t.Fatalf("cloud warmup misreported: %+v", w)
	}
	if w.CurrentVolume != 6 || w.TargetVolume != 13 || w.MaxVolume != 40 || w.DaysActive != 3 {
		t.Fatalf("cloud warmup lost its numbers: %+v", w)
	}

	email.CloudWarmup.State = &paused
	if w := statusFor(t, email).WarmupStatus; w == nil || w.State != models.WarmupStatePaused || !w.Paused {
		t.Fatalf("cloud pause misreported: %+v", w)
	}

	email.CloudWarmup.State = nil
	if w := statusFor(t, email).WarmupStatus; w == nil || w.State != models.WarmupStateUnknown || w.Enabled {
		t.Fatalf("unreported cloud warmup must read unknown, not off: %+v", w)
	}
}

func TestAccountStatusLocalWarmupKeepsItsSource(t *testing.T) {
	started := time.Now().Add(-24 * time.Hour)
	w := statusFor(t, &models.Email{ID: uuid.New(), Status: "active", Warmup: &started}).WarmupStatus
	if w == nil || w.Source != models.WarmupSourceLocal || w.State != models.WarmupStateActive || !w.Enabled {
		t.Fatalf("local warmup misreported: %+v", w)
	}
	if w := statusFor(t, &models.Email{ID: uuid.New(), Status: "active"}).WarmupStatus; w != nil {
		t.Fatalf("mailbox with warmup off reported a warmup: %+v", w)
	}
}
