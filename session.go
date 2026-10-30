package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// sessionCookieName is the only cookie that Firebase Hosting forwards to Cloud Run.
const sessionCookieName = "__session"

var (
	errInvalidToken = errors.New("invalid session token")
	errExpiredToken = errors.New("expired session token")
)

type tokenPayload struct {
	GuestID string `json:"g"`
	Exp     int64  `json:"exp"`
}

// signToken returns base64url(payload) + "." + base64url(HMAC-SHA256(payload)).
func signToken(key []byte, guestID string, exp time.Time) string {
	// Marshalling a struct of a string and an int64 cannot fail.
	payload, _ := json.Marshal(tokenPayload{GuestID: guestID, Exp: exp.Unix()})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(tokenMAC(key, encoded))
}

// verifyToken checks the signature and expiry of token and returns its guest id.
func verifyToken(key []byte, token string, now time.Time) (string, error) {
	encoded, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", errInvalidToken
	}
	gotMAC, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(gotMAC, tokenMAC(key, encoded)) {
		return "", errInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", errInvalidToken
	}
	var p tokenPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.GuestID == "" {
		return "", errInvalidToken
	}
	if now.Unix() >= p.Exp {
		return "", errExpiredToken
	}
	return p.GuestID, nil
}

func tokenMAC(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// requireSession lets a request through only with the session of a guest who is still in the
// catalog and confirmed (FR-008).
func (s *server) requireSession(next func(http.ResponseWriter, *http.Request, Guest)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated)
			return
		}
		guestID, err := verifyToken(s.sessionKey, c.Value, s.now())
		if err != nil {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated)
			return
		}
		guest, ok := s.catalog.ByID(guestID)
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
		next(w, r, guest)
	}
}
