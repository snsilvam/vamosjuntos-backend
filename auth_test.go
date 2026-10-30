package main

import (
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer abc.def", "abc.def", true},
		{"Bearer   abc  ", "abc", true},
		{"Bearer ", "", false},
		{"Basic abc", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/api/me", nil)
		if tt.header != "" {
			r.Header.Set("Authorization", tt.header)
		}
		got, ok := bearerToken(r)
		if got != tt.want || ok != tt.ok {
			t.Errorf("bearerToken(%q) = %q, %v; want %q, %v", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}
