package service

import "testing"

func TestAssignRole(t *testing.T) {
	tests := []struct {
		name      string
		userCount int64
		want      string
	}{
		{"first user gets owner", 0, "owner"},
		{"second user gets viewer", 1, "viewer"},
		{"many users get viewer", 100, "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assignRole(tt.userCount)
			if got != tt.want {
				t.Errorf("assignRole(%d) = %q, want %q", tt.userCount, got, tt.want)
			}
		})
	}
}
