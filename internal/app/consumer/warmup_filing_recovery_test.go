package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

type recoveryStub struct {
	repository.WarmupRecoveryRepository
	known     bool
	id        uuid.UUID
	queued    []models.WarmupEmailAction
	completed []uuid.UUID
	err       error
}

func (r *recoveryStub) IsKnown(context.Context, uuid.UUID, string, []string) (bool, error) {
	return r.known, nil
}

func (r *recoveryStub) RememberMessage(context.Context, uuid.UUID, string, time.Time) error {
	return nil
}

func (r *recoveryStub) EnqueueFiling(_ context.Context, action models.WarmupEmailAction) (uuid.UUID, error) {
	if r.err != nil {
		return uuid.Nil, r.err
	}
	r.queued = append(r.queued, action)
	return r.id, nil
}

func (r *recoveryStub) ClaimFilings(context.Context, int) ([]models.WarmupEmailAction, error) {
	return r.queued, nil
}

func (r *recoveryStub) CompleteFiling(_ context.Context, _ uuid.UUID, id uuid.UUID) error {
	r.completed = append(r.completed, id)
	return nil
}

func TestRecoveredWarmupIsFiledOnArrivalWithoutReplayingEngagement(t *testing.T) {
	e, _ := leakedWarmup()
	worker := uuid.New()
	pub := &backfillPublisher{}
	inbox := &warmupInboxRepo{}
	recovery := &recoveryStub{known: true, id: uuid.New()}
	s := &JobsService{WarmupRecoveryRepo: recovery, UniboxRepository: inbox, Publisher: pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{ID: e.Message.EmailID, WorkerID: &worker}}}
	if err := s.HandleNewEmail(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	if inbox.entries != 0 || len(pub.actions) != 1 || len(recovery.queued) != 1 {
		t.Fatalf("recovered warmup leaked or was not queued: inbox=%d published=%d queued=%d", inbox.entries, len(pub.actions), len(recovery.queued))
	}
	action := pub.actions[0]
	if action.FilingID != recovery.id.String() || len(action.Actions) != 1 || action.Actions[0] != models.WarmupActionFile {
		t.Fatalf("recovery replayed engagement or lost acknowledgement ID: %+v", action)
	}
	// A matching sender or subject alone is not recognition evidence.
	recovery.known = false
	e.Message.MessageID = "<ordinary@example.test>"
	if err := s.HandleNewEmail(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	if inbox.entries != 1 || len(pub.actions) != 1 {
		t.Fatalf("ordinary message was hidden: inbox=%d published=%d", inbox.entries, len(pub.actions))
	}
}

func TestWarmupFilingSurvivesUnassignedMailboxAndFailedPublish(t *testing.T) {
	for _, unassigned := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed publish", true: "unassigned mailbox"}[unassigned], func(t *testing.T) {
			e, inbox := leakedWarmup()
			worker := uuid.New()
			account := &models.Email{ID: e.Message.EmailID, WorkerID: &worker}
			if unassigned {
				account.WorkerID = nil
			}
			recovery := &recoveryStub{known: true, id: uuid.New()}
			pub := &backfillPublisher{err: errors.New("bus unavailable")}
			s := &JobsService{WarmupRecoveryRepo: recovery, UniboxRepository: inbox, Publisher: pub,
				EmailRepository: backfillEmailRepo{account: account}}
			if _, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err != nil {
				t.Fatal(err)
			}
			if len(recovery.queued) != 1 || len(inbox.deleted) != 1 || len(recovery.completed) != 0 {
				t.Fatalf("filing lost before acknowledgement: queued=%d hidden=%d completed=%d", len(recovery.queued), len(inbox.deleted), len(recovery.completed))
			}
			account.WorkerID = &worker
			account.WarmupFolder = "Current destination"
			pub.err = nil
			recovery.queued[0].FilingID = recovery.id.String()
			if err := s.retryWarmupFilings(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(pub.actions) != 1 || pub.actions[0].TargetFolder != "Current destination" || pub.workers[0] != worker || len(recovery.completed) != 0 {
				t.Fatalf("retry lost current assignment/settings or completed prematurely: %+v", pub.actions)
			}
			if err := s.HandleWarmupFiled(context.Background(), &models.JobEventWarmupFiled{EmailID: account.ID, FilingID: recovery.id}); err != nil {
				t.Fatal(err)
			}
			if len(recovery.completed) != 1 || recovery.completed[0] != recovery.id {
				t.Fatal("worker acknowledgement did not complete the filing")
			}
		})
	}
}

func TestWarmupFilingPersistenceFailurePreservesCleanupCandidate(t *testing.T) {
	e, inbox := leakedWarmup()
	worker := uuid.New()
	pub := &backfillPublisher{}
	s := &JobsService{WarmupRecoveryRepo: &recoveryStub{known: true, err: errors.New("database unavailable")},
		UniboxRepository: inbox, Publisher: pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{ID: e.Message.EmailID, WorkerID: &worker}}}
	if _, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err == nil {
		t.Fatal("persistence failure was reported as success")
	}
	if len(inbox.deleted) != 0 || len(pub.actions) != 0 {
		t.Fatal("cleanup discarded its candidate before durable filing")
	}
}

func TestWarmupFilingRetryRespectsExplicitInboxPlacement(t *testing.T) {
	account, id := uuid.New(), uuid.New()
	worker := uuid.New()
	recovery := &recoveryStub{queued: []models.WarmupEmailAction{{EmailID: account, FilingID: id.String()}}}
	pub := &backfillPublisher{}
	s := &JobsService{WarmupRecoveryRepo: recovery, Publisher: pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{ID: account, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementInbox}}}
	if err := s.retryWarmupFilings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(pub.actions) != 0 || len(recovery.completed) != 1 {
		t.Fatal("retry moved a message the owner chose to keep in the inbox")
	}
}

func TestWarmupFilingDoesNotRestoreTrashedMail(t *testing.T) {
	e, _ := leakedWarmup()
	e.Message.ProviderFolder = models.FolderTrash
	worker := uuid.New()
	recovery := &recoveryStub{id: uuid.New()}
	pub := &backfillPublisher{}
	s := &JobsService{WarmupRecoveryRepo: recovery, Publisher: pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{ID: e.Message.EmailID, WorkerID: &worker}}}
	if err := s.fileWarmupOutOfMailbox(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	if len(recovery.queued) != 0 || len(pub.actions) != 0 {
		t.Fatal("warmup filing tried to undo provider or retention deletion")
	}
}
