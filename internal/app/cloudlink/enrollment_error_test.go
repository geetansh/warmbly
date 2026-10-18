package cloudlink

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/models"
)

type enrollmentErrorRepo struct {
	*enrollmentFaultRepo
}

func (r *enrollmentErrorRepo) RecordEnrollmentError(_ context.Context, _ uuid.UUID, code, message string) error {
	if r.mailbox != nil && r.mailbox.EnrollmentState == "pending_enroll" {
		r.mailbox.EnrollmentError = &models.CloudLinkEnrollmentError{Code: code, Message: message}
	}
	return nil
}

// A retry keeps the last refusal, and accepting leaves it in the row, so only the listing hides it.
func (r *enrollmentErrorRepo) BeginEnrollment(ctx context.Context, account, remote, instance uuid.UUID) error {
	var previous *models.CloudLinkEnrollmentError
	if r.mailbox != nil {
		previous = r.mailbox.EnrollmentError
	}
	if err := r.enrollmentFaultRepo.BeginEnrollment(ctx, account, remote, instance); err != nil {
		return err
	}
	r.mailbox.EnrollmentError = previous
	return nil
}

func TestRefusedEnrollmentKeepsCloudsAnswerOnThePendingRow(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	ctx := context.Background()
	org, account, instance := uuid.New(), uuid.New(), uuid.New()
	var refuse atomic.Bool
	refuse.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && refuse.Load():
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"pool_link_mailbox_conflict","message":"The address exists in this Cloud workspace."}`))
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	r := &enrollmentErrorRepo{&enrollmentFaultRepo{stubLinkRepo: &stubLinkRepo{
		link: &models.CloudLink{InstanceID: instance, OrganizationID: &org, CloudURL: srv.URL, Token: "t"},
	}}}
	s := NewService(r, enrollmentEmails{stubEmails{account: &models.Email{
		ID: account, OrganizationID: &org, Status: "active", Provider: "smtp_imap",
	}}}, nil).(*service)
	listed := func() models.CloudLinkMailboxRow {
		t.Helper()
		rows, xerr := s.ListMailboxes(ctx, org)
		if xerr != nil || len(rows) != 1 {
			t.Fatalf("list: %v %+v", xerr, rows)
		}
		return rows[0]
	}

	if _, xerr := s.Enroll(ctx, org, account); xerr == nil || xerr.Identifier != "pool_link_mailbox_conflict" {
		t.Fatalf("refusal not returned: %v", xerr)
	}
	row := listed()
	if row.EnrollmentState != "pending_enroll" || row.EnrollmentError == nil ||
		row.EnrollmentError.Code != "pool_link_mailbox_conflict" || row.EnrollmentError.Message != "The address exists in this Cloud workspace." {
		t.Fatalf("pending row lost Cloud's answer: %+v", row)
	}

	r.mailbox.EnrollmentState = "pending_remove"
	if row := listed(); row.EnrollmentError != nil {
		t.Fatalf("a removal showed the old enrollment refusal: %+v", row.EnrollmentError)
	}
	r.mailbox.EnrollmentState = "pending_enroll"

	refuse.Store(false)
	if _, xerr := s.Enroll(ctx, org, account); xerr != nil {
		t.Fatal(xerr)
	}
	if row := listed(); row.EnrollmentState != "active" || row.EnrollmentError != nil {
		t.Fatalf("accepted enrollment kept the refusal: %+v", row)
	}
}
