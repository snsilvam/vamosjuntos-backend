package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestToken(t *testing.T) {
	key := bytes.Repeat([]byte{7}, sessionKeyBytes)
	otherKey := bytes.Repeat([]byte{8}, sessionKeyBytes)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, colombia)
	exp := now.Add(time.Hour)
	token := signToken(key, "carmencita", exp)

	t.Run("round trip", func(t *testing.T) {
		got, err := verifyToken(key, token, now)
		if err != nil || got != "carmencita" {
			t.Fatalf("verifyToken = %q, %v", got, err)
		}
	})

	t.Run("tampered payload", func(t *testing.T) {
		forged := signToken(key, "liam", exp)
		payload, _, _ := strings.Cut(forged, ".")
		_, sig, _ := strings.Cut(token, ".")
		if _, err := verifyToken(key, payload+"."+sig, now); !errors.Is(err, errInvalidToken) {
			t.Fatalf("err = %v, want errInvalidToken", err)
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		if _, err := verifyToken(otherKey, token, now); !errors.Is(err, errInvalidToken) {
			t.Fatalf("err = %v, want errInvalidToken", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		if _, err := verifyToken(key, token, exp); !errors.Is(err, errExpiredToken) {
			t.Fatalf("err = %v, want errExpiredToken", err)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		for _, bad := range []string{"", "sinpunto", "a.b", ".", "%%%.%%%"} {
			if _, err := verifyToken(key, bad, now); err == nil {
				t.Errorf("verifyToken(%q) accepted a malformed token", bad)
			}
		}
	})
}
