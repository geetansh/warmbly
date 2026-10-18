package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type lifecycleRebalanceRepo struct {
	repository.SendLifecycleRepository
	candidates []repository.LifecycleCandidate
	moved      map[uuid.UUID]models.SendLifecycle
	restarted  []uuid.UUID
	checked    []uuid.UUID
}

func (r *lifecycleRebalanceRepo) ListLifecycleCandidates(context.Context, int) ([]repository.LifecycleCandidate, error) {
	return r.candidates, nil
}

func (r *lifecycleRebalanceRepo) SetSendLifecycle(_ context.Context, id uuid.UUID, state models.SendLifecycle, _ string, _ bool) (bool, error) {
	r.moved[id] = state
	return true, nil
}

func (r *lifecycleRebalanceRepo) RestartProbation(_ context.Context, id uuid.UUID) error {
	r.restarted = append(r.restarted, id)
	return nil
}

func (r *lifecycleRebalanceRepo) MarkLifecycleChecked(_ context.Context, ids []uuid.UUID) error {
	r.checked = append(r.checked, ids...)
	return nil
}

// A mailbox whose Warmbly Cloud report is missing or stale is neither rested,
// resumed nor has its probation restarted; a real hold beside it still rests.
func TestLifecycleRebalancerLeavesUnknownCloudEvidenceAlone(t *testing.T) {
	served := time.Now().Add(-2 * models.RestProbation)
	active, resting, held := uuid.New(), uuid.New(), uuid.New()
	repo := &lifecycleRebalanceRepo{moved: map[uuid.UUID]models.SendLifecycle{}, candidates: []repository.LifecycleCandidate{
		{EmailAccountID: active, Current: models.SendLifecycleActive, CloudEvidenceUnknown: true},
		{EmailAccountID: resting, Current: models.SendLifecycleResting, Since: &served, CloudEvidenceUnknown: true},
		{EmailAccountID: held, Current: models.SendLifecycleActive, HealthState: models.WarmupHealthQuarantined, CloudEvidenceUnknown: true},
	}}
	(&JobsService{LifecycleRepo: repo}).rebalanceLifecycles(context.Background())

	if _, ok := repo.moved[active]; ok {
		t.Fatalf("unknown evidence moved an active mailbox to %q", repo.moved[active])
	}
	if _, ok := repo.moved[resting]; ok {
		t.Fatalf("unknown evidence moved a resting mailbox to %q", repo.moved[resting])
	}
	if len(repo.restarted) != 0 {
		t.Fatalf("unknown evidence restarted probation for %v", repo.restarted)
	}
	if repo.moved[held] != models.SendLifecycleResting {
		t.Fatalf("a real quarantine moved to %q, want resting", repo.moved[held])
	}
	if len(repo.checked) != 3 {
		t.Fatalf("checked %d mailboxes, want all 3", len(repo.checked))
	}
}
