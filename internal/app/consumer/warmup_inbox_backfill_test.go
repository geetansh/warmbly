package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/events"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

// backfillPublisher records the warmup actions the sweep sends, or refuses
// them all when err is set.
type backfillPublisher struct {
	events.Publisher
	actions []models.WarmupEmailAction
	workers []uuid.UUID
	err     error
}

func (p *backfillPublisher) PublishWarmupAction(_ context.Context, workerID uuid.UUID, a *models.WarmupEmailAction) error {
	if p.err != nil {
		return p.err
	}
	p.actions = append(p.actions, *a)
	p.workers = append(p.workers, workerID)
	return nil
}

// backfillEmailRepo hands back one mailbox with a chosen filing preference,
// or a chosen lookup error.
type backfillEmailRepo struct {
	repository.EmailRepository
	account *models.Email
	err     *errx.Error
}

func (r backfillEmailRepo) GetByID(context.Context, uuid.UUID) (*models.Email, *errx.Error) {
	return r.account, r.err
}

// knownLeak recognises leakedWarmup by its Message-ID locally, with no Cloud involved.
func knownLeak() *warmupReportRepo {
	return &warmupReportRepo{warmup: map[string]bool{"<leak@test.local>": true}}
}

func leakedWarmup() (models.JobEventNewEmail, *warmupCleanupInbox) {
	e := models.JobEventNewEmail{UserID: uuid.New(), Message: &models.EmailMessageStoreData{
		ID: uuid.New(), EmailID: uuid.New(), MessageID: "<leak@test.local>",
		GmailID: "gm-1", UID: 42, Mailbox: 7, FolderPath: "INBOX",
	}}
	return e, &warmupCleanupInbox{events: []models.JobEventNewEmail{e}}
}

// TestCleanupFilesTheLeakOutOfTheCustomersOwnMailbox covers the half of #583
// the sweep never did: deleting the Unibox row hides warmup mail from our
// inbox, but the copy in the customer's Gmail or Outlook is the one they are
// looking at, and nothing else ever goes back for it.
func TestCleanupFilesTheLeakOutOfTheCustomersOwnMailbox(t *testing.T) {
	worker := uuid.New()
	e, inbox := leakedWarmup()
	pub := &backfillPublisher{}
	s := &JobsService{
		UniboxRepository: inbox,
		WarmupRepo:       knownLeak(),
		Publisher:        pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker,
			WarmupPlacement: models.WarmupPlacementFolder, WarmupFolder: "Warmbly",
		}},
	}

	if _, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(inbox.deleted) != 1 {
		t.Fatalf("unibox row not removed: %d", len(inbox.deleted))
	}
	if len(pub.actions) != 1 {
		t.Fatalf("the leak was hidden from Unibox but left in the customer's mailbox: %d actions published", len(pub.actions))
	}
	got := pub.actions[0]
	if pub.workers[0] != worker {
		t.Errorf("published to worker %s, want %s", pub.workers[0], worker)
	}
	if len(got.Actions) != 1 || got.Actions[0] != models.WarmupActionFile {
		t.Errorf("actions = %v, want only %q: replaying engagement on old mail is activity no real reader produces",
			got.Actions, models.WarmupActionFile)
	}
	// The Gmail path acts on the provider id, so the locators have to survive
	// the trip from the Unibox row into the action.
	if got.GmailID != "gm-1" || got.RFCMessageID != "<leak@test.local>" || got.MailboxFolder != "INBOX" {
		t.Errorf("locators lost: gmail=%q rfc=%q folder=%q", got.GmailID, got.RFCMessageID, got.MailboxFolder)
	}
	if got.Placement != models.WarmupPlacementFolder || got.TargetFolder != "Warmbly" {
		t.Errorf("filing = %q/%q, want folder/Warmbly", got.Placement, got.TargetFolder)
	}
}

// TestCleanupLeavesWarmupAloneWhenTheOwnerAskedForInbox: placement "inbox" is a
// deliberate choice, so there is no leak to repair and moving the mail would
// override the setting.
func TestCleanupLeavesWarmupAloneWhenTheOwnerAskedForInbox(t *testing.T) {
	worker := uuid.New()
	e, inbox := leakedWarmup()
	pub := &backfillPublisher{}
	s := &JobsService{
		UniboxRepository: inbox,
		WarmupRepo:       knownLeak(),
		Publisher:        pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker,
			WarmupPlacement: models.WarmupPlacementInbox,
		}},
	}

	if _, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(pub.actions) != 0 {
		t.Fatalf("moved mail the owner asked to keep in the inbox: %+v", pub.actions)
	}
	if len(inbox.deleted) != 1 {
		t.Fatalf("unibox row should still be removed: %d", len(inbox.deleted))
	}
}

// The Unibox row is the only retry record the sweep has. If the filing action
// never reaches the bus, deleting the row hides the leak in our inbox while
// leaving it in the customer's, with nothing left to try again from. A bus
// refusal is exactly the failure that took production down in #583.
func TestCleanupKeepsTheRowWhenPublishingFails(t *testing.T) {
	worker := uuid.New()
	e, inbox := leakedWarmup()
	pub := &backfillPublisher{err: errors.New("schema registry request failed")}
	s := &JobsService{
		UniboxRepository: inbox,
		WarmupRepo:       knownLeak(),
		Publisher:        pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{
			ID: e.Message.EmailID, WorkerID: &worker, WarmupPlacement: models.WarmupPlacementFolder,
		}},
	}

	sweep := &warmupInboxSweep{}
	_, err := s.cleanWarmupInboxBatch(context.Background(), sweep)
	if err == nil {
		t.Fatal("a failed publish was reported as success")
	}
	if h, held := sweep.held[e.Message.EmailID]; !held || h.resume != uuid.Nil {
		t.Fatalf("the mailbox was not held for a retry of the leak it did not file: %v", sweep.held)
	}
	if len(inbox.deleted) != 0 {
		t.Fatal("the unibox row was deleted although the customer's copy was never filed")
	}

	// Once the bus is back the retry files and deletes.
	pub.err = nil
	sweep.finish(time.Now())
	if done, err := s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done {
		t.Fatalf("retry after the bus recovered: done=%v err=%v", done, err)
	}
	if len(pub.actions) != 1 || len(inbox.deleted) != 1 {
		t.Fatalf("retry did not file and delete: actions=%d deleted=%d", len(pub.actions), len(inbox.deleted))
	}
}

// A lookup that fails for a reason other than "no such mailbox" is transient
// and keeps the row; a mailbox that is genuinely gone has nothing to file
// into and the row is cleared as before.
func TestCleanupDistinguishesAMissingMailboxFromAFailedLookup(t *testing.T) {
	_, inbox := leakedWarmup()
	s := &JobsService{
		UniboxRepository: inbox,
		WarmupRepo:       knownLeak(),
		Publisher:        &backfillPublisher{},
		EmailRepository:  backfillEmailRepo{err: errx.InternalError()},
	}
	if _, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err == nil {
		t.Fatal("a failed mailbox lookup was reported as success")
	}
	if len(inbox.deleted) != 0 {
		t.Fatal("the unibox row was deleted on a transient lookup failure")
	}

	_, inbox = leakedWarmup()
	s.UniboxRepository = inbox
	s.EmailRepository = backfillEmailRepo{err: errx.ErrNotFound}
	if done, err := s.cleanWarmupInboxBatch(context.Background(), &warmupInboxSweep{}); err != nil || !done {
		t.Fatalf("a missing mailbox should be skipped, not retried: done=%v err=%v", done, err)
	}
	if len(inbox.deleted) != 1 {
		t.Fatal("the unibox row for a mailbox that no longer exists was kept")
	}
}
