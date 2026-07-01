package service

import (
	"strings"
	"testing"
)

func TestTeamSlug(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"Platform Team", "platformteam"},
		{"platform-team", "platform-team"},
		{"SRE / On-Call", "sreon-call"},
		{"---", ""},
		{"日本語", ""},
		{"-edge-trimmed-", "edge-trimmed"},
		{strings.Repeat("a", 100), strings.Repeat("a", 64)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := teamSlug(tt.name)
			if got != tt.want {
				t.Errorf("teamSlug(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
