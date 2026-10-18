package repository

import (
	"context"
	"testing"
)

func TestLiveEnrollmentErrorStaysOnThePendingIntentUntilCloudAccepts(t *testing.T) {
	f := newPoolLinkFixture(t)
	ctx := context.Background()
	instance := liveCloudLink(t, f)
	r := NewCloudLinkRepository(f.pool, nil).(*cloudLinkRepository)
	if err := r.BeginEnrollment(ctx, f.sender, f.sender, instance); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordEnrollmentError(ctx, f.sender, "pool_link_mailbox_conflict", "The address exists in this Cloud workspace."); err != nil {
		t.Fatal(err)
	}
	m, err := r.GetByAccount(ctx, f.sender)
	if err != nil || m == nil || m.EnrollmentError == nil || m.EnrollmentError.Code != "pool_link_mailbox_conflict" {
		t.Fatalf("refusal not kept on the pending intent: %+v %v", m, err)
	}
	if err := r.BeginEnrollment(ctx, f.sender, f.sender, instance); err != nil {
		t.Fatal(err)
	}
	if m, _ = r.GetByAccount(ctx, f.sender); m.EnrollmentError == nil {
		t.Fatal("a retry erased the refusal before Cloud answered")
	}
	if m, err = r.Enroll(ctx, f.sender, f.sender, instance, false); err != nil || m.EnrollmentState != "active" || m.EnrollmentError != nil {
		t.Fatalf("accepted enrollment kept the refusal: %+v %v", m, err)
	}
	if err := r.RecordEnrollmentError(ctx, f.sender, "cloud_link_unreachable", "Warmbly Cloud could not be reached."); err != nil {
		t.Fatal(err)
	}
	if m, _ = r.GetByAccount(ctx, f.sender); m.EnrollmentError != nil {
		t.Fatal("a refusal was recorded on an active enrollment")
	}
}
