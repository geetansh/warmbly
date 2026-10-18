package jobs

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
)

// sweepCloud answers the cleanup's delivery check per message, and can fail,
// stall or interrupt on chosen ones.
type sweepCloud struct {
	warmup  map[string]bool
	failing map[string]bool
	delay   time.Duration
	onAsk   func(messageID string)
	asked   map[string]int
}

func (c *sweepCloud) CheckEnrollment(context.Context, uuid.UUID) (bool, error) { return true, nil }

func (c *sweepCloud) VerifyWarmupToken(context.Context, uuid.UUID, string) (bool, error) {
	return false, nil
}

func (c *sweepCloud) IsCloudWarmupDelivery(ctx context.Context, _ uuid.UUID, _, messageID, _ string) (bool, error) {
	if c.asked == nil {
		c.asked = map[string]int{}
	}
	c.asked[messageID]++
	if c.onAsk != nil {
		c.onAsk(messageID)
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if c.failing[messageID] {
		return false, errors.New("cloud could not be reached")
	}
	return c.warmup[messageID], nil
}

func (c *sweepCloud) IsCloudWarmupThreadReply(context.Context, uuid.UUID, string, []string) (bool, error) {
	return false, nil
}

// sweepRows builds unibox rows in id order, one per (mailbox, Message-ID) pair.
func sweepRows(rows ...[2]string) ([]models.JobEventNewEmail, map[string]uuid.UUID) {
	ids := make([]uuid.UUID, len(rows))
	for i := range ids {
		ids[i] = uuid.New()
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	mailboxes := map[string]uuid.UUID{}
	out := make([]models.JobEventNewEmail, len(rows))
	for i, r := range rows {
		if _, ok := mailboxes[r[0]]; !ok {
			mailboxes[r[0]] = uuid.New()
		}
		out[i] = models.JobEventNewEmail{UserID: uuid.New(), Message: &models.EmailMessageStoreData{
			ID: ids[i], EmailID: mailboxes[r[0]], MessageID: r[1],
		}}
	}
	return out, mailboxes
}

func (r *warmupCleanupInbox) remaining() *warmupCleanupInbox {
	var kept []models.JobEventNewEmail
	for _, e := range r.events {
		if !slices.Contains(r.deleted, e.Message.ID) {
			kept = append(kept, e)
		}
	}
	return &warmupCleanupInbox{events: kept, deleted: r.deleted}
}

func TestWarmupInboxCleanupKeepsProgressWhenThePassIsInterrupted(t *testing.T) {
	rows, _ := sweepRows([2]string{"m", "<a@test>"}, [2]string{"m", "<b@test>"}, [2]string{"m", "<c@test>"})
	inbox := &warmupCleanupInbox{events: rows}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cloud := &sweepCloud{warmup: map[string]bool{"<a@test>": true, "<b@test>": true, "<c@test>": true}}
	cloud.onAsk = func(id string) {
		if id == "<b@test>" {
			cancel()
		}
	}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}

	done, err := s.cleanWarmupInboxBatch(ctx, sweep)
	if err == nil || done {
		t.Fatalf("an interrupted pass reported done=%v err=%v", done, err)
	}
	if sweep.afterID != rows[0].Message.ID {
		t.Fatalf("cursor = %v, want the last row fully processed (%v)", sweep.afterID, rows[0].Message.ID)
	}
	if len(sweep.held) != 0 {
		t.Fatal("an interrupted pass blamed the mailbox it was checking")
	}
	if len(inbox.deleted) != 1 || inbox.deleted[0] != rows[0].Message.ID {
		t.Fatalf("deleted %v, want only the first row", inbox.deleted)
	}

	cloud.onAsk = nil
	inbox = inbox.remaining()
	s.UniboxRepository = inbox
	if done, err = s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done {
		t.Fatalf("resumed pass: done=%v err=%v", done, err)
	}
	if cloud.asked["<a@test>"] != 1 || cloud.asked["<b@test>"] != 2 || cloud.asked["<c@test>"] != 1 {
		t.Fatalf("resumed pass re-asked the cloud about finished work: %v", cloud.asked)
	}
	if len(inbox.deleted) != 3 {
		t.Fatalf("deleted %d rows after resuming, want 3", len(inbox.deleted))
	}
}

func TestWarmupInboxCleanupStopsBeforeThePassDeadline(t *testing.T) {
	rows, _ := sweepRows([2]string{"m", "<a@test>"}, [2]string{"m", "<b@test>"})
	inbox := &warmupCleanupInbox{events: rows}
	cloud := &sweepCloud{warmup: map[string]bool{"<a@test>": true, "<b@test>": true}, delay: 400 * time.Millisecond}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}

	// Room for one slow candidate; the second would start inside the reserve.
	ctx, cancel := context.WithTimeout(context.Background(), warmupCleanupCandidateReserve+250*time.Millisecond)
	defer cancel()
	done, err := s.cleanWarmupInboxBatch(ctx, sweep)
	if err != nil || done {
		t.Fatalf("a pass that stopped for its deadline reported done=%v err=%v", done, err)
	}
	if sweep.afterID != rows[0].Message.ID || cloud.asked["<b@test>"] != 0 {
		t.Fatalf("cursor=%v asked=%v, want the pass to stop after the first row", sweep.afterID, cloud.asked)
	}

	cloud.delay = 0
	if done, err = s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done {
		t.Fatalf("next pass: done=%v err=%v", done, err)
	}
	if len(inbox.deleted) != 2 || cloud.asked["<a@test>"] != 1 {
		t.Fatalf("next pass deleted %v, asked %v", inbox.deleted, cloud.asked)
	}

	// A pass that starts inside the reserve asks nothing at all.
	short, cancelShort := context.WithTimeout(context.Background(), warmupCleanupCandidateReserve/2)
	defer cancelShort()
	fresh := &warmupInboxSweep{}
	if done, err = s.cleanWarmupInboxBatch(short, fresh); err != nil || done || fresh.afterID != uuid.Nil {
		t.Fatalf("a pass with no room: done=%v err=%v cursor=%v", done, err, fresh.afterID)
	}
}

func TestWarmupInboxCleanupHoldsAFailingMailboxWithoutStallingOthers(t *testing.T) {
	rows, mailboxes := sweepRows(
		[2]string{"slow", "<s1@test>"},
		[2]string{"other", "<o1@test>"},
		[2]string{"slow", "<s2@test>"},
		[2]string{"other", "<o2@test>"},
		[2]string{"slow", "<s3@test>"},
	)
	inbox := &warmupCleanupInbox{events: rows}
	cloud := &sweepCloud{
		warmup:  map[string]bool{"<s1@test>": true, "<o1@test>": true, "<s2@test>": true, "<s3@test>": true},
		failing: map[string]bool{"<s2@test>": true},
	}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}

	done, err := s.cleanWarmupInboxBatch(context.Background(), sweep)
	if err == nil || !done {
		t.Fatalf("one failing mailbox: done=%v err=%v, want the sweep finished with the failure reported", done, err)
	}
	if resume, held := sweep.held[mailboxes["slow"]]; !held || resume != rows[1].Message.ID {
		t.Fatalf("held %v, want the failing mailbox resuming after %v", sweep.held, rows[1].Message.ID)
	}
	if cloud.asked["<s3@test>"] != 0 {
		t.Fatal("the cloud was asked again about a mailbox already failing this sweep")
	}
	if cloud.asked["<o1@test>"] != 1 || cloud.asked["<o2@test>"] != 1 {
		t.Fatalf("the other mailbox was not swept: %v", cloud.asked)
	}
	if !slices.Contains(inbox.deleted, rows[1].Message.ID) || slices.Contains(inbox.deleted, rows[2].Message.ID) {
		t.Fatalf("deleted %v, want the other mailbox's warmup cleared and the unverified row kept", inbox.deleted)
	}

	now := time.Now()
	if next := sweep.finish(now); !next.Equal(now.Add(warmupCleanupRetryDelay)) {
		t.Fatalf("retry scheduled at %v, want %v", next, now.Add(warmupCleanupRetryDelay))
	}
	if sweep.afterID != rows[1].Message.ID {
		t.Fatalf("retry sweep starts after %v, want %v", sweep.afterID, rows[1].Message.ID)
	}

	cloud.failing = nil
	inbox = inbox.remaining()
	s.UniboxRepository = inbox
	if done, err = s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done {
		t.Fatalf("retry sweep: done=%v err=%v", done, err)
	}
	if cloud.asked["<s2@test>"] != 2 || cloud.asked["<s3@test>"] != 1 || cloud.asked["<o2@test>"] != 1 || cloud.asked["<s1@test>"] != 1 {
		t.Fatalf("retry sweep asked %v, want only the held mailbox's unfinished rows", cloud.asked)
	}
	if !slices.Contains(inbox.deleted, rows[2].Message.ID) || !slices.Contains(inbox.deleted, rows[4].Message.ID) {
		t.Fatalf("retry sweep did not clear the held mailbox: %v", inbox.deleted)
	}
	if next := sweep.finish(now.Add(time.Hour)); !next.Equal(now.Add(warmupCleanupInterval)) {
		t.Fatalf("after a clean retry the next sweep is at %v, want the daily sweep at %v", next, now.Add(warmupCleanupInterval))
	}
	if sweep.only != nil || sweep.afterID != uuid.Nil {
		t.Fatal("the daily sweep after a clean retry is still limited to the held mailboxes")
	}
}

func TestWarmupInboxSweepBacksOffRetriesUntilTheDailySweep(t *testing.T) {
	mailbox, resume := uuid.New(), uuid.New()
	sweep := &warmupInboxSweep{}
	now := time.Now()
	daily := now.Add(warmupCleanupInterval)
	sweep.hold(mailbox)
	next := sweep.finish(now)
	want := warmupCleanupRetryDelay
	for next.Before(daily) {
		if got := next.Sub(now); got != want {
			t.Fatalf("retry after %v, want %v", got, want)
		}
		if sweep.only == nil {
			t.Fatal("a retry sweep is not limited to the held mailbox")
		}
		now = next
		sweep.afterID = resume
		sweep.hold(mailbox)
		next = sweep.finish(now)
		want *= 2
	}
	if !next.Equal(daily) || sweep.only != nil {
		t.Fatalf("after the last retry the next sweep is at %v limited=%v, want the daily sweep at %v", next, sweep.only != nil, daily)
	}
	sweep.hold(mailbox)
	if sweep.finish(next); sweep.only == nil || sweep.retries != 1 {
		t.Fatal("a daily sweep that holds a mailbox does not restart the retries")
	}
}
