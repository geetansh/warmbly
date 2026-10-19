package msgraph

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/warmbly/warmbly/internal/errx"
)

type diagnosticRawRT struct {
	body   []byte
	status int
	path   string
}

func (r *diagnosticRawRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.path = req.URL.Path
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(bytes.NewReader(r.body)), Header: http.Header{}}, nil
}

func TestDiagnosticRawMessageFetchesOnlyNamedBoundedMessage(t *testing.T) {
	want := []byte("From: sender@example.test\r\n\r\nBounded diagnostic")
	rt := &diagnosticRawRT{body: want, status: http.StatusOK}
	c := &Client{hc: &http.Client{Transport: rt}}
	got, err := c.DiagnosticRawMessage(context.Background(), "provider/id", len(want))
	if err != nil || string(got) != string(want) || !strings.HasSuffix(rt.path, "/messages/provider/id/$value") {
		t.Fatalf("got=%q path=%s err=%v", got, rt.path, err)
	}
	if _, err = c.DiagnosticRawMessage(context.Background(), "provider/id", len(want)-1); !errors.Is(err, ErrRawMessageTooLarge) {
		t.Fatal("oversize raw MIME accepted")
	}
}

func TestDiagnosticRawMessagePreservesFailureClassification(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		c := &Client{hc: &http.Client{Transport: &diagnosticRawRT{body: []byte(`{"error":{"message":"provider-secret-error"}}`), status: status}}}
		_, err := c.DiagnosticRawMessage(t.Context(), "id", 1024)
		var mailErr *errx.MailError
		if !errors.As(err, &mailErr) || mailErr.Failure == nil || mailErr.Failure.Status != status || strings.Contains(err.Error(), "provider-secret-error") {
			t.Fatalf("status %d lost safe failure classification: %v", status, err)
		}
	}
}

func TestDiagnosticRawMessageDoesNotReturnProviderErrors(t *testing.T) {
	secret := "provider-secret-error"
	c := &Client{hc: &http.Client{Transport: &diagnosticRawRT{body: []byte(secret), status: http.StatusForbidden}}}
	_, err := c.DiagnosticRawMessage(context.Background(), "id", 1024)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("provider response escaped", err)
	}
}
