package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type fallbackCloudHealth struct {
	repository.WarmupRepository
	states map[uuid.UUID]repository.WarmupHealthRead
	reads  int
}

func (r *fallbackCloudHealth) GetHealthState(_ context.Context, id uuid.UUID) (models.WarmupHealthState, *time.Time, error) {
	h := r.states[id]
	return h.State, h.BlockedUntil, nil
}

func (r *fallbackCloudHealth) GetHealthStates(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]repository.WarmupHealthRead, error) {
	r.reads++
	if r.reads == 1 {
		return nil, errors.New("health batch unavailable")
	}
	return map[uuid.UUID]repository.WarmupHealthRead{ids[0]: r.states[ids[0]]}, nil
}

type fallbackCloudCounts struct{ repository.TaskRepository }

func (*fallbackCloudCounts) CountCampaignEmailsSentTodayByAccounts(context.Context, []uuid.UUID) (map[uuid.UUID]int, error) {
	return map[uuid.UUID]int{}, nil
}

func TestMissingCloudClassificationSurvivesHealthBatchFailure(t *testing.T) {
	for _, realHold := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing report", true: "real hold"}[realHold], func(t *testing.T) {
			now := time.Now()
			ended := now.Add(-time.Minute)
			acct := models.Email{ID: uuid.New(), CampaignLimit: 50}
			repo := &fallbackCloudHealth{states: map[uuid.UUID]repository.WarmupHealthRead{acct.ID: {
				State: models.WarmupHealthBlocked, BlockedUntil: &ended, CloudStatusOnly: !realHold, CloudReportMissing: true,
			}}}
			s := &schedulerService{warmupRepo: repo, taskRepo: &fallbackCloudCounts{}}
			pass := &campaignPass{campaign: &models.Campaign{DailyLimit: 50}, risk: models.OrgRiskTrusted,
				health: map[uuid.UUID]healthRead{}, sentToday: map[uuid.UUID]int{}}
			if err := s.prefillPool(t.Context(), pass, []models.Email{acct}); err != nil {
				t.Fatal(err)
			}
			if len(pass.health) != 0 {
				t.Fatal("failed batch populated the health memo")
			}
			d := s.planMailbox(t.Context(), pass, acct, 0, 0, nil, now, 10*3600, now.Add(10*time.Hour))
			wantState, wantBand := models.MailboxPlanCloudStatus, ""
			if realHold {
				wantState, wantBand = models.MailboxPlanHealthHold, string(models.WarmupHealthBlocked)
			}
			if d.state != wantState || d.healthBand() != wantBand || !d.health.cloudMissing {
				t.Fatalf("fallback plan: state=%q band=%q health=%+v", d.state, d.healthBand(), d.health)
			}
			if gate, remaining := s.gateFor(t.Context(), pass, acct, 10); gate.reason != gateHealth || remaining != 0 {
				t.Fatalf("fallback opened send gate: %+v remaining=%d", gate, remaining)
			}
			if repo.reads != 2 {
				t.Fatalf("health reads=%d, want one failed batch and one memoized fallback", repo.reads)
			}
		})
	}
}

// A hold that is only a missing Warmbly Cloud report stays a health gate, so
// nothing is sent, but carries its own mark for the sending plan.
func TestHealthGateMarksAMissingCloudReport(t *testing.T) {
	cloudOnly, held := uuid.New(), uuid.New()
	pass := &campaignPass{health: map[uuid.UUID]healthRead{
		cloudOnly: {state: models.WarmupHealthBlocked, known: true, cloudStatus: true},
		held:      {state: models.WarmupHealthBlocked, known: true},
	}}
	s := &schedulerService{}
	if gate, remaining := s.gateFor(context.Background(), pass, models.Email{ID: cloudOnly}, 10); gate.reason != gateHealth || !gate.cloudStatus || remaining != 0 {
		t.Fatalf("missing Cloud report gave %+v with %d left, want a health hold marked as a Cloud status", gate, remaining)
	}
	if gate, _ := s.gateFor(context.Background(), pass, models.Email{ID: held}, 10); gate.reason != gateHealth || gate.cloudStatus {
		t.Fatalf("real block gave %+v, want an unmarked health hold", gate)
	}
}

// The plan's mailbox row names a missing Cloud report as its own state with no
// "blocked" band, and keeps the band for a real hold.
func TestPlanMailboxRowNamesAMissingCloudReport(t *testing.T) {
	now := time.Now()
	cloudOnly, held := models.Email{ID: uuid.New(), CampaignLimit: 50}, models.Email{ID: uuid.New(), CampaignLimit: 50}
	pass := &campaignPass{campaign: &models.Campaign{DailyLimit: 50}, risk: models.OrgRiskTrusted, health: map[uuid.UUID]healthRead{
		cloudOnly.ID: {state: models.WarmupHealthBlocked, known: true, cloudStatus: true},
		held.ID:      {state: models.WarmupHealthQuarantined, known: true},
	}}
	s := &schedulerService{}
	d := s.planMailbox(t.Context(), pass, cloudOnly, 0, 0, nil, now, 10*3600, now.Add(10*time.Hour))
	if d.state != models.MailboxPlanCloudStatus || d.healthBand() != "" {
		t.Fatalf("missing Cloud report row = %q band %q, want cloud_status_hold with no band", d.state, d.healthBand())
	}
	d = s.planMailbox(t.Context(), pass, held, 0, 0, nil, now, 10*3600, now.Add(10*time.Hour))
	if d.state != models.MailboxPlanHealthHold || d.healthBand() != string(models.WarmupHealthQuarantined) {
		t.Fatalf("real hold row = %q band %q, want health_hold quarantined", d.state, d.healthBand())
	}
}

// A hold beside a missing Cloud report never lapses on the clock: its end can pass
// between the read and the check while the report is still missing.
func TestHealthGateHoldsOpenEndedWhileTheCloudReportIsMissing(t *testing.T) {
	ended := time.Now().Add(-time.Minute)
	missing, known := uuid.New(), uuid.New()
	pass := &campaignPass{health: map[uuid.UUID]healthRead{
		missing: {state: models.WarmupHealthQuarantined, blockedUntil: &ended, known: true, cloudMissing: true},
		known:   {state: models.WarmupHealthQuarantined, blockedUntil: &ended, known: true},
	}}
	s := &schedulerService{}
	if gate, _ := s.gateFor(context.Background(), pass, models.Email{ID: missing}, 10); gate.reason != gateHealth {
		t.Fatalf("an ended hold beside a missing Cloud report gave %+v, want it still held", gate)
	}
	if gate, _ := s.gateFor(context.Background(), pass, models.Email{ID: known}, 10); gate.reason == gateHealth {
		t.Fatalf("an ended hold with a current report gave %+v, want the health gate open", gate)
	}
}
