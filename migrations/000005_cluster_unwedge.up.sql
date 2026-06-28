-- Unwedge is the break-glass teardown for a spoke whose provider-opentofu
-- Workspace is stuck on crossplane's external-create-pending: a create call went
-- in flight and never reported back, so crossplane will neither finish creating
-- nor delete (it can't tell whether the external resources exist). The
-- operator-triggered unwedge op tears the spoke's tagged AWS resources down
-- directly — assumed into the workload account's fleet-unwedge role — then drops
-- the Workspace finalizers so the condemned object garbage-collects. It's its own
-- op kind so the timeline + audit read as break-glass, not a routine deprovision.
ALTER TYPE cluster_op_kind ADD VALUE IF NOT EXISTS 'unwedge';
