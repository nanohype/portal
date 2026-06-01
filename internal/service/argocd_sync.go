package service

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/kubernetes"

	"github.com/nanohype/portal/internal/k8s"
	"github.com/nanohype/portal/internal/repository"
)

// ArgoCDSyncService reconciles the portal's cluster inventory from ArgoCD's
// cluster registry — the Secrets in the argocd namespace. It lets a cluster be
// onboarded with zero manual portal registration: register it with ArgoCD (the
// cluster-bootstrap in-cluster Secret already does this) and the portal picks
// it up. Discovered clusters are attached to a single operator-configured
// org + account; their credentials come straight from ArgoCD (bearer token for
// remote clusters, the pod's own ServiceAccount for the in-cluster entry).
type ArgoCDSyncService struct {
	clusters  *ClusterService
	argocd    kubernetes.Interface
	namespace string
	orgID     string
	accountID string
	createdBy string
}

func NewArgoCDSyncService(clusters *ClusterService, argocd kubernetes.Interface, namespace, orgID, accountID, createdBy string) *ArgoCDSyncService {
	return &ArgoCDSyncService{
		clusters:  clusters,
		argocd:    argocd,
		namespace: namespace,
		orgID:     orgID,
		accountID: accountID,
		createdBy: createdBy,
	}
}

// Sync reads ArgoCD's registry and upserts the inventory. New clusters are
// created (and a connection-test enqueued); existing ones (matched by name) get
// their credentials refreshed. Returns created/updated/skipped counts.
func (s *ArgoCDSyncService) Sync(ctx context.Context) (created, updated, skipped int, err error) {
	discovered, skips, err := k8s.ListArgoCDClusters(ctx, s.argocd, s.namespace)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list argocd clusters: %w", err)
	}

	existing, _, err := s.clusters.List(ctx, s.orgID, "", 1, 1000)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("list existing clusters: %w", err)
	}
	byName := make(map[string]repository.Cluster, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}

	for _, dc := range discovered {
		// The in-cluster entry carries no token; it's watched via the pod's own
		// ServiceAccount (BuildRestConfig keys on the in-cluster API endpoint).
		ca, token := string(dc.CABundle), dc.BearerToken
		env := labelOr(dc.Labels, "environment", "development")
		region := labelOr(dc.Labels, "region", "")

		if ex, ok := byName[dc.Name]; ok {
			if _, err := s.clusters.Update(ctx, UpdateClusterParams{
				ID: ex.ID, OrgID: s.orgID, Name: dc.Name, Environment: env,
				APIEndpoint: dc.Server, CABundle: ca, SAToken: token, Region: region,
			}); err != nil {
				slog.Warn("argocd sync: update cluster", "name", dc.Name, "error", err)
				continue
			}
			updated++
			continue
		}

		c, err := s.clusters.Create(ctx, CreateClusterParams{
			OrgID: s.orgID, AccountID: s.accountID, Name: dc.Name, Environment: env,
			Description: "discovered from ArgoCD cluster registry",
			APIEndpoint: dc.Server, CABundle: ca, SAToken: token, Region: region,
			CreatedBy: s.createdBy,
		})
		if err != nil {
			slog.Warn("argocd sync: create cluster", "name", dc.Name, "error", err)
			continue
		}
		if err := s.clusters.EnqueueConnectionTest(ctx, c.ID, s.orgID); err != nil {
			slog.Warn("argocd sync: enqueue connection test", "name", dc.Name, "error", err)
		}
		created++
	}

	for _, sk := range skips {
		slog.Debug("argocd sync: skipped cluster", "detail", sk)
	}
	return created, updated, len(skips), nil
}

func labelOr(labels map[string]string, key, def string) string {
	if v, ok := labels[key]; ok && v != "" {
		// The bootstrap env tree uses "dev"; the portal labels clusters
		// "development". Map the common case, pass everything else through.
		if key == "environment" && v == "dev" {
			return "development"
		}
		return v
	}
	return def
}
