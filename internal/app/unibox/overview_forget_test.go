package unibox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
)

// fakeOpenRepo is the forward fake plus the one write opening a message makes.
type fakeOpenRepo struct {
	*fakeForwardRepo
	marked int
}

func (f *fakeOpenRepo) MarkSeenBulk(_ context.Context, _ uuid.UUID, ids []uuid.UUID, _ bool) ([]uuid.UUID, error) {
	f.marked++
	return ids, nil
}

// cachedOverviewService returns a service whose overview for orgID is already computed once.
func cachedOverviewService(t *testing.T, seen bool) (*uniboxService, uuid.UUID, uuid.UUID, *int) {
	t.Helper()
	s, msg := forwardService(t, nil)
	msg.Seen = seen
	s.uniboxRepository = &fakeOpenRepo{fakeForwardRepo: s.uniboxRepository.(*fakeForwardRepo)}
	computes := 0
	s.overview = newOverviewCache(func(context.Context, overviewScope) (*models.UniboxOverview, error) {
		computes++
		return &models.UniboxOverview{}, nil
	})
	orgID := uuid.New()
	if _, err := s.overview.get(context.Background(), overviewScope{OrgID: orgID}); err != nil {
		t.Fatal(err)
	}
	return s, orgID, msg.ID, &computes
}

func TestOpeningAnUnreadMessageForgetsTheOverview(t *testing.T) {
	s, orgID, id, computes := cachedOverviewService(t, false)
	if _, xerr := s.GetByID(context.Background(), orgID, id); xerr != nil {
		t.Fatal(xerr)
	}
	if _, err := s.overview.get(context.Background(), overviewScope{OrgID: orgID}); err != nil {
		t.Fatal(err)
	}
	if *computes != 2 {
		t.Errorf("overview computed %d times, want a fresh one after the open marked a message read", *computes)
	}
}

func TestOpeningWithoutChangingReadStateKeepsTheOverview(t *testing.T) {
	for name, open := range map[string]func(*uniboxService, uuid.UUID, uuid.UUID) *errx.Error{
		"an already read message": func(s *uniboxService, org, id uuid.UUID) *errx.Error {
			_, xerr := s.GetByID(context.Background(), org, id)
			return xerr
		},
		"a peek": func(s *uniboxService, org, id uuid.UUID) *errx.Error {
			_, xerr := s.Peek(context.Background(), org, id)
			return xerr
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, orgID, id, computes := cachedOverviewService(t, name == "an already read message")
			if xerr := open(s, orgID, id); xerr != nil {
				t.Fatal(xerr)
			}
			if _, err := s.overview.get(context.Background(), overviewScope{OrgID: orgID}); err != nil {
				t.Fatal(err)
			}
			if *computes != 1 {
				t.Errorf("overview computed %d times, want the cached one", *computes)
			}
		})
	}
}
