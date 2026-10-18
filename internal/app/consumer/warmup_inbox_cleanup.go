package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/warmbly/warmbly/internal/errx"
	"github.com/warmbly/warmbly/internal/jobrun"
	"github.com/warmbly/warmbly/internal/models"
	"github.com/warmbly/warmbly/internal/observability/arrivaldiag"
)

// StartWarmupInboxCleanup repairs old leaks in bounded passes, retrying verification failures per mailbox.
func (s *JobsService) StartWarmupInboxCleanup(ctx context.Context) {
	if s.UniboxRepository == nil {
		return
	}
	sweep := &warmupInboxSweep{}
	var nextPass time.Time
	jobrun.Loop(ctx, "warmup_inbox_cleanup", time.Minute, true, func(ctx context.Context) error {
		if time.Now().Before(nextPass) {
			return nil
		}
		passCtx, cancel := context.WithTimeout(ctx, warmupCleanupPassBudget)
		defer cancel()
		done, err := s.cleanWarmupInboxBatch(passCtx, sweep)
		if done {
			nextPass = sweep.finish(time.Now())
		}
		return err
	})
}

const (
	warmupCleanupInterval   = 24 * time.Hour
	warmupCleanupRetryDelay = 15 * time.Minute
	warmupCleanupPassBudget = 45 * time.Second
	// A candidate can run both Cloud checks to their timeout, so none starts this close to the pass deadline.
	warmupCleanupCandidateReserve = 2*cloudWarmupCheckTimeout + 5*time.Second
	warmupCleanupPageSize         = 100
	warmupCleanupPassChecks       = 100
	warmupCleanupPassPages        = 10
)

// warmupInboxSweep is the cleanup's position, carried across one-minute passes.
type warmupInboxSweep struct {
	afterID uuid.UUID
	// held maps a mailbox whose check failed to the cursor before that row; the rest of it waits for a retry sweep.
	held map[uuid.UUID]uuid.UUID
	// only limits a retry sweep to the mailboxes the previous sweep held, each past its own resume point.
	only    map[uuid.UUID]uuid.UUID
	retries int
	fullAt  time.Time
}

func (w *warmupInboxSweep) wants(mailbox, id uuid.UUID) bool {
	if _, held := w.held[mailbox]; held {
		return false
	}
	if w.only == nil {
		return true
	}
	resume, ok := w.only[mailbox]
	return ok && bytes.Compare(id[:], resume[:]) > 0
}

func (w *warmupInboxSweep) hold(mailbox uuid.UUID) {
	if w.held == nil {
		w.held = map[uuid.UUID]uuid.UUID{}
	}
	w.held[mailbox] = w.afterID
}

// finish closes a sweep and returns when the next starts: a backed-off retry of the held mailboxes, else the next full sweep.
func (w *warmupInboxSweep) finish(now time.Time) time.Time {
	if w.only == nil {
		w.fullAt, w.retries = now.Add(warmupCleanupInterval), 0
	}
	held := w.held
	w.afterID, w.held, w.only = uuid.Nil, nil, nil
	retry := now.Add(warmupCleanupRetryDelay << w.retries)
	if len(held) == 0 || !retry.Before(w.fullAt) {
		return w.fullAt
	}
	w.retries++
	w.only = held
	first := true
	for _, resume := range held {
		if first || bytes.Compare(resume[:], w.afterID[:]) < 0 {
			w.afterID, first = resume, false
		}
	}
	return retry
}

func warmupCleanupHasTime(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= warmupCleanupCandidateReserve
}

// fileWarmupOutOfMailbox persists filing separately from best-effort engagement.
func (s *JobsService) fileWarmupOutOfMailbox(ctx context.Context, e *models.JobEventNewEmail) error {
	defer arrivaldiag.Begin(ctx, arrivaldiag.WarmupFile)()
	if s.Publisher == nil || s.EmailRepository == nil || e.Message == nil {
		return nil
	}
	if models.NormalizeFolder(e.Message.Folder, e.Message.Flags) == models.FolderTrash || e.Message.ProviderFolder == models.FolderTrash {
		return nil
	}
	account, xerr := s.EmailRepository.GetByID(ctx, e.Message.EmailID)
	if xerr != nil {
		if xerr.Code == errx.NotFound {
			return nil
		}
		return fmt.Errorf("%w: warmup mailbox lookup: %w", errWarmupVerification, xerr)
	}
	if account == nil {
		return nil
	}
	if s.WarmupRecoveryRepo != nil {
		if err := s.WarmupRecoveryRepo.RememberMessage(ctx, e.Message.EmailID, e.Message.MessageID, e.Message.InternalDate); err != nil {
			return fmt.Errorf("%w: remember warmup message: %w", errWarmupVerification, err)
		}
	}
	if s.CloudLink != nil {
		enrolled, err := s.CloudLink.CheckEnrollment(ctx, account.ID)
		if err != nil {
			return fmt.Errorf("%w: cloud enrollment lookup: %w", errWarmupVerification, err)
		}
		if enrolled {
			return nil // Warmbly Cloud files the mail it warms; workers admit no local filing for it.
		}
	}
	placement, folder := account.WarmupFiling()
	if placement == models.WarmupPlacementInbox {
		return nil
	}
	action := models.WarmupEmailAction{
		UserID:             e.UserID,
		EmailID:            e.Message.EmailID,
		GmailID:            e.Message.GmailID,
		UID:                e.Message.UID,
		MailboxUIDValidity: e.Message.Mailbox,
		MailboxFolder:      e.Message.FolderPath,
		RFCMessageID:       e.Message.MessageID,
		InternalID:         e.Message.ID.String(),
		Actions:            []string{models.WarmupActionFile},
		Placement:          placement,
		TargetFolder:       folder,
	}
	if s.WarmupRecoveryRepo != nil {
		id, err := s.WarmupRecoveryRepo.EnqueueFiling(ctx, action)
		if err != nil {
			return fmt.Errorf("%w: persist warmup filing: %w", errWarmupVerification, err)
		}
		action.FilingID = id.String()
	}
	if account.WorkerID == nil {
		if s.WarmupRecoveryRepo != nil {
			return nil
		}
		return fmt.Errorf("warmup filing: mailbox has no assigned worker")
	}
	if s.WarmupRecoveryRepo == nil {
		s.markSelfMove(ctx, e.Message.EmailID, e.Message.MessageID)
	}
	// Durable filings use presence verification instead of assuming every attempt moves mail.
	err := s.Publisher.PublishWarmupAction(ctx, *account.WorkerID, &action)
	if s.WarmupRecoveryRepo != nil {
		return nil // The persisted filing survives a failed publish.
	}
	if err != nil {
		return fmt.Errorf("%w: warmup file: %w", errWarmupVerification, err)
	}
	return nil
}

// StartPendingWarmupVerification drains arrivals held during verification outages.
func (s *JobsService) StartPendingWarmupVerification(ctx context.Context) {
	jobrun.Loop(ctx, "pending_warmup_verification", time.Minute, true, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		return s.retryPendingWarmupVerification(ctx)
	})
}

func (s *JobsService) retryPendingWarmupVerification(ctx context.Context) error {
	events, err := s.UniboxRepository.ClaimPendingWarmupVerification(ctx, 25)
	if err != nil {
		return err
	}
	var failures []error
	for _, e := range events {
		if err := s.UniboxRepository.ProcessPendingWarmupVerification(ctx, e.Message.ID, func(current *models.JobEventNewEmail) error {
			return s.ingestNewEmail(ctx, current)
		}); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// cleanWarmupInboxBatch runs one pass and reports whether the sweep reached the end.
func (s *JobsService) cleanWarmupInboxBatch(ctx context.Context, sweep *warmupInboxSweep) (bool, error) {
	var failures []error
	checked := 0
	for range warmupCleanupPassPages {
		if !warmupCleanupHasTime(ctx) {
			return false, warmupCleanupFailures(failures)
		}
		events, err := s.UniboxRepository.ListWarmupReviewCandidates(ctx, sweep.afterID, warmupCleanupPageSize)
		if err != nil {
			return false, errors.Join(warmupCleanupFailures(failures), err)
		}
		for _, e := range events {
			if !sweep.wants(e.Message.EmailID, e.Message.ID) {
				sweep.afterID = e.Message.ID
				continue
			}
			if checked == warmupCleanupPassChecks || !warmupCleanupHasTime(ctx) {
				return false, warmupCleanupFailures(failures)
			}
			checked++
			if err := s.cleanWarmupCandidate(ctx, e); err != nil {
				if ctx.Err() != nil {
					// The pass ran out, not the mailbox: this row is the first one retried.
					return false, errors.Join(warmupCleanupFailures(failures), err)
				}
				// One mailbox's failing checks must not stall every mailbox sorted after it.
				sweep.hold(e.Message.EmailID)
				log.Warn().Err(err).Str("email_account_id", e.Message.EmailID.String()).
					Msg("warmup inbox cleanup held a mailbox for retry")
				failures = append(failures, err)
			}
			sweep.afterID = e.Message.ID
		}
		if len(events) < warmupCleanupPageSize {
			return true, warmupCleanupFailures(failures)
		}
	}
	return false, warmupCleanupFailures(failures)
}

func warmupCleanupFailures(failures []error) error {
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("warmup inbox cleanup held %d mailbox(es) for retry: %w", len(failures), failures[0])
}

func (s *JobsService) cleanWarmupCandidate(ctx context.Context, e models.JobEventNewEmail) error {
	// Historical cleanup requires an exact identifier, never a reused subject.
	candidate, message := e, *e.Message
	message.Subject = ""
	candidate.Message = &message
	warmup, err := s.isKnownWarmupEmail(ctx, &candidate)
	if err != nil {
		return err
	}
	if !warmup {
		// A reply in a warmup thread that got in before ancestry was
		// checked, or while the turn it answers was still unknown.
		if warmup, err = s.isWarmupThreadReply(ctx, &candidate); err != nil {
			return err
		}
	}
	if !warmup {
		return nil
	}
	// Deleting the Unibox row only takes it out of OUR inbox. The copy
	// in the customer's own mailbox is what they are looking at, and
	// nothing else ever goes back for it, so file it here too (#583).
	// The Unibox row is the retry record: it stays until the filing
	// action is on the bus, so a publish or lookup failure is
	// re-offered when the mailbox is retried instead of hiding the leak.
	if err := s.fileWarmupOutOfMailbox(ctx, &candidate); err != nil {
		return err
	}
	if err := s.UniboxRepository.Delete(ctx, e.UserID, e.Message.ID); err != nil {
		return err
	}
	if s.StreamingPublisher != nil {
		s.StreamingPublisher.PublishEmailDeleted(ctx, s.emailInboxEvent(ctx, e.UserID, e.Message))
	}
	return nil
}
