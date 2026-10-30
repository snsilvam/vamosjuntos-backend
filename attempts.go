package main

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxFailedAttempts = 5
	blockDuration     = 15 * time.Minute
)

// attemptState counts consecutive failed code attempts from one account (FR-014 of spec 008).
type attemptState struct {
	Failures     int       `firestore:"failures"`
	BlockedUntil time.Time `firestore:"blockedUntil"`
}

// blockedFor returns how long the account stays blocked, or 0 if it is not blocked.
func blockedFor(s attemptState, now time.Time) time.Duration {
	if now.Before(s.BlockedUntil) {
		return s.BlockedUntil.Sub(now)
	}
	return 0
}

// registerAttempt returns the state after an attempt. A success resets the counter; the fifth
// consecutive failure blocks the account for 15 minutes and starts a new count.
func registerAttempt(s attemptState, ok bool, now time.Time) attemptState {
	if ok {
		return attemptState{}
	}
	s.Failures++
	if s.Failures >= maxFailedAttempts {
		return attemptState{BlockedUntil: now.Add(blockDuration)}
	}
	return s
}

// AttemptStore keeps attempt counters in loginAttempts/{uid}.
type AttemptStore struct {
	client *firestore.Client
	now    func() time.Time
}

// NewAttemptStore returns an AttemptStore that uses the wall clock.
func NewAttemptStore(client *firestore.Client) *AttemptStore {
	return &AttemptStore{client: client, now: time.Now}
}

func (s *AttemptStore) doc(uid string) *firestore.DocumentRef {
	return s.client.Collection("loginAttempts").Doc(uid)
}

// Blocked returns how long the account uid stays blocked, or 0.
func (s *AttemptStore) Blocked(ctx context.Context, uid string) (time.Duration, error) {
	snap, err := s.doc(uid).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get attempts: %w", err)
	}
	var st attemptState
	if err := snap.DataTo(&st); err != nil {
		return 0, fmt.Errorf("decode attempts: %w", err)
	}
	return blockedFor(st, s.now()), nil
}

// Record stores the result of one attempt from the account uid.
func (s *AttemptStore) Record(ctx context.Context, uid string, ok bool) error {
	ref := s.doc(uid)
	if ok {
		if _, err := ref.Delete(ctx); err != nil {
			return fmt.Errorf("reset attempts: %w", err)
		}
		return nil
	}
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var st attemptState
		snap, err := tx.Get(ref)
		switch {
		case status.Code(err) == codes.NotFound:
		case err != nil:
			return fmt.Errorf("get attempts: %w", err)
		default:
			if err := snap.DataTo(&st); err != nil {
				return fmt.Errorf("decode attempts: %w", err)
			}
		}
		return tx.Set(ref, registerAttempt(st, false, s.now()))
	})
	if err != nil {
		return fmt.Errorf("record failed attempt: %w", err)
	}
	return nil
}
