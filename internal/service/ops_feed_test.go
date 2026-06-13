package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/nanohype/portal/internal/repository"
)

func TestActivityTime(t *testing.T) {
	created := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := activityTime(created, nil); !got.Equal(created) {
		t.Errorf("nil completed: got %v, want created %v", got, created)
	}
	if got := activityTime(created, &completed); !got.Equal(completed) {
		t.Errorf("completed set: got %v, want completed %v", got, completed)
	}
}

// mergeFeed must interleave both logs by activity time (completion if finished,
// else creation), newest first, and tag each item with the right kind+pointer.
func TestMergeFeed(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 6, 1, h, 0, 0, 0, time.UTC) }
	tp := func(h int) *time.Time { v := at(h); return &v }

	clusterOps := []repository.ClusterOperation{
		{ID: "c1", Name: "alpha", CreatedAt: at(9), CompletedAt: tp(15)}, // activity 15
		{ID: "c2", Name: "beta", CreatedAt: at(10)},                      // activity 10
	}
	tenantOps := []repository.TenantOperation{
		{ID: "t1", TenantName: "x", CreatedAt: at(11)},                     // activity 11
		{ID: "t2", TenantName: "y", CreatedAt: at(8), CompletedAt: tp(13)}, // activity 13
	}

	feed := mergeFeed(clusterOps, tenantOps)
	if len(feed) != 4 {
		t.Fatalf("len = %d, want 4", len(feed))
	}
	// Recency order across both kinds: c1(15) > t2(13) > t1(11) > c2(10).
	wantIDs := []string{"c1", "t2", "t1", "c2"}
	for i, want := range wantIDs {
		var gotID string
		switch feed[i].Kind {
		case "cluster":
			if feed[i].Cluster == nil || feed[i].Tenant != nil {
				t.Fatalf("item %d kind=cluster but pointers wrong (cluster=%v tenant=%v)", i, feed[i].Cluster, feed[i].Tenant)
			}
			gotID = feed[i].Cluster.ID
		case "tenant":
			if feed[i].Tenant == nil || feed[i].Cluster != nil {
				t.Fatalf("item %d kind=tenant but pointers wrong (cluster=%v tenant=%v)", i, feed[i].Cluster, feed[i].Tenant)
			}
			gotID = feed[i].Tenant.ID
		default:
			t.Fatalf("item %d unexpected kind %q", i, feed[i].Kind)
		}
		if gotID != want {
			t.Errorf("position %d: got %q, want %q", i, gotID, want)
		}
	}
}

// Each appended item must carry its OWN op (no loop-variable aliasing across the
// shared backing slice).
func TestMergeFeedNoPointerAliasing(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clusterOps := []repository.ClusterOperation{
		{ID: "c1", CreatedAt: base.Add(1 * time.Minute)},
		{ID: "c2", CreatedAt: base.Add(2 * time.Minute)},
	}
	feed := mergeFeed(clusterOps, nil)
	seen := map[string]bool{}
	for _, item := range feed {
		seen[item.Cluster.ID] = true
	}
	if !seen["c1"] || !seen["c2"] {
		t.Errorf("expected distinct c1 and c2, got %v", seen)
	}
}

// mergeFeed caps the result and keeps the most recent across the cap boundary.
func TestMergeFeedCap(t *testing.T) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var clusterOps []repository.ClusterOperation
	for i := 0; i < feedCap+10; i++ {
		clusterOps = append(clusterOps, repository.ClusterOperation{
			ID:        fmt.Sprintf("c%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	feed := mergeFeed(clusterOps, nil)
	if len(feed) != feedCap {
		t.Fatalf("len = %d, want cap %d", len(feed), feedCap)
	}
	// Highest i is newest, so it must survive at the head.
	if feed[0].Cluster.ID != fmt.Sprintf("c%d", feedCap+10-1) {
		t.Errorf("most recent dropped: head = %q", feed[0].Cluster.ID)
	}
}
