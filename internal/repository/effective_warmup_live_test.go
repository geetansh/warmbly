package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/warmbly/warmbly/internal/models"
)

// A mailbox Warmbly Cloud warms keeps email_accounts.warmup NULL. Every read
// that reports warmup must still say it is warming:
//
//	WARMBLY_TEST_DB=postgres://warmbly:warmbly@localhost:15432/warmbly_dev?sslmode=disable \
//	  go test ./internal/repository/ -run LiveEffectiveWarmup -v
func TestLiveEffectiveWarmupReadsCloudState(t *testing.T) {
	f := newPoolLinkFixture(t)
	ctx := context.Background()
	handle, _ := liveContactDB(t)
	instance := liveCloudLink(t, f)
	if _, err := f.pool.Exec(ctx, `INSERT INTO cloud_link_mailboxes (email_account_id, remote_id, instance_id) VALUES ($1, $2, $3)`,
		f.sender, uuid.New(), instance); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM cloud_link_mailboxes WHERE email_account_id = $1`, f.sender)
	})

	emails := NewEmailRepostory(handle, nil)
	state := func(id uuid.UUID) models.EffectiveWarmup {
		t.Helper()
		e, xerr := emails.Get(ctx, f.org.String(), id.String())
		if xerr != nil {
			t.Fatal(xerr)
		}
		if e.WarmupEffective == nil {
			t.Fatal("Get left warmup_effective empty")
		}
		return *e.WarmupEffective
	}

	if w := state(f.sender); w.Source != models.WarmupSourceCloud || w.State != models.WarmupStateUnknown {
		t.Fatalf("enrolled before any report = %+v, want cloud/unknown", w)
	}
	if w := state(f.recipient); w.Source != models.WarmupSourceLocal || w.State != models.WarmupStateOff {
		t.Fatalf("unenrolled mailbox = %+v, want local/off", w)
	}

	mirror := NewCloudLinkRepository(f.pool, nil).(CloudWarmupStateRepository)
	started := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Second)
	volume, target := 5, 12
	observed := time.Now()
	report := func(at time.Time, s models.WarmupState) []uuid.UUID {
		t.Helper()
		changed, err := mirror.MirrorCloudWarmupState(ctx, at, map[uuid.UUID]models.CloudWarmupObserved{
			f.sender: {State: s, StartedAt: &started, Volume: &volume, Target: &target},
		})
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if changed := report(observed, models.WarmupStateActive); len(changed) != 1 || changed[0] != f.sender {
		t.Fatalf("first report changed %v, want the sender", changed)
	}
	if changed := report(observed.Add(time.Minute), models.WarmupStateActive); len(changed) != 0 {
		t.Fatalf("same state announced again: %v", changed)
	}
	if changed := report(observed.Add(-time.Hour), models.WarmupStatePaused); len(changed) != 0 {
		t.Fatalf("an older report overwrote a newer one: %v", changed)
	}
	w := state(f.sender)
	if w.Source != models.WarmupSourceCloud || w.State != models.WarmupStateActive || w.StartedAt == nil || !w.StartedAt.Equal(started) {
		t.Fatalf("Get after a report = %+v, want cloud/active since %s", w, started)
	}

	page, xerr := emails.Search(ctx, f.org.String(), "", nil, nil, 10, nil)
	if xerr != nil {
		t.Fatal(xerr)
	}
	for _, e := range page.Data {
		if e.ID == f.sender && (e.WarmupEffective == nil || !e.WarmupEffective.Active() || e.CloudWarmup == nil || *e.CloudWarmup.Volume != 5) {
			t.Fatalf("Search lost the Cloud warmup: %+v %+v", e.WarmupEffective, e.CloudWarmup)
		}
	}

	usage, xerr := NewAnalyticsRepository(handle).GetEmailAccountCounts(ctx, f.org)
	if xerr != nil {
		t.Fatal(xerr)
	}
	if usage.InWarmup != 1 {
		t.Fatalf("in_warmup = %d, want the Cloud-warmed mailbox counted", usage.InWarmup)
	}

	snap, err := NewAdvisorRepository(handle).(*advisorRepository).loadMailboxes(ctx, f.org, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snap {
		if m.ID == f.sender && (!m.WarmupActive || !m.WarmupCloud || m.WarmupUnknown) {
			t.Fatalf("advisor read the Cloud mailbox as %+v", m)
		}
		if m.ID == f.recipient && (m.WarmupActive || m.WarmupCloud) {
			t.Fatalf("advisor read the local mailbox as %+v", m)
		}
	}

	if err := mirror.ForgetCloudWarmupState(ctx, f.sender); err != nil {
		t.Fatal(err)
	}
	if w := state(f.sender); w.State != models.WarmupStateUnknown {
		t.Fatalf("a mailbox Cloud stopped listing = %+v, want unknown, never off", w)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE cloud_link_mailboxes SET enrollment_state = 'pending_remove' WHERE email_account_id = $1`, f.sender); err != nil {
		t.Fatal(err)
	}
	if w := state(f.sender); w.Source != models.WarmupSourceLocal {
		t.Fatalf("a mailbox leaving Cloud = %+v, want local", w)
	}
}
