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

// sweepCloud answers the cleanup's delivery check per message, and can fail
// or interrupt on chosen ones.
type sweepCloud struct {
	warmup  map[string]bool
	failing map[string]bool
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

// manualClock is a pass's clock that only moves when a test moves it.
type manualClock struct{ at time.Time }

func (c *manualClock) now() time.Time { return c.at }

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

// A row that outruns the pass every time would otherwise stall every mailbox sorted after it.
func TestWarmupInboxCleanupHoldsARowThatKeepsOutrunningThePass(t *testing.T) {
	rows, mailboxes := sweepRows([2]string{"slow", "<a@test>"}, [2]string{"other", "<b@test>"})
	inbox := &warmupCleanupInbox{events: rows}
	cloud := &sweepCloud{warmup: map[string]bool{"<b@test>": true}}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}
	pass := func() error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cloud.onAsk = func(id string) {
			if id == "<a@test>" {
				cancel()
			}
		}
		_, err := s.cleanWarmupInboxBatch(ctx, sweep)
		return err
	}

	if err := pass(); err == nil || sweep.afterID != uuid.Nil || len(sweep.held) != 0 {
		t.Fatalf("first overrun: err=%v cursor=%v held=%v, want the row retried", err, sweep.afterID, sweep.held)
	}
	if err := pass(); err == nil || sweep.afterID != rows[0].Message.ID {
		t.Fatalf("second overrun: err=%v cursor=%v, want the mailbox held and the cursor past the row", err, sweep.afterID)
	}
	if _, held := sweep.held[mailboxes["slow"]]; !held {
		t.Fatal("a row that keeps outrunning the pass did not hold its mailbox")
	}
	cloud.onAsk = nil
	if done, err := s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done || len(inbox.deleted) != 1 || inbox.deleted[0] != rows[1].Message.ID {
		t.Fatalf("the mailbox after it: done=%v err=%v deleted=%v", done, err, inbox.deleted)
	}
}

func TestWarmupInboxCleanupStopsBeforeThePassDeadline(t *testing.T) {
	rows, _ := sweepRows([2]string{"m", "<a@test>"}, [2]string{"m", "<b@test>"})
	inbox := &warmupCleanupInbox{events: rows}
	clock := &manualClock{at: time.Now()}
	cloud := &sweepCloud{warmup: map[string]bool{"<a@test>": true, "<b@test>": true}}
	cloud.onAsk = func(string) { clock.at = clock.at.Add(2 * time.Second) }
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{now: clock.now}

	// Room for one candidate; after it the second would start inside the reserve.
	ctx, cancel := context.WithDeadline(context.Background(), clock.at.Add(warmupCleanupCandidateReserve+time.Second))
	defer cancel()
	done, err := s.cleanWarmupInboxBatch(ctx, sweep)
	if err != nil || done {
		t.Fatalf("a pass that stopped for its deadline reported done=%v err=%v", done, err)
	}
	if sweep.afterID != rows[0].Message.ID || cloud.asked["<b@test>"] != 0 {
		t.Fatalf("cursor=%v asked=%v, want the pass to stop after the first row", sweep.afterID, cloud.asked)
	}

	sweep.now = nil
	if done, err = s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || !done {
		t.Fatalf("next pass: done=%v err=%v", done, err)
	}
	if len(inbox.deleted) != 2 || cloud.asked["<a@test>"] != 1 {
		t.Fatalf("next pass deleted %v, asked %v", inbox.deleted, cloud.asked)
	}

	// A pass that starts inside the reserve does not even read a page.
	fresh := &warmupInboxSweep{now: clock.now}
	clock.at = time.Now()
	short, cancelShort := context.WithDeadline(context.Background(), clock.at.Add(warmupCleanupCandidateReserve/2))
	defer cancelShort()
	inbox.lists = 0
	if done, err = s.cleanWarmupInboxBatch(short, fresh); err != nil || done || fresh.afterID != uuid.Nil || inbox.lists != 0 {
		t.Fatalf("a pass with no room: done=%v err=%v cursor=%v pages read=%d", done, err, fresh.afterID, inbox.lists)
	}
}

func TestWarmupInboxCleanupCapsChecksAndPagesPerPass(t *testing.T) {
	var spec [][2]string
	for i := 0; i < warmupCleanupPassChecks+50; i++ {
		spec = append(spec, [2]string{"m", uuid.NewString()})
	}
	rows, _ := sweepRows(spec...)
	inbox := &warmupCleanupInbox{events: rows}
	cloud := &sweepCloud{}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}
	if done, err := s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || done {
		t.Fatalf("done=%v err=%v, want the pass to stop at its check cap", done, err)
	}
	if asked := len(cloud.asked); asked != warmupCleanupPassChecks || sweep.afterID != rows[warmupCleanupPassChecks-1].Message.ID {
		t.Fatalf("asked %d, cursor %v, want %d checks and the cursor on the last one", asked, sweep.afterID, warmupCleanupPassChecks)
	}

	// Rows a retry sweep skips cost no check, but a pass still reads a bounded number of pages.
	spec = nil
	for i := 0; i < (warmupCleanupPassPages+1)*warmupCleanupPageSize; i++ {
		spec = append(spec, [2]string{"skipped", uuid.NewString()})
	}
	spec = append(spec, [2]string{"held", "<late@test>"})
	rows, mailboxes := sweepRows(spec...)
	inbox = &warmupCleanupInbox{events: rows}
	cloud = &sweepCloud{}
	s = &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep = &warmupInboxSweep{only: map[uuid.UUID]heldMailbox{mailboxes["held"]: {}}}
	if done, err := s.cleanWarmupInboxBatch(context.Background(), sweep); err != nil || done {
		t.Fatalf("done=%v err=%v, want the pass to stop at its page cap", done, err)
	}
	if inbox.lists != warmupCleanupPassPages || len(cloud.asked) != 0 || sweep.afterID != rows[warmupCleanupPassPages*warmupCleanupPageSize-1].Message.ID {
		t.Fatalf("pages %d, asked %v, cursor %v", inbox.lists, cloud.asked, sweep.afterID)
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
	if h, held := sweep.held[mailboxes["slow"]]; !held || h.resume != rows[1].Message.ID {
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

	// It cleared s1 before failing, so it carries on straight away rather than backing off.
	now := time.Now()
	if next := sweep.finish(now); !next.Equal(now) {
		t.Fatalf("retry scheduled at %v, want now", next)
	}
	if sweep.afterID != rows[1].Message.ID || sweep.heldError() == nil {
		t.Fatalf("retry sweep starts after %v (want %v), held error %v", sweep.afterID, rows[1].Message.ID, sweep.heldError())
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
	if sweep.only != nil || sweep.afterID != uuid.Nil || sweep.heldError() != nil {
		t.Fatal("the daily sweep after a clean retry is still limited to, or reporting, the held mailbox")
	}
}

// A retry that gets through some rows before failing again keeps going; only one that finishes none backs off.
func TestWarmupInboxRetryBacksOffOnlyWithoutProgress(t *testing.T) {
	rows, mailboxes := sweepRows(
		[2]string{"m", "<1@test>"}, [2]string{"m", "<2@test>"}, [2]string{"m", "<3@test>"},
		[2]string{"m", "<4@test>"}, [2]string{"m", "<5@test>"},
	)
	inbox := &warmupCleanupInbox{events: rows}
	cloud := &sweepCloud{failing: map[string]bool{"<2@test>": true}}
	s := &JobsService{UniboxRepository: inbox, CloudLink: cloud}
	sweep := &warmupInboxSweep{}
	now := time.Now()
	sweepTo := func() time.Time {
		t.Helper()
		if done, _ := s.cleanWarmupInboxBatch(context.Background(), sweep); !done {
			t.Fatal("sweep did not reach the end")
		}
		return sweep.finish(now)
	}

	if next := sweepTo(); !next.Equal(now) {
		t.Fatalf("after progress then a failure: next %v, want now", next)
	}
	cloud.failing = map[string]bool{"<4@test>": true}
	if next := sweepTo(); !next.Equal(now) || sweep.only[mailboxes["m"]].resume != rows[2].Message.ID {
		t.Fatalf("a retry that progressed again: next %v resume %v, want now from %v", next, sweep.only[mailboxes["m"]].resume, rows[2].Message.ID)
	}
	// The first row it retries fails: nothing finished, so it backs off and doubles.
	for i, want := range []time.Duration{warmupCleanupRetryDelay, 2 * warmupCleanupRetryDelay, 4 * warmupCleanupRetryDelay} {
		if next := sweepTo(); !next.Equal(now.Add(want)) {
			t.Fatalf("retry %d without progress: next in %v, want %v", i, next.Sub(now), want)
		}
	}
	if sweep.heldError() == nil {
		t.Fatal("a mailbox waiting on its retry is not reported")
	}
}

func TestWarmupInboxSweepRetriesEachMailboxOnItsOwnClock(t *testing.T) {
	stuck, moving := uuid.New(), uuid.New()
	now := time.Now()
	sweep := &warmupInboxSweep{}
	sweep.hold(stuck, errors.New("no answer"))
	sweep.hold(moving, errors.New("no answer"))
	sweep.finished(moving)
	if next := sweep.finish(now); !next.Equal(now) || len(sweep.only) != 1 {
		t.Fatalf("next %v, scope %v, want the progressing mailbox alone, now", next, sweep.only)
	}
	if _, ok := sweep.only[moving]; !ok {
		t.Fatal("the progressing mailbox was not retried first")
	}
	if h, ok := sweep.waiting[stuck]; !ok || !h.due.Equal(now.Add(warmupCleanupRetryDelay)) {
		t.Fatalf("the stuck mailbox waits until %v, want %v", h.due, now.Add(warmupCleanupRetryDelay))
	}
	// The progressing one clears; the stuck one is next, at its own time.
	if next := sweep.finish(now); !next.Equal(now.Add(warmupCleanupRetryDelay)) {
		t.Fatalf("next %v, want the stuck mailbox's retry", next)
	}
	if _, ok := sweep.only[stuck]; !ok || len(sweep.only) != 1 {
		t.Fatalf("scope %v, want the stuck mailbox", sweep.only)
	}
	// Retries never outlast the next daily sweep, which covers every mailbox again.
	for sweep.only != nil {
		for m := range sweep.only {
			sweep.hold(m, errors.New("no answer"))
		}
		sweep.finish(now)
	}
	if len(sweep.waiting) != 0 || sweep.heldError() != nil {
		t.Fatalf("waiting %v past the daily sweep", sweep.waiting)
	}
}
