package jobs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

// threadReplyRepo knows which message ids are turns of a warmup conversation
// and records what ingest remembers. Every path ahead of the ancestry check
// answers "not warmup", which is exactly the position a hand-typed reply is
// in: no token, a fresh id, a "Re:" subject.
type threadReplyRepo struct {
	repository.WarmupRepository
	turns    map[string]bool
	err      error
	asked    [][]string
	recorded []string
}

func (r *threadReplyRepo) FindDeliveredWarmupToken(context.Context, uuid.UUID, string, string, string) (*models.WarmupToken, error) {
	return nil, nil
}

func (r *threadReplyRepo) IsWarmupDelivery(context.Context, uuid.UUID, string, string, string) (bool, error) {
	return false, nil
}

func (r *threadReplyRepo) IsWarmupThreadReply(_ context.Context, _ uuid.UUID, parents []string) (bool, error) {
	r.asked = append(r.asked, parents)
	if r.err != nil {
		return false, r.err
	}
	for _, p := range parents {
		if r.turns[p] {
			return true, nil
		}
	}
	return false, nil
}

func (r *threadReplyRepo) RecordWarmupThreadMessage(_ context.Context, _ uuid.UUID, id string) error {
	r.recorded = append(r.recorded, id)
	return nil
}

func handTypedReply(inReplyTo ...string) *models.JobEventNewEmail {
	return &models.JobEventNewEmail{UserID: uuid.New(), Message: &models.EmailMessageStoreData{
		ID: uuid.New(), EmailID: uuid.New(), Folder: models.FolderInbox, FolderPath: "INBOX",
		MessageID: "<reply-1@gmail.test>", GmailID: "gm-9", UID: 9, Mailbox: 3,
		FromAddr:  []string{"partner@gmail.test"},
		Subject:   "Re: Quick question",
		InReplyTo: inReplyTo,
	}}
}

// The report behind this: a warmup message between two of the owner's own
// mailboxes, answered by hand from Gmail, and the answer sitting in the
// unibox and in the inbox. The reply carries nothing the token paths can see.
func TestHandTypedReplyInAWarmupThreadStaysOutOfTheInbox(t *testing.T) {
	worker := uuid.New()
	for _, tc := range []struct {
		name      string
		inReplyTo []string
		turns     map[string]bool
		placement string
		wantHide  bool
		wantFile  bool
		wantAsked []string
	}{
		{
			name: "a reply to a warmup send", inReplyTo: []string{"<warm-1@sender.test>"},
			turns: map[string]bool{"warm-1@sender.test": true}, wantHide: true, wantFile: true,
			wantAsked: []string{"warm-1@sender.test"},
		},
		{
			name: "a reply to that reply", inReplyTo: []string{"<reply-0@gmail.test>"},
			turns: map[string]bool{"reply-0@gmail.test": true}, wantHide: true, wantFile: true,
			wantAsked: []string{"reply-0@gmail.test"},
		},
		{
			name: "an IMAP envelope with two ids in one value", inReplyTo: []string{"<older@x.test> <warm-1@sender.test>"},
			turns: map[string]bool{"warm-1@sender.test": true}, wantHide: true, wantFile: true,
			wantAsked: []string{"older@x.test", "warm-1@sender.test"},
		},
		{
			name: "a reply to ordinary mail", inReplyTo: []string{"<real@prospect.test>"},
			turns:     map[string]bool{"warm-1@sender.test": true},
			wantAsked: []string{"real@prospect.test"},
		},
		{
			name:  "no In-Reply-To asks nothing",
			turns: map[string]bool{"warm-1@sender.test": true},
		},
		{
			name: "the owner asked for warmup to stay in the inbox", inReplyTo: []string{"<warm-1@sender.test>"},
			turns: map[string]bool{"warm-1@sender.test": true}, placement: models.WarmupPlacementInbox, wantHide: true,
			wantAsked: []string{"warm-1@sender.test"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &threadReplyRepo{turns: tc.turns}
			inbox := &warmupInboxRepo{}
			pub := &backfillPublisher{}
			account := &models.Email{WorkerID: &worker, WarmupPlacement: tc.placement, WarmupFolder: "Warmbly"}
			s := &JobsService{WarmupRepo: repo, UniboxRepository: inbox, Publisher: pub, EmailRepository: backfillEmailRepo{account: account}}
			e := handTypedReply(tc.inReplyTo...)
			if err := s.ingestNewEmail(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			if hidden := inbox.entries == 0; hidden != tc.wantHide {
				t.Fatalf("unibox entries = %d, want hidden %v", inbox.entries, tc.wantHide)
			}
			if tc.wantAsked == nil {
				if len(repo.asked) != 0 {
					t.Fatalf("asked about %v with no In-Reply-To", repo.asked)
				}
			} else if len(repo.asked) != 1 || !reflect.DeepEqual(repo.asked[0], tc.wantAsked) {
				t.Fatalf("asked about %v, want %v", repo.asked, tc.wantAsked)
			}
			if tc.wantHide {
				if !reflect.DeepEqual(repo.recorded, []string{"<reply-1@gmail.test>"}) {
					t.Fatalf("recorded %v, want the reply's own id so the next turn is recognised", repo.recorded)
				}
			} else if len(repo.recorded) != 0 {
				t.Fatalf("recorded %v for ordinary mail", repo.recorded)
			}
			if !tc.wantFile {
				if len(pub.actions) != 0 {
					t.Fatalf("published %v, want no filing", pub.actions)
				}
				return
			}
			if len(pub.actions) != 1 || pub.workers[0] != worker {
				t.Fatalf("published %d actions to %v, want one to the mailbox's worker", len(pub.actions), pub.workers)
			}
			a := pub.actions[0]
			if !reflect.DeepEqual(a.Actions, []string{models.WarmupActionFile}) || a.TargetFolder != "Warmbly" || a.Placement != models.WarmupPlacementFolder {
				t.Fatalf("action = %+v, want filing only into the warmup folder", a)
			}
			if a.EmailID != e.Message.EmailID || a.GmailID != "gm-9" || a.UID != 9 || a.MailboxUIDValidity != 3 || a.MailboxFolder != "INBOX" || a.RFCMessageID != "<reply-1@gmail.test>" {
				t.Fatalf("action names the wrong message: %+v", a)
			}
		})
	}
}

// A lookup that fails must hold the message, not expose it: the arrival is
// deferred and re-offered, and the retry hides it.
func TestHandTypedReplyIsHeldWhileTheThreadLookupIsDown(t *testing.T) {
	repo := &threadReplyRepo{turns: map[string]bool{"warm-1@sender.test": true}, err: errors.New("database unavailable")}
	inbox := &warmupInboxRepo{}
	s := &JobsService{WarmupRepo: repo, UniboxRepository: inbox, EmailRepository: warmupInboxEmailRepo{}}
	e := handTypedReply("<warm-1@sender.test>")
	if err := s.ingestNewEmail(context.Background(), e); !errors.Is(err, errWarmupVerification) || inbox.entries != 0 {
		t.Fatalf("error = %v, entries = %d; want the arrival held for retry", err, inbox.entries)
	}
	repo.err = nil
	if err := s.ingestNewEmail(context.Background(), e); err != nil || inbox.entries != 0 {
		t.Fatalf("retry: error = %v, entries = %d", err, inbox.entries)
	}
}

// A linked mailbox is warmed by the cloud, so the turns are the cloud's to
// recognise; the instance asks by ancestry and never stores a yes.
func TestHandTypedReplyOnALinkedMailboxAsksTheCloud(t *testing.T) {
	cloud := &warmupInboxCloud{threadOK: true}
	inbox := &warmupInboxRepo{}
	s := &JobsService{CloudLink: cloud, UniboxRepository: inbox, EmailRepository: warmupInboxEmailRepo{}}
	if err := s.ingestNewEmail(context.Background(), handTypedReply("<warm-1@cloud.test>")); err != nil || inbox.entries != 0 || cloud.threadCalls != 1 {
		t.Fatalf("error = %v, entries = %d, cloud asked %d times", err, inbox.entries, cloud.threadCalls)
	}
	cloud.threadOK = false
	if err := s.ingestNewEmail(context.Background(), handTypedReply("<real@prospect.test>")); err != nil || inbox.entries != 1 {
		t.Fatalf("ordinary reply: error = %v, entries = %d", err, inbox.entries)
	}
}

// A reply that got into the unibox before ancestry was checked is repaired by
// the sweep like any other leak: the row goes, and the copy in the customer's
// mailbox is filed.
func TestCleanupRepairsALeakedThreadReply(t *testing.T) {
	worker := uuid.New()
	e := *handTypedReply("<warm-1@sender.test>")
	inbox := &warmupCleanupInbox{events: []models.JobEventNewEmail{e}}
	repo := &threadReplyRepo{turns: map[string]bool{"warm-1@sender.test": true}}
	pub := &backfillPublisher{}
	s := &JobsService{UniboxRepository: inbox, WarmupRepo: repo, Publisher: pub,
		EmailRepository: backfillEmailRepo{account: &models.Email{WorkerID: &worker}}}
	sweep := &warmupInboxSweep{}
	done, err := s.cleanWarmupInboxBatch(context.Background(), sweep)
	if err != nil || !done || sweep.afterID != e.Message.ID {
		t.Fatalf("sweep: %v %v %v", sweep.afterID, done, err)
	}
	if len(inbox.deleted) != 1 || inbox.deleted[0] != e.Message.ID {
		t.Fatalf("deleted %v, want the leaked reply's unibox row", inbox.deleted)
	}
	if len(pub.actions) != 1 || pub.actions[0].RFCMessageID != "<reply-1@gmail.test>" || pub.workers[0] != worker {
		t.Fatalf("filed %+v on %v, want the reply filed by the mailbox's worker", pub.actions, pub.workers)
	}
}
