package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nanohype/portal/internal/repository"
)

// OpsFeedService assembles the org-wide operations feed — cluster vends and
// tenant deploys merged into one activity stream, newest first. It's a pure
// read-model view over the two operation logs; it never writes. Clusters carry
// the rich regressible vend timeline (vend_phases); tenant operations terminate
// at commit (no watcher advances them further), so they ride as their status.
type OpsFeedService struct {
	queries *repository.Queries
}

func NewOpsFeedService(queries *repository.Queries) *OpsFeedService {
	return &OpsFeedService{queries: queries}
}

// OpsFeedItem is one entry in the merged feed: exactly one of Cluster/Tenant is
// set, discriminated by Kind. At is the activity time the feed sorts on —
// completed_at when the op has finished, else created_at — so a just-finished op
// floats to the top alongside fresh orders.
type OpsFeedItem struct {
	Kind    string                       `json:"kind"` // "cluster" | "tenant"
	At      time.Time                    `json:"at"`
	Cluster *repository.ClusterOperation `json:"cluster,omitempty"`
	Tenant  *repository.TenantOperation  `json:"tenant,omitempty"`
}

// feedCap bounds the merged feed. Each source query already caps at 50; the
// merge keeps the most recent feedCap across both so the page stays light.
const feedCap = 50

// Feed returns the merged, recency-sorted ops feed for an org.
func (s *OpsFeedService) Feed(ctx context.Context, orgID string) ([]OpsFeedItem, error) {
	clusterOps, err := s.queries.ListClusterOperationsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list cluster operations: %w", err)
	}
	tenantOps, err := s.queries.ListTenantOperationsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list tenant operations: %w", err)
	}
	return mergeFeed(clusterOps, tenantOps), nil
}

// mergeFeed interleaves the two operation logs into one feed, newest activity
// first, capped at feedCap. Pure, so the recency interleaving is unit-tested.
func mergeFeed(clusterOps []repository.ClusterOperation, tenantOps []repository.TenantOperation) []OpsFeedItem {
	items := make([]OpsFeedItem, 0, len(clusterOps)+len(tenantOps))
	for i := range clusterOps {
		op := clusterOps[i]
		items = append(items, OpsFeedItem{Kind: "cluster", At: activityTime(op.CreatedAt, op.CompletedAt), Cluster: &op})
	}
	for i := range tenantOps {
		op := tenantOps[i]
		items = append(items, OpsFeedItem{Kind: "tenant", At: activityTime(op.CreatedAt, op.CompletedAt), Tenant: &op})
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].At.After(items[b].At) })
	if len(items) > feedCap {
		items = items[:feedCap]
	}
	return items
}

// activityTime is when an op last changed: its completion if it has finished,
// otherwise when it was placed.
func activityTime(created time.Time, completed *time.Time) time.Time {
	if completed != nil {
		return *completed
	}
	return created
}
