package jobs

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
)

// Warmbly Cloud files the mail it warms, and workers admit no local filing for
// a Cloud mailbox, so a queued one would retry forever without completing.
func TestCloudWarmedLeakLeavesUniboxWithoutALocalFiling(t *testing.T) {
	worker := uuid.New()
	e, inbox := leakedWarmup()
	pub := &backfillPublisher{}
	recovery := &recoveryStub{id: uuid.New()}
	s := &JobsService{
		UniboxRepository:   inbox,
		CloudLink:          &warmupInboxCloud{deliveryOK: true},
		WarmupRecoveryRepo: recovery,
		Publisher:          pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementFolder, WarmupFolder: "Warmbly",
		}},
	}

	if done, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err != nil || !done {
		t.Fatalf("cleanup: done=%v err=%v", done, err)
	}
	if len(inbox.deleted) != 1 || inbox.deleted[0] != e.Message.ID {
		t.Fatalf("deleted %v, want the Cloud warmup kept out of the Unibox", inbox.deleted)
	}
	if len(recovery.queued) != 0 || len(pub.actions) != 0 {
		t.Fatalf("queued %d filings and published %d actions for a Cloud mailbox", len(recovery.queued), len(pub.actions))
	}
}

func TestFilingForACloudMailboxIsSkippedOnArrival(t *testing.T) {
	worker := uuid.New()
	e, _ := leakedWarmup()
	pub := &backfillPublisher{}
	recovery := &recoveryStub{id: uuid.New()}
	s := &JobsService{
		CloudLink:          &warmupInboxCloud{},
		WarmupRecoveryRepo: recovery,
		Publisher:          pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementFolder,
		}},
	}
	if err := s.fileWarmupOutOfMailbox(context.Background(), &e); err != nil {
		t.Fatal(err)
	}
	if len(recovery.queued) != 0 || len(pub.actions) != 0 {
		t.Fatalf("queued %d filings and published %d actions for a Cloud mailbox", len(recovery.queued), len(pub.actions))
	}
}

// Not knowing whether Cloud warms the mailbox is not a reason to hide or file
// the leak: the row stays and the mailbox is retried.
func TestCloudEnrollmentLookupFailureHoldsTheLeak(t *testing.T) {
	worker := uuid.New()
	e, inbox := leakedWarmup()
	pub := &backfillPublisher{}
	s := &JobsService{
		UniboxRepository: inbox,
		WarmupRepo:       knownLeak(),
		CloudLink:        &warmupInboxCloud{enrollmentErr: errors.New("database unavailable")},
		Publisher:        pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementFolder,
		}},
	}
	sweep := &warmupInboxSweep{}
	if _, err := s.cleanWarmupInboxBatch(context.Background(), sweep); err == nil {
		t.Fatal("a failed enrollment lookup was reported as success")
	}
	if _, held := sweep.held[e.Message.EmailID]; !held {
		t.Fatal("the mailbox was not held for retry")
	}
	if len(inbox.deleted) != 0 || len(pub.actions) != 0 {
		t.Fatalf("deleted %v and published %d actions without knowing who files the mail", inbox.deleted, len(pub.actions))
	}
}

func TestWarmupFilingRecoveryDropsCloudFilingsBeforeRetrying(t *testing.T) {
	recovery := &recoveryStub{dropped: 3}
	s := &JobsService{WarmupRecoveryRepo: recovery, EmailRepository: backfillEmailRepo{}, Publisher: &backfillPublisher{}}
	if err := s.recoverWarmupFilings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"drop", "claim", "purge"}; !slices.Equal(recovery.calls, want) {
		t.Fatalf("calls = %v, want %v", recovery.calls, want)
	}

	// A failed drop is reported but does not stop the retries for other mailboxes.
	recovery = &recoveryStub{dropErr: errors.New("database unavailable")}
	s.WarmupRecoveryRepo = recovery
	if err := s.recoverWarmupFilings(context.Background()); err == nil {
		t.Fatal("a failed drop was not reported")
	}
	if want := []string{"drop", "claim", "purge"}; !slices.Equal(recovery.calls, want) {
		t.Fatalf("calls after a failed drop = %v, want %v", recovery.calls, want)
	}
}

// Only an active enrollment hands filing to the cloud; any other mailbox still gets a durable filing here.
func TestFilingIsQueuedForAMailboxTheCloudDoesNotHold(t *testing.T) {
	for name, cloud := range map[string]*warmupInboxCloud{
		"not enrolled":    {notEnrolled: true},
		"still enrolling": {pendingEnroll: true},
	} {
		t.Run(name, func(t *testing.T) {
			worker := uuid.New()
			e, _ := leakedWarmup()
			pub := &backfillPublisher{}
			recovery := &recoveryStub{id: uuid.New()}
			s := &JobsService{
				CloudLink:          cloud,
				WarmupRecoveryRepo: recovery,
				Publisher:          pub,
				EmailRepository: backfillEmailRepo{account: &models.Email{
					ID: e.Message.EmailID, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementFolder,
				}},
			}
			if err := s.fileWarmupOutOfMailbox(context.Background(), &e); err != nil {
				t.Fatal(err)
			}
			if len(recovery.queued) != 1 || len(pub.actions) != 1 || pub.actions[0].FilingID != recovery.id.String() {
				t.Fatalf("queued %d, published %+v, want one durable filing", len(recovery.queued), pub.actions)
			}
		})
	}
}
