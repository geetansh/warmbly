package wmail

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/warmbly/warmbly/internal/client/msgraph"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

// What Graph's /$value returns for an Outlook.com feedback-loop report.
var graphFeedbackReportMIME = strings.ReplaceAll(`From: staff@hotmail.com
Subject: complaint about message from 203.0.113.9
Message-ID: <report-envelope@hotmail.com>
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="b1"

--b1
Content-Type: text/plain

This is an email abuse report for an email message received from IP 203.0.113.9.

--b1
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: Hotmail FBL
Version: 1
Original-Rcpt-To: <recipient@hotmail.com>

--b1
Content-Type: message/rfc822

From: box@outlook.test
To: recipient@hotmail.com
Subject: Quick question
Message-ID: <the-original-send@outlook.test>

Hi there
--b1--
`, "\n", "\r\n")

// What Graph's delta sync hands the worker for the same message: the rendered notice only.
func graphRenderedReport() *models.EmailMessageData {
	return &models.EmailMessageData{
		GmailID:  "report-1",
		From:     []string{"staff@hotmail.com"},
		Subject:  "complaint about message from 203.0.113.9",
		Flags:    []string{`Content-Type:multipart/report; report-type=feedback-report; boundary="b1"`},
		BodyHTML: "<p>This is an email abuse report for an email message received from IP 203.0.113.9.</p>",
	}
}

func rawMIMEServer(t *testing.T, status int, fetched *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*fetched = append(*fetched, r.URL.Path)
		if r.URL.Path != "/v1.0/me/messages/report-1/$value" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(graphFeedbackReportMIME))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGraphComplaintIsReadFromTheRawMessage(t *testing.T) {
	var fetched []string
	var events []captured
	var states []models.SyncState
	w := newGraphTestMail(t, rawMIMEServer(t, http.StatusOK, &fetched), &events, &states)

	got, err := w.complaintReport(t.Context(), graphRenderedReport())
	if err != nil || got == nil {
		t.Fatalf("no complaint from a Graph feedback report (fetched %v)", fetched)
	}
	if got.OriginalMessageID != "the-original-send@outlook.test" || got.ComplainedRecipient != "recipient@hotmail.com" || got.Provider != "Hotmail FBL" {
		t.Errorf("complaint = %+v", got)
	}
	if len(fetched) != 1 {
		t.Errorf("raw fetches = %v, want one", fetched)
	}
}

func TestGraphComplaintFetchesOnlyWhenTheSyncedBodyCannotTell(t *testing.T) {
	t.Run("a body that already carries the report parts", func(t *testing.T) {
		var fetched []string
		var events []captured
		var states []models.SyncState
		w := newGraphTestMail(t, rawMIMEServer(t, http.StatusOK, &fetched), &events, &states)
		msg := graphRenderedReport()
		msg.BodyPlain = "Feedback-Type: abuse\r\nMessage-ID: <the-original-send@outlook.test>\r\n"
		if got, err := w.complaintReport(t.Context(), msg); err != nil || got == nil || got.OriginalMessageID != "the-original-send@outlook.test" {
			t.Errorf("complaint = %+v", got)
		}
		if len(fetched) != 0 {
			t.Errorf("fetched %v for a body that already parsed", fetched)
		}
	})
	t.Run("a mailbox that is not Graph", func(t *testing.T) {
		w := &WMail{EmailType: models.InboxProviderGoogle}
		if got, err := w.complaintReport(t.Context(), graphRenderedReport()); err != nil || got != nil {
			t.Errorf("complaint = %+v from a rendered body alone", got)
		}
	})
	t.Run("ordinary mail", func(t *testing.T) {
		var fetched []string
		var events []captured
		var states []models.SyncState
		w := newGraphTestMail(t, rawMIMEServer(t, http.StatusOK, &fetched), &events, &states)
		msg := &models.EmailMessageData{GmailID: "report-1", From: []string{"ada@acme.com"}, Subject: "Re: your note", BodyPlain: "thanks"}
		if got, err := w.complaintReport(t.Context(), msg); err != nil || got != nil {
			t.Errorf("complaint = %+v", got)
		}
		if len(fetched) != 0 {
			t.Errorf("fetched %v for ordinary mail", fetched)
		}
	})
	t.Run("a raw fetch that fails", func(t *testing.T) {
		var fetched []string
		var events []captured
		var states []models.SyncState
		w := newGraphTestMail(t, rawMIMEServer(t, http.StatusInternalServerError, &fetched), &events, &states)
		if got, err := w.complaintReport(t.Context(), graphRenderedReport()); err == nil || got != nil {
			t.Errorf("complaint = %+v after a failed fetch", got)
		}
	})
}

func TestGraphComplaintRetriesBeforeAdmission(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		for _, backfill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/backfill=%t", status, backfill), func(t *testing.T) {
				fetches := 0
				reportJSON := func() map[string]any {
					msg := graphMessageJSON("report-1")
					msg["from"] = map[string]any{"emailAddress": map[string]any{"address": "staff@hotmail.com"}}
					msg["subject"] = graphRenderedReport().Subject
					msg["body"] = map[string]any{"contentType": "html", "content": graphRenderedReport().BodyHTML}
					return msg
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1.0/me/messages/report-1/$value":
						fetches++
						if fetches == 1 {
							w.Header().Set("Retry-After", "1")
							w.WriteHeader(status)
							return
						}
						_, _ = w.Write([]byte(graphFeedbackReportMIME))
					case "/v1.0/me/messages/report-1":
						_ = json.NewEncoder(w).Encode(reportJSON())
					case "/v1.0/me/mailFolders/inbox/messages":
						_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{reportJSON()}})
					default:
						values := []any{}
						if !backfill && r.URL.Path == "/v1.0/me/mailFolders/inbox/messages/delta" {
							values = append(values, map[string]any{"id": "report-1", "isRead": false})
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"value": values,
							"@odata.deltaLink": "https://graph.microsoft.com" + r.URL.Path + "?$deltatoken=done"})
					}
				}))
				t.Cleanup(srv.Close)
				var events []captured
				var states []models.SyncState
				w := newGraphTestMail(t, srv, &events, &states)
				store := &retainedArrivalStore{body: map[string][]byte{}}
				w.Storage = store
				if !backfill {
					w.tracker.state.BackfillStatus = models.SyncBackfillComplete
				}
				mapping := &recoveryMessageMap{data: map[string]repository.EmailMessageData{}}
				w.EmailMessageMapRepository = mapping
				seed := w.GraphData.Client.DeltaLinks[msgraph.FolderInbox]
				if err := w.SyncGraph(t.Context()); err == nil || err.Failure == nil || err.Failure.Status != status {
					t.Fatalf("first sync = %v, want provider %d", err, status)
				}
				cursorHeld := w.GraphData.Client.DeltaLinks[msgraph.FolderInbox] == seed
				if backfill {
					cursorHeld = !w.tracker.folder(msgraph.FolderInbox).Done
				}
				complaintEmitted := false
				for _, event := range events {
					complaintEmitted = complaintEmitted || event.eventType == models.JobEventTypeInboundComplaint
				}
				if fetches != 1 || len(mapping.data) != 0 || len(importedIDs(events)) != 0 || complaintEmitted || !cursorHeld || len(store.body) != 0 {
					t.Fatalf("failed fetch admitted the report: fetches=%d maps=%v events=%v cursor=%s", fetches, mapping.data, events, w.GraphData.Client.DeltaLinks[msgraph.FolderInbox])
				}
				for range 2 {
					if err := w.SyncGraph(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				complaints := 0
				for _, event := range events {
					if event.eventType == models.JobEventTypeInboundComplaint {
						complaints++
						got := event.body.(*models.JobEventInboundComplaint)
						if got.UserID != w.UserID || got.EmailID != w.ID || got.OriginalMessageID != "the-original-send@outlook.test" {
							t.Fatalf("complaint lost ownership or original send: %+v", got)
						}
					}
				}
				if fetches != 2 || len(mapping.data) != 1 || complaints != 1 || len(importedIDs(events)) != 1 || w.GraphData.Client.DeltaLinks[msgraph.FolderInbox] == seed {
					t.Fatalf("retry did not emit once: fetches=%d maps=%v complaints=%d arrivals=%v", fetches, mapping.data, complaints, importedIDs(events))
				}
			})
		}
	}
}

func TestGraphComplaintDoesNotRetryPermanentFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var fetched []string
			var events []captured
			var states []models.SyncState
			w := newGraphTestMail(t, rawMIMEServer(t, status, &fetched), &events, &states)
			got, err := w.complaintReport(t.Context(), graphRenderedReport())
			if got != nil || (err == nil) != (status == http.StatusNotFound) {
				t.Fatalf("status %d: complaint=%+v err=%v", status, got, err)
			}
		})
	}
	t.Run("oversized raw message", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", complaintMIMEBytes+1)))
		}))
		t.Cleanup(srv.Close)
		var events []captured
		var states []models.SyncState
		w := newGraphTestMail(t, srv, &events, &states)
		if got, err := w.complaintReport(t.Context(), graphRenderedReport()); got != nil || err != nil {
			t.Fatalf("oversize message pins admission: complaint=%+v err=%v", got, err)
		}
	})
}
