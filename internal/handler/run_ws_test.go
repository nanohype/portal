package handler

import (
	"reflect"
	"testing"
)

func TestWsOriginPatterns(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		want    []string
	}{
		{
			name:    "production URL",
			origins: []string{"https://portal.example.com"},
			want:    []string{"portal.example.com"},
		},
		{
			name:    "dev URLs with port",
			origins: []string{"http://localhost:5173", "http://localhost:8080"},
			want:    []string{"localhost:5173", "localhost:8080"},
		},
		{
			name:    "empty list falls back to localhost wildcard",
			origins: []string{},
			want:    []string{"localhost:*"},
		},
		{
			name:    "invalid URL skipped",
			origins: []string{"not-a-url", "https://valid.com"},
			want:    []string{"valid.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wsOriginPatterns(tt.origins)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wsOriginPatterns(%v) = %v, want %v", tt.origins, got, tt.want)
			}
		})
	}
}
