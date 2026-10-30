package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/auth"
)

// Account is the Firebase Authentication account behind a request.
type Account struct {
	UID   string
	Email string
}

var errInvalidToken = errors.New("invalid ID token")

// FirebaseVerifier checks Firebase ID tokens with the Admin SDK. The SDK validates the signature,
// the "aud", "iss" and "exp" claims, and caches Google's public keys.
type FirebaseVerifier struct {
	client *auth.Client
}

// NewFirebaseVerifier returns a verifier for projectID. Credentials come from Application
// Default Credentials; verifying tokens needs no extra IAM role.
func NewFirebaseVerifier(ctx context.Context, projectID string) (*FirebaseVerifier, error) {
	// The project id is explicit because the token's "aud" claim is compared with it.
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("firebase app: %w", err)
	}
	client, err := app.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase auth client: %w", err)
	}
	return &FirebaseVerifier{client: client}, nil
}

// Verify returns the account of a valid, unexpired ID token issued for this project.
func (v *FirebaseVerifier) Verify(ctx context.Context, raw string) (Account, error) {
	token, err := v.client.VerifyIDToken(ctx, raw)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %v", errInvalidToken, err)
	}
	email, _ := token.Claims["email"].(string)
	return Account{UID: token.UID, Email: email}, nil
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", false
	}
	raw = strings.TrimSpace(raw)
	return raw, raw != ""
}

// requireAccount lets a request through only with a valid Firebase ID token (FR-002). The token
// is never logged.
func (s *server) requireAccount(next func(http.ResponseWriter, *http.Request, Account)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated)
			return
		}
		account, err := s.verifier.Verify(r.Context(), raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated)
			return
		}
		next(w, r, account)
	}
}

// requireGuest lets a request through only for an account linked to a guest who is still in the
// catalog and confirmed (FR-010).
func (s *server) requireGuest(next func(http.ResponseWriter, *http.Request, Account, Guest)) http.HandlerFunc {
	return s.requireAccount(func(w http.ResponseWriter, r *http.Request, account Account) {
		guestID, linked, err := s.links.GuestOf(r.Context(), account.UID)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if !linked {
			writeError(w, http.StatusForbidden, codeNotLinked)
			return
		}
		catalog, ok := s.loadCatalog(w, r)
		if !ok {
			return
		}
		guest, ok := catalog.ByID(guestID)
		if !ok {
			writeError(w, http.StatusForbidden, codeNotInvited)
			return
		}
		statuses, err := s.attendance.Statuses(r.Context())
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if statuses[guest.ID] != statusConfirmed {
			writeError(w, http.StatusForbidden, codeNotInvited)
			return
		}
		next(w, r, account, guest)
	})
}
