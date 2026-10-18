package email

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type sendHoldEmailRepo struct {
	repository.EmailRepository
	account *models.Email
}

func (r *sendHoldEmailRepo) Get(context.Context, string, string) (*models.Email, *errx.Error) {
	return r.account, nil
}

type sendHoldLifecycleRepo struct {
	repository.SendLifecycleRepository
	candidate repository.LifecycleCandidate
	set       []models.SendLifecycle
	reasons   []string
}

func (r *sendHoldLifecycleRepo) GetLifecycleCandidate(context.Context, uuid.UUID) (*repository.LifecycleCandidate, error) {
	c := r.candidate
	return &c, nil
}

func (r *sendHoldLifecycleRepo) SetSendLifecycle(_ context.Context, _ uuid.UUID, state models.SendLifecycle, reason string, _ bool) (bool, error) {
	r.set = append(r.set, state)
	r.reasons = append(r.reasons, reason)
	r.candidate.Current = state
	return true, nil
}

func (r *sendHoldLifecycleRepo) GetSendLifecycles(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]models.SendLifecycleState, error) {
	out := map[uuid.UUID]models.SendLifecycleState{}
	for _, id := range ids {
		out[id] = models.SendLifecycleState{State: r.candidate.Current}
	}
	return out, nil
}

func sendHoldFixture(candidate repository.LifecycleCandidate) (*emailService, *sendHoldLifecycleRepo, *models.Email) {
	account := &models.Email{ID: uuid.New()}
	candidate.EmailAccountID = account.ID
	lifecycle := &sendHoldLifecycleRepo{candidate: candidate}
	return &emailService{emailRepository: &sendHoldEmailRepo{account: account}, lifecycleRepo: lifecycle}, lifecycle, account
}

// Putting a mailbox back while Warmbly Cloud has not reported lands it in
// active; the send gate still holds its sends until a fresh report arrives.
func TestSendHoldReleaseWithUnknownCloudEvidenceLandsActive(t *testing.T) {
	since := time.Now().Add(-time.Hour)
	svc, lifecycle, account := sendHoldFixture(repository.LifecycleCandidate{
		Current: models.SendLifecycleResting, Since: &since, CloudEvidenceUnknown: true,
	})
	state, xerr := svc.SetSendHold(context.Background(), uuid.NewString(), account.ID.String(), false)
	if xerr != nil {
		t.Fatalf("SetSendHold: %v", xerr)
	}
	if len(lifecycle.set) != 1 || lifecycle.set[0] != models.SendLifecycleActive || state.State != models.SendLifecycleActive {
		t.Fatalf("release moved to %v (state %+v), want active", lifecycle.set, state)
	}
}

// A real hold beside unknown Cloud evidence still keeps the mailbox resting.
func TestSendHoldReleaseWithARealHoldStaysResting(t *testing.T) {
	svc, lifecycle, account := sendHoldFixture(repository.LifecycleCandidate{
		Current: models.SendLifecycleResting, HealthState: models.WarmupHealthQuarantined, CloudEvidenceUnknown: true,
	})
	if _, xerr := svc.SetSendHold(context.Background(), uuid.NewString(), account.ID.String(), false); xerr != nil {
		t.Fatalf("SetSendHold: %v", xerr)
	}
	if len(lifecycle.set) != 1 || lifecycle.set[0] != models.SendLifecycleResting {
		t.Fatalf("release moved to %v, want resting", lifecycle.set)
	}
}
