-- Per-cluster health projections written by the in-cluster hub health watcher
-- (mirrors the connection_status / node_count / k8s_version pattern — denormalized
-- live facts captured periodically, read straight back by the cluster surface).
--
-- argocd_*: the per-cluster ArgoCD Application's sync + health, read from the hub
--   Application named cluster-<environment>-<name> (Synced/OutOfSync, Healthy/
--   Progressing/Degraded). Empty when no such Application exists (a hand-registered
--   cluster, or one whose CR was pruned).
-- control_plane_status / platform_version: from eks:DescribeCluster via the
--   account's assume-role (ACTIVE/UPDATING/DEGRADED, eks.N). The AWS-side control
--   plane lifecycle is distinct from kube-API reachability — a cluster can be
--   UPDATING while the API still answers. Empty when EKS describe isn't available
--   (no IAM permission, non-EKS, or not yet observed).
-- last_health_observed_at: when the health watcher last ran a check for this row.
ALTER TABLE clusters
    ADD COLUMN argocd_sync_status      TEXT NOT NULL DEFAULT '',
    ADD COLUMN argocd_health_status    TEXT NOT NULL DEFAULT '',
    ADD COLUMN control_plane_status    TEXT NOT NULL DEFAULT '',
    ADD COLUMN platform_version        TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_health_observed_at TIMESTAMPTZ;
