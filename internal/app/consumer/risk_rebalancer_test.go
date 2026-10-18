package jobs

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type riskRebalanceRepo struct {
	repository.WorkerRepository
	candidates []repository.RiskCandidate
	bands      map[uuid.UUID]models.EmailRiskBand
}

func (r *riskRebalanceRepo) ListRiskCandidates(context.Context, int) ([]repository.RiskCandidate, error) {
	return r.candidates, nil
}

func (r *riskRebalanceRepo) SetEmailAccountRiskBand(_ context.Context, id uuid.UUID, band models.EmailRiskBand) error {
	r.bands[id] = band
	return nil
}

// A missing Warmbly Cloud report keeps the band it had; a real hold beside it still moves it.
func TestRiskRebalancerSkipsUnknownCloudEvidence(t *testing.T) {
	unknown, held := uuid.New(), uuid.New()
	repo := &riskRebalanceRepo{bands: map[uuid.UUID]models.EmailRiskBand{}, candidates: []repository.RiskCandidate{
		{EmailAccountID: unknown, CurrentBand: models.EmailRiskBandRisky, CloudEvidenceUnknown: true},
		{EmailAccountID: held, CurrentBand: models.EmailRiskBandClean, HealthState: models.WarmupHealthBlocked, CloudEvidenceUnknown: true},
	}}
	(&JobsService{WorkerRepo: repo}).rebalanceRisk(context.Background())

	if band, ok := repo.bands[unknown]; ok {
		t.Fatalf("unknown evidence moved the band to %q", band)
	}
	if repo.bands[held] != models.EmailRiskBandQuarantine {
		t.Fatalf("a real block set %q, want quarantine", repo.bands[held])
	}
}

type riskMarkWarmupRepo struct {
	repository.WarmupRepository
	state   models.WarmupHealthState
	unknown bool
}

func (r riskMarkWarmupRepo) GetKnownHealthState(context.Context, uuid.UUID) (models.WarmupHealthState, bool, error) {
	return r.state, r.unknown, nil
}

// A local event re-reads the standing; a missing Cloud report there must not reset the band.
func TestRiskMarkKeepsTheBandOnUnknownCloudEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   models.WarmupHealthState
		unknown bool
		want    models.EmailRiskBand
		writes  bool
	}{
		{"unknown", models.WarmupHealthHealthy, true, "", false},
		{"unknown beside a real hold", models.WarmupHealthQuarantined, true, models.EmailRiskBandQuarantine, true},
		{"known", models.WarmupHealthWatch, false, models.EmailRiskBandRisky, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			repo := &riskRebalanceRepo{bands: map[uuid.UUID]models.EmailRiskBand{}}
			s := &JobsService{WorkerRepo: repo, WarmupRepo: riskMarkWarmupRepo{state: tc.state, unknown: tc.unknown}}
			s.markRiskBandFromWarmupHealth(context.Background(), id, nil)
			band, wrote := repo.bands[id]
			if wrote != tc.writes || band != tc.want {
				t.Fatalf("band = %q (written %v), want %q (written %v)", band, wrote, tc.want, tc.writes)
			}
		})
	}
}
