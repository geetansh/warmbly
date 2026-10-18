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

// enrollmentErrorRepo keeps a refusal the way the SQL does: a retry leaves it, acceptance clears it.
type enrollmentErrorRepo struct {
	*enrollmentFaultRepo
}

func (r *enrollmentErrorRepo) RecordEnrollmentError(_ context.Context, _ uuid.UUID, code, message string) (bool, error) {
	if r.mailbox == nil || r.mailbox.EnrollmentState != "pending_enroll" {
		return false, nil
	}
	if e := r.mailbox.EnrollmentError; e != nil && e.Code == code && e.Message == message {
		return false, nil
	}
	r.mailbox.EnrollmentError = &models.CloudLinkEnrollmentError{Code: code, Message: message}
	return true, nil
}

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

func (r *enrollmentErrorRepo) Enroll(ctx context.Context, account, remote, instance uuid.UUID, managed bool) (*models.CloudLinkMailbox, error) {
	m, err := r.enrollmentFaultRepo.Enroll(ctx, account, remote, instance, managed)
	if m != nil {
		m.EnrollmentError = nil
	}
	return m, err
}

func TestEnrollmentKeepsOnlyCloudsOwnRefusal(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	ctx := context.Background()
	org, account, instance := uuid.New(), uuid.New(), uuid.New()
	var mode atomic.Value
	mode.Store("conflict")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		switch mode.Load() {
		case "conflict":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"pool_link_mailbox_conflict","message":"The address exists in this Cloud workspace."}`))
		case "server":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"code":"pool_link_store_failed","message":"pq: relation does not exist"}`))
		case "proxy":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>Bad Gateway</html>`))
		case "throttle":
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"pool_link_rate_limited","message":"Slow down."}`))
		case "garbled":
			_, _ = w.Write([]byte(`not json`))
		case "drop":
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
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
	var announced int
	s.OnEnrollmentRefusalChange(func(_ context.Context, id uuid.UUID) {
		if id != account {
			t.Errorf("announced another mailbox: %s", id)
		}
		announced++
	})
	listed := func() models.CloudLinkMailboxRow {
		t.Helper()
		rows, xerr := s.ListMailboxes(ctx, org)
		if xerr != nil || len(rows) != 1 {
			t.Fatalf("list: %v %+v", xerr, rows)
		}
		return rows[0]
	}
	kept := func(when string) {
		t.Helper()
		row := listed()
		if row.EnrollmentState != "pending_enroll" || row.EnrollmentError == nil ||
			row.EnrollmentError.Code != "pool_link_mailbox_conflict" || row.EnrollmentError.Message != "The address exists in this Cloud workspace." {
			t.Fatalf("%s: the conflict was not kept: %+v", when, row)
		}
	}

	for range 2 {
		if _, xerr := s.Enroll(ctx, org, account); xerr == nil || xerr.Identifier != "pool_link_mailbox_conflict" {
			t.Fatalf("refusal not returned: %v", xerr)
		}
	}
	kept("after the refusal")
	if announced != 1 {
		t.Fatalf("a repeated refusal was announced again: %d", announced)
	}

	for _, blip := range []string{"drop", "server", "proxy", "throttle", "garbled"} {
		mode.Store(blip)
		if _, xerr := s.Enroll(ctx, org, account); xerr == nil {
			t.Fatalf("%s: enrollment reported success", blip)
		}
		kept(blip)
	}
	if announced != 1 {
		t.Fatalf("a failure that is not Cloud's refusal changed the kept one: %d", announced)
	}

	r.mailbox.EnrollmentState = "pending_remove"
	if row := listed(); row.EnrollmentError != nil {
		t.Fatalf("a removal showed the old enrollment refusal: %+v", row.EnrollmentError)
	}
	r.mailbox.EnrollmentState = "pending_enroll"

	mode.Store("accept")
	if _, xerr := s.Enroll(ctx, org, account); xerr != nil {
		t.Fatal(xerr)
	}
	if row := listed(); row.EnrollmentState != "active" || row.EnrollmentError != nil {
		t.Fatalf("accepted enrollment kept the refusal: %+v", row)
	}
	if announced != 2 {
		t.Fatalf("clearing the refusal was not announced: %d", announced)
	}
}
