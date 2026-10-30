package main

import (
	"context"
	"errors"
	"fmt"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	errAccountLinked = errors.New("account already linked to another guest")
	errGuestLinked   = errors.New("guest already linked to another account")
)

// LinkStore keeps the account ↔ guest links in events/{EVENT_ID}/links/{guestId}, with the
// account's uid and email. The organizer undoes a link by deleting its document (FR-006).
type LinkStore struct {
	client  *firestore.Client
	eventID string
}

// NewLinkStore returns a LinkStore for the given event.
func NewLinkStore(client *firestore.Client, eventID string) *LinkStore {
	return &LinkStore{client: client, eventID: eventID}
}

func (s *LinkStore) collection() *firestore.CollectionRef {
	return s.client.Collection("events").Doc(s.eventID).Collection("links")
}

func (s *LinkStore) byAccount(uid string) firestore.Query {
	return s.collection().Where("uid", "==", uid).Limit(1)
}

// GuestOf returns the id of the guest linked to the account uid.
func (s *LinkStore) GuestOf(ctx context.Context, uid string) (string, bool, error) {
	docs, err := s.byAccount(uid).Documents(ctx).GetAll()
	if err != nil {
		return "", false, fmt.Errorf("find link of account: %w", err)
	}
	if len(docs) == 0 {
		return "", false, nil
	}
	return docs[0].Ref.ID, true, nil
}

// Link links the account uid to guestID (FR-004, FR-005). Linking the same pair again does
// nothing. The guest document is read inside the transaction, so of two accounts racing for the
// same guest only one commits; the other retries, finds the link and gets errGuestLinked.
func (s *LinkStore) Link(ctx context.Context, uid, email, guestID string) error {
	ref := s.collection().Doc(guestID)
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		docs, err := tx.Documents(s.byAccount(uid)).GetAll()
		if err != nil {
			return fmt.Errorf("find link of account: %w", err)
		}
		if len(docs) > 0 {
			if docs[0].Ref.ID == guestID {
				return nil
			}
			return errAccountLinked
		}
		_, err = tx.Get(ref)
		switch {
		case err == nil:
			return errGuestLinked
		case status.Code(err) != codes.NotFound:
			return fmt.Errorf("get link of guest: %w", err)
		}
		return tx.Create(ref, map[string]any{
			"uid":      uid,
			"email":    email,
			"linkedAt": firestore.ServerTimestamp,
		})
	})
	switch {
	case errors.Is(err, errAccountLinked), errors.Is(err, errGuestLinked):
		return err
	case status.Code(err) == codes.AlreadyExists:
		return errGuestLinked
	case err != nil:
		return fmt.Errorf("link %s: %w", guestID, err)
	}
	return nil
}
