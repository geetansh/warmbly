package cloudlink

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/repository"
)

var (
	errSettingsUnreported = errx.NewWithIdentifier(errx.ServiceUnavailable, "cloud_link_bad_response", "Warmbly Cloud did not report this mailbox's warmup settings. Try again in a moment.")
	errLinkDisconnecting  = errx.NewWithIdentifier(errx.Conflict, "cloud_link_disconnecting", "This workspace is disconnecting from Warmbly Cloud. Try again once it has finished.")
)

func errWarmupNotApplied(fields []string) *errx.Error {
	return errx.NewWithIdentifier(errx.Conflict, "cloud_warmup_not_applied",
		"Warmbly Cloud did not apply "+strings.Join(fields, ", ")+". The mailbox now shows the values Warmbly Cloud reported.")
}

func (s *service) OnSettingsChange(fn func(context.Context, []uuid.UUID)) {
	s.settingsChanged = fn
}

// mirror writes the cloud's reported settings onto the local columns and announces the mailboxes that changed.
func (s *service) mirror(ctx context.Context, observedAt time.Time, settings map[uuid.UUID]models.PoolLinkWarmupSettings) ([]uuid.UUID, error) {
	r, ok := s.repo.(repository.CloudWarmupMirrorRepository)
	if !ok || len(settings) == 0 {
		return nil, nil
	}
	changed, err := r.MirrorCloudWarmupSettings(ctx, observedAt, settings)
	if err != nil {
		return nil, err
	}
	if len(changed) > 0 && s.settingsChanged != nil {
		s.settingsChanged(ctx, changed)
	}
	return changed, nil
}

// mirrorReported mirrors one state the cloud just returned; a failure leaves the next sync to repair it.
func (s *service) mirrorReported(ctx context.Context, accountID uuid.UUID, state *models.PoolLinkMailboxState) {
	if state == nil || !state.Settings.Reported() {
		return
	}
	if _, err := s.mirror(ctx, time.Now(), map[uuid.UUID]models.PoolLinkWarmupSettings{accountID: state.Settings}); err != nil {
		log.Warn().Err(err).Str("account_id", accountID.String()).Msg("cloud link: warmup settings could not be mirrored")
	}
}

// sameDays treats 0 and the all-days mask as the same "every day".
func sameDays(a, b int) bool {
	if a == 0 {
		a = models.WarmupEveryDay
	}
	if b == 0 {
		b = models.WarmupEveryDay
	}
	return a == b
}

// notApplied names each requested setting the cloud's answer does not carry; zero means not requested.
func notApplied(want models.PoolLinkWarmupSettings, lifecycle string, state *models.PoolLinkMailboxState) []string {
	got := state.Settings
	var out []string
	for _, f := range []struct {
		name      string
		want, got int
	}{
		{"starting volume", want.Base, got.Base}, {"maximum volume", want.Max, got.Max},
		{"daily increase", want.Increase, got.Increase}, {"reply rate", want.ReplyRate, got.ReplyRate},
	} {
		if f.want > 0 && f.want != f.got {
			out = append(out, f.name)
		}
	}
	if want.StartTime != "" && models.ClockHHMM(want.StartTime) != models.ClockHHMM(got.StartTime) {
		out = append(out, "start time")
	}
	if want.EndTime != "" && models.ClockHHMM(want.EndTime) != models.ClockHHMM(got.EndTime) {
		out = append(out, "end time")
	}
	if want.Days != 0 && !sameDays(want.Days, got.Days) {
		out = append(out, "sending days")
	}
	if want.Timezone != "" && want.Timezone != got.Timezone {
		out = append(out, "timezone")
	}
	if state.Warmup != nil && (lifecycle == "pause" && !state.Warmup.Paused || lifecycle == "resume" && state.Warmup.Paused) {
		out = append(out, "pause state")
	}
	return out
}

// RelayWarmupSettings sends a change to the cloud, which owns these settings, and mirrors what it answers.
func (s *service) RelayWarmupSettings(ctx context.Context, orgID, accountID uuid.UUID, want models.PoolLinkWarmupSettings, lifecycle string) (*models.PoolLinkWarmupSettings, *errx.Error) {
	if lifecycle != "" && lifecycle != "pause" && lifecycle != "resume" {
		return nil, errx.NewWithIdentifier(errx.BadRequest, "cloud_link_lifecycle", "Action must be pause or resume.")
	}
	if _, xerr := s.ownedAccount(ctx, orgID, accountID); xerr != nil {
		return nil, xerr
	}
	m, err := s.repo.GetByAccount(ctx, accountID)
	if err != nil {
		return nil, errx.InternalError()
	}
	if m == nil || m.EnrollmentState == "pending_remove" {
		return nil, errx.ErrNotFound
	}
	l, xerr := s.mailboxLink(ctx, m)
	if xerr == ErrAlreadyLinked {
		return nil, errLinkDisconnecting
	}
	if xerr != nil {
		return nil, xerr
	}
	var state models.PoolLinkMailboxState
	patch := models.PoolLinkMailboxPatch{Warmup: &want, Lifecycle: lifecycle}
	if xerr := s.clientFor(l).do(ctx, http.MethodPatch, "/instance/mailboxes/"+m.RemoteID.String(), patch, &state); xerr != nil {
		return nil, xerr
	}
	observedAt := time.Now()
	s.recordStanding(ctx, accountID, state.Health, true)
	if !state.Settings.Reported() {
		return nil, errSettingsUnreported
	}
	if _, err := s.mirror(ctx, observedAt, map[uuid.UUID]models.PoolLinkWarmupSettings{accountID: state.Settings}); err != nil {
		log.Error().Err(err).Str("account_id", accountID.String()).Msg("cloud link: warmup settings applied on the cloud but not mirrored")
		return nil, errx.InternalError()
	}
	if missing := notApplied(want, lifecycle, &state); len(missing) > 0 {
		return nil, errWarmupNotApplied(missing)
	}
	got := state.Settings.Mirrored()
	return &got, nil
}

// SyncWarmupSettings refreshes every enrolled mailbox's local copy from the cloud and pushes this instance's timezone.
func (s *service) SyncWarmupSettings(ctx context.Context) ([]uuid.UUID, *errx.Error) {
	links, err := s.repo.ListLinks(ctx)
	if err != nil {
		return nil, errx.InternalError()
	}
	enrolled, err := s.repo.List(ctx)
	if err != nil {
		return nil, errx.InternalError()
	}
	groups := mailboxGroups(enrolled)
	var changed []uuid.UUID
	var lastErr *errx.Error
	for i := range links {
		l := &links[i]
		group := groups[l.InstanceID]
		if len(group) == 0 || l.DisconnectPending {
			continue
		}
		observedAt := time.Now()
		var states []models.PoolLinkMailboxState
		if xerr := s.clientFor(l).do(ctx, http.MethodGet, "/instance/mailboxes", nil, &states); xerr != nil {
			log.Warn().Str("instance_id", l.InstanceID.String()).Str("code", xerr.Identifier).Msg("cloud link: warmup settings could not be refreshed")
			lastErr = xerr
			continue
		}
		byRemote := make(map[uuid.UUID]*models.PoolLinkMailboxState, len(states))
		for j := range states {
			byRemote[states[j].RemoteID] = &states[j]
		}
		reported := map[uuid.UUID]models.PoolLinkWarmupSettings{}
		for _, m := range group {
			state := byRemote[m.RemoteID]
			if m.EnrollmentState == "pending_remove" || state == nil || !state.Settings.Reported() {
				continue
			}
			reported[m.EmailAccountID] = state.Settings
			s.pushTimezone(ctx, l, m, state.Settings.Timezone)
		}
		ids, err := s.mirror(ctx, observedAt, reported)
		if err != nil {
			log.Warn().Err(err).Str("instance_id", l.InstanceID.String()).Msg("cloud link: warmup settings could not be mirrored")
			lastErr = errx.InternalError()
			continue
		}
		changed = append(changed, ids...)
	}
	return changed, lastErr
}

// pushTimezone keeps the cloud reading the warmup window in the zone this instance owns.
func (s *service) pushTimezone(ctx context.Context, l *models.CloudLink, m models.CloudLinkMailbox, cloudTZ string) {
	acc, xerr := s.emails.GetByID(ctx, m.EmailAccountID)
	if xerr != nil || acc == nil {
		return
	}
	tz := acc.ClockTimezone()
	if tz == "" || tz == cloudTZ {
		return
	}
	patch := models.PoolLinkMailboxPatch{Warmup: &models.PoolLinkWarmupSettings{Timezone: tz}}
	if xerr := s.clientFor(l).do(ctx, http.MethodPatch, "/instance/mailboxes/"+m.RemoteID.String(), patch, nil); xerr != nil {
		log.Warn().Str("account_id", m.EmailAccountID.String()).Str("code", xerr.Identifier).Msg("cloud link: warmup timezone could not be pushed")
	}
}
