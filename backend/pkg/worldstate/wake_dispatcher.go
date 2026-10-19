package worldstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"pentagi/pkg/database"

	"github.com/sirupsen/logrus"
)

const (
	wakePollInterval  = 500 * time.Millisecond
	wakeLeaseDuration = 10 * time.Second
	wakeBatchSize     = 32
)

type PrimaryWaitResume func(context.Context, database.AgentChainWait) error

type PrimaryWaitDispatcher struct {
	db     TransactionDB
	resume PrimaryWaitResume
	owner  string
	hints  chan struct{}
}

func NewPrimaryWaitDispatcher(db TransactionDB, resume PrimaryWaitResume) *PrimaryWaitDispatcher {
	return &PrimaryWaitDispatcher{
		db: db, resume: resume,
		owner: fmt.Sprintf("world-state-wake-%d", time.Now().UnixNano()),
		hints: make(chan struct{}, 1),
	}
}

func (d *PrimaryWaitDispatcher) Hint(_ context.Context, _ int64) error {
	select {
	case d.hints <- struct{}{}:
	default:
	}
	return nil
}

func (d *PrimaryWaitDispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(wakePollInterval)
	defer ticker.Stop()
	d.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.hints:
		}
		d.scan(ctx)
	}
}

func (d *PrimaryWaitDispatcher) scan(ctx context.Context) {
	waits, err := d.db.LeasePrimaryWorldStateWaits(ctx, database.LeasePrimaryWorldStateWaitsParams{
		LeaseOwner:     sql.NullString{String: d.owner, Valid: true},
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(wakeLeaseDuration), Valid: true},
		LimitRows:      wakeBatchSize,
	})
	if err != nil {
		if ctx.Err() == nil {
			logrus.WithError(err).Warn("failed to lease primary World State waits")
		}
		return
	}
	for _, wait := range waits {
		head, err := d.db.GetWorldStateEventHead(ctx, wait.FlowID)
		if err == nil {
			_, err = ResolveLeasedPrimaryWorldStateWait(ctx, d.db, wait, d.owner, head)
		}
		if err != nil {
			d.release(ctx, wait)
		}
	}
	d.resumePending(ctx)
}

func (d *PrimaryWaitDispatcher) resumePending(ctx context.Context) {
	waits, err := d.db.LeasePrimaryWorldStateResumeWaits(ctx, database.LeasePrimaryWorldStateResumeWaitsParams{
		LeaseOwner:     sql.NullString{String: d.owner, Valid: true},
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(wakeLeaseDuration), Valid: true},
		LimitRows:      wakeBatchSize,
	})
	if err != nil {
		if ctx.Err() == nil {
			logrus.WithError(err).Warn("failed to reconcile primary World State resumes")
		}
		return
	}
	for _, wait := range waits {
		if err := d.resume(ctx, wait); err != nil && ctx.Err() == nil {
			d.releaseResume(ctx, wait)
			logrus.WithError(err).WithFields(logrus.Fields{
				"flow_id": wait.FlowID, "msgchain_id": wait.MsgchainID,
			}).Warn("primary World State resume was not accepted")
		}
	}
}

func (d *PrimaryWaitDispatcher) releaseResume(ctx context.Context, wait database.AgentChainWait) {
	delay := time.Second << min(wait.RetryCount, 5)
	_, err := d.db.ReleasePrimaryWorldStateResumeLease(ctx, database.ReleasePrimaryWorldStateResumeLeaseParams{
		NextAttemptAt: time.Now().Add(delay), MsgchainID: wait.MsgchainID,
		LeaseOwner: sql.NullString{String: d.owner, Valid: true},
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
		logrus.WithError(err).Warn("failed to release primary World State resume lease")
	}
}

func (d *PrimaryWaitDispatcher) release(ctx context.Context, wait database.AgentChainWait) {
	delay := time.Second << min(wait.RetryCount, 5)
	_, err := d.db.ReleaseAgentChainWaitLease(ctx, database.ReleaseAgentChainWaitLeaseParams{
		NextAttemptAt: time.Now().Add(delay), MsgchainID: wait.MsgchainID,
		LeaseOwner: sql.NullString{String: d.owner, Valid: true},
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
		logrus.WithError(err).Warn("failed to release primary World State wait lease")
	}
}
