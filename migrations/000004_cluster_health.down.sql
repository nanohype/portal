ALTER TABLE clusters
    DROP COLUMN argocd_sync_status,
    DROP COLUMN argocd_health_status,
    DROP COLUMN control_plane_status,
    DROP COLUMN platform_version,
    DROP COLUMN last_health_observed_at;
