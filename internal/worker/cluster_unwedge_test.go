package worker

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// workspace builds an unstructured Workspace with the given deletion + annotation
// state, the two things verifyWedged gates on.
func workspace(deleting bool, annotations map[string]string) *unstructured.Unstructured {
	ws := &unstructured.Unstructured{}
	ws.SetName("prod-eks-stack")
	if deleting {
		now := metav1.Now()
		ws.SetDeletionTimestamp(&now)
	}
	if annotations != nil {
		ws.SetAnnotations(annotations)
	}
	return ws
}

func TestVerifyWedged(t *testing.T) {
	pending := map[string]string{unwedgePendingAnnotation: "20260627120000"}

	tests := []struct {
		name     string
		ws       *unstructured.Unstructured
		wantErr  bool
		errMatch string
	}{
		{
			name: "deleting and pending → wedged",
			ws:   workspace(true, pending),
		},
		{
			name:     "not being deleted → refuse (would churn a live workspace)",
			ws:       workspace(false, pending),
			wantErr:  true,
			errMatch: "not being deleted",
		},
		{
			name:     "deleting but no pending annotation → refuse (not actually stuck)",
			ws:       workspace(true, map[string]string{"other": "x"}),
			wantErr:  true,
			errMatch: "not wedged",
		},
		{
			name:     "deleting, no annotations at all → refuse",
			ws:       workspace(true, nil),
			wantErr:  true,
			errMatch: "not wedged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyWedged(tt.ws)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if !strings.Contains(err.Error(), tt.errMatch) {
					t.Errorf("error = %q, want it to contain %q", err, tt.errMatch)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
