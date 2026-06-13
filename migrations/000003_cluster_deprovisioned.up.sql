-- A deprovision operation commits (its Cluster CR is removed from the GitOps
-- repo), then ArgoCD prunes and Crossplane runs tofu destroy — a 20–40 min
-- teardown the in-cluster watch-back observes by watching the Cluster XR
-- disappear from the hub. 'deprovisioned' is that terminal: teardown done.
-- (The in-flight 'deprovisioning' state lives in vend_phases, not here — the
-- status enum stays coarse, the phase map carries the substrate detail.)
ALTER TYPE cluster_op_status ADD VALUE IF NOT EXISTS 'deprovisioned';
