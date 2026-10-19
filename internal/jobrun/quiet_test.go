package jobrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/warmbly/warmbly/internal/models"
)

type recordingStore struct {
	Store
	finished []error
}

func (s *recordingStore) MarkStarted(context.Context, string, time.Time) error { return nil }

func (s *recordingStore) MarkFinished(_ context.Context, _ string, _, _ time.Time, runErr error) error {
	s.finished = append(s.finished, runErr)
	return nil
}

func (s *recordingStore) List(context.Context) ([]models.ScheduledJobRun, error) { return nil, nil }

// A standing condition stays on the panel on every run without being reported again.
func TestQuietErrorsAreStillRecorded(t *testing.T) {
	prev, prevService := current()
	t.Cleanup(func() { Configure(prev, prevService) })
	st := &recordingStore{}
	Configure(st, "test")

	held := errors.New("2 mailboxes held for retry")
	Run(context.Background(), "job", func(context.Context) error { return Quiet(held) })
	if len(st.finished) != 1 || !errors.Is(st.finished[0], held) || st.finished[0].Error() != held.Error() {
		t.Fatalf("recorded %v, want the held condition with its own message", st.finished)
	}
	if Quiet(nil) != nil {
		t.Fatal("Quiet(nil) is not nil")
	}
}
