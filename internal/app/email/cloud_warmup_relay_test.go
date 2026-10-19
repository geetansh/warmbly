package email

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

// relayEmailRepo is the mailbox row the relay reads and the local write it makes.
type relayEmailRepo struct {
	repository.EmailRepository
	account *models.Email
	updates []models.UpdateEmail
	cycles  []string
}

func (r *relayEmailRepo) Get(context.Context, string, string) (*models.Email, *errx.Error) {
	return r.account, nil
}

func (r *relayEmailRepo) Update(_ context.Context, _, _ string, u *models.UpdateEmail) (*models.Email, *errx.Error) {
	if u.Name == nil && u.Timezone == nil && u.WarmupTag == nil && !touchesCloudWarmup(u) {
		return nil, errx.ErrNotEnough
	}
	r.updates = append(r.updates, *u)
	return r.account, nil
}

func (r *relayEmailRepo) SetWarmupLifecycle(_ context.Context, _, _, action string) (*models.Email, *errx.Error) {
	r.cycles = append(r.cycles, action)
	return r.account, nil
}

type stubRelay struct {
	wants      []models.PoolLinkWarmupSettings
	lifecycles []string
	actions    []string
	err        *errx.Error
}

func (s *stubRelay) RelayWarmupSettings(_ context.Context, _, _ uuid.UUID, want models.PoolLinkWarmupSettings, lifecycle string) (*models.PoolLinkWarmupSettings, *errx.Error) {
	if s.err != nil {
		return nil, s.err
	}
	s.wants = append(s.wants, want)
	s.lifecycles = append(s.lifecycles, lifecycle)
	return &want, nil
}

func (s *stubRelay) SetLifecycle(_ context.Context, _, _ uuid.UUID, action string) (*models.CloudLinkMailboxRow, *errx.Error) {
	if s.err != nil {
		return nil, s.err
	}
	s.actions = append(s.actions, action)
	return &models.CloudLinkMailboxRow{}, nil
}

func cloudRelayFixture(state string) (*emailService, *relayEmailRepo, *stubRelay) {
	org, id := uuid.New(), uuid.New()
	repo := &relayEmailRepo{account: &models.Email{ID: id, OrganizationID: &org, WarmupBase: 10, WarmupMax: 40, WarmupIncrease: 1, WarmupReplyRate: 30, WarmupStartTime: "08:00:00", WarmupEndTime: "20:00:00", OrgTimezone: "Asia/Calcutta"}}
	relay := &stubRelay{}
	s := &emailService{emailRepository: repo, cloudWarmup: relay, cloudLink: &stubCloudLinkRepo{row: &models.CloudLinkMailbox{EmailAccountID: id, EnrollmentState: state}}}
	return s, repo, relay
}

func ptr[T any](v T) *T { return &v }

func TestCloudWarmupEditGoesToTheCloudAndOnlyTheRestIsWrittenHere(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	_, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{
		Name: ptr("Sales"), WarmupBase: ptr(15), WarmupDays: ptr(0), WarmupStartTime: ptr("09:00"), Timezone: ptr(""),
	})
	if xerr != nil {
		t.Fatal(xerr)
	}
	want := models.PoolLinkWarmupSettings{Base: 15, Days: models.WarmupEveryDay, StartTime: "09:00", Timezone: "Asia/Calcutta"}
	if len(relay.wants) != 1 || relay.wants[0] != want {
		t.Fatalf("relayed %+v, want %+v", relay.wants, want)
	}
	if len(repo.updates) != 1 {
		t.Fatalf("local writes = %d, want 1", len(repo.updates))
	}
	local := repo.updates[0]
	if local.WarmupBase != nil || local.WarmupDays != nil || local.WarmupStartTime != nil || local.Name == nil || local.Timezone == nil {
		t.Fatalf("local write %+v, want only the fields this instance owns", local)
	}
}

func TestCloudWarmupEditIsNotSavedWhenTheCloudRefuses(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	relay.err = errx.NewWithIdentifier(errx.ServiceUnavailable, "cloud_link_unreachable", "Warmbly Cloud could not be reached.")
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{Name: ptr("Sales"), WarmupBase: ptr(15)}); xerr == nil {
		t.Fatal("a refused relay was reported as saved")
	}
	if len(repo.updates) != 0 {
		t.Fatal("wrote locally although the cloud took nothing")
	}
}

func TestCloudWarmupEditIsValidatedBeforeTheCloudSeesIt(t *testing.T) {
	s, _, relay := cloudRelayFixture("active")
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{Name: ptr("<b>x</b>"), WarmupBase: ptr(15)}); xerr == nil {
		t.Fatal("an invalid name passed")
	}
	if len(relay.wants) != 0 {
		t.Fatal("relayed a patch that cannot be saved")
	}
}

func TestCloudOnlyPatchReturnsTheMailboxWithoutALocalWrite(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	got, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{WarmupMax: ptr(35)})
	if xerr != nil || got == nil {
		t.Fatalf("update = %v, %v", got, xerr)
	}
	if len(relay.wants) != 1 || relay.wants[0].Max != 35 || len(repo.updates) != 0 {
		t.Fatalf("relayed %+v, local writes %d", relay.wants, len(repo.updates))
	}
}

func TestCloudWarmupZeroValues(t *testing.T) {
	s, _, relay := cloudRelayFixture("active")
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{WarmupIncrease: ptr(0)}); xerr == nil || xerr.Identifier != "cloud_warmup_value_unsupported" {
		t.Fatalf("error = %v, want cloud_warmup_value_unsupported", xerr)
	}
	s.emailRepository.(*relayEmailRepo).account.WarmupIncrease = 0
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{WarmupIncrease: ptr(0)}); xerr != nil {
		t.Fatalf("a zero the cloud already holds was refused: %v", xerr)
	}
	if len(relay.wants) != 0 {
		t.Fatalf("relayed %+v for a value that did not change", relay.wants)
	}
}

func TestCloudWarmupParticipationIsChangedThroughItsOwnControls(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	for _, u := range []*models.UpdateEmail{
		{TestSendEnabled: ptr(false)}, {SharedDailyLimit: ptr(5)}, {TestMode: ptr(models.TestParticipationOff)},
	} {
		if _, xerr := s.Update(context.Background(), "org", "user", "id", u); xerr == nil || xerr.Identifier != "cloud_warmup_managed" {
			t.Fatalf("error = %v, want cloud_warmup_managed", xerr)
		}
	}
	if len(relay.wants) != 0 || len(repo.updates) != 0 {
		t.Fatal("participation was changed locally or relayed as settings")
	}
}

func TestWarmupToggleBecomesACloudPauseOrResume(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{Warmup: ptr(false)}); xerr != nil {
		t.Fatal(xerr)
	}
	if len(relay.lifecycles) != 1 || relay.lifecycles[0] != "pause" || len(repo.updates) != 0 {
		t.Fatalf("lifecycles %v, local writes %d", relay.lifecycles, len(repo.updates))
	}
}

func TestLocalSettingsStayLocalUntilTheCloudOwnsThem(t *testing.T) {
	for _, state := range []string{"pending_enroll", "pending_remove"} {
		s, repo, relay := cloudRelayFixture(state)
		if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{WarmupBase: ptr(15)}); xerr != nil {
			t.Fatal(xerr)
		}
		if len(relay.wants) != 0 || len(repo.updates) != 1 {
			t.Fatalf("%s: relayed %d, local writes %d, want a local write", state, len(relay.wants), len(repo.updates))
		}
	}
}

func TestCloudWarmupLifecycleRoutes(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	for _, action := range []string{"start", "pause", "resume"} {
		if _, xerr := s.SetWarmupLifecycle(context.Background(), "org", "id", action); xerr != nil {
			t.Fatalf("%s: %v", action, xerr)
		}
	}
	if want := []string{"resume", "pause", "resume"}; len(relay.actions) != 3 || relay.actions[0] != want[0] || relay.actions[1] != want[1] || relay.actions[2] != want[2] {
		t.Fatalf("relayed %v, want %v", relay.actions, want)
	}
	if _, xerr := s.SetWarmupLifecycle(context.Background(), "org", "id", "stop"); xerr == nil || xerr.Identifier != "cloud_warmup_managed" {
		t.Fatalf("stop = %v, want cloud_warmup_managed", xerr)
	}
	if len(repo.cycles) != 0 {
		t.Fatalf("changed local warmup state %v for a Cloud mailbox", repo.cycles)
	}
}

func TestCloudWarmupWindowIsCheckedAgainstTheStoredHalf(t *testing.T) {
	s, repo, relay := cloudRelayFixture("active")
	repo.account.WarmupStartTime, repo.account.WarmupEndTime = "08:00:00", "20:00:00"
	if _, xerr := s.Update(context.Background(), "org", "user", "id", &models.UpdateEmail{WarmupEndTime: ptr("07:00")}); xerr == nil {
		t.Fatal("a window ending before it starts was sent")
	}
	if len(relay.wants) != 0 {
		t.Fatal("relayed an invalid window")
	}
}
