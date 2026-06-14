import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { navigate } from "@/hooks/useNavigate";
import type { Account, ClusterOperation } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  Drawer,
  DrawerHeader,
  DrawerTitle,
  DrawerDescription,
  DrawerBody,
  DrawerFooter,
} from "@/components/ui/drawer";
import { cn } from "@/lib/utils";
import { VendTimeline } from "./VendTimeline";

const AWS_REGION_RE = /^[a-z]{2}-[a-z]+-\d$/;
const K8S_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

// The vend "order desk" as a slide-over: it produces an eks-fleet Cluster CR
// (committed to the clusters GitOps repo) rather than registering an existing
// cluster. On submit the drawer morphs from the form into a live provisioning
// view for the just-placed order — you watch it advance in place instead of
// hunting the page for it. The provision watch-back auto-registers the cluster
// once it comes up, so there's no credential entry here.
export function ClusterProvisionDrawer({
  open,
  onClose,
  accounts,
}: {
  open: boolean;
  onClose: () => void;
  accounts: Account[];
}) {
  const queryClient = useQueryClient();
  const [accountID, setAccountID] = useState("");
  const [name, setName] = useState("");
  const [team, setTeam] = useState("");
  const [environment, setEnvironment] = useState<
    "dev" | "staging" | "production"
  >("dev");
  const [region, setRegion] = useState("");
  const [clusterVersion, setClusterVersion] = useState("");
  const [publicAccess, setPublicAccess] = useState(true);
  // Set on a successful order → the drawer switches to the live timeline view.
  const [orderedId, setOrderedId] = useState<string | null>(null);

  const selectedAccount = accounts.find((a) => a.id === accountID);

  const reset = () => {
    setAccountID("");
    setName("");
    setTeam("");
    setEnvironment("dev");
    setRegion("");
    setClusterVersion("");
    setPublicAccess(true);
    setOrderedId(null);
  };

  // Picking an account pre-fills the region with its default — most vends land
  // in the account's home region; the operator can still override.
  const onPickAccount = (id: string) => {
    setAccountID(id);
    const acct = accounts.find((a) => a.id === id);
    if (acct && region === "") setRegion(acct.default_region);
  };

  const orderMutation = useMutation({
    mutationFn: async () => {
      if (!selectedAccount) throw new Error("Pick an account");
      const { data, error } = await api.POST("/cluster-orders", {
        body: {
          name: name.trim(),
          account: selectedAccount.aws_account_id,
          region: region.trim(),
          team: team.trim(),
          environment,
          cluster_version: clusterVersion.trim() || undefined,
          endpoint_public_access: publicAccess,
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: (op) => {
      // The order renders from the ['cluster-operations'] surface — seed the new
      // op into that cache so it appears instantly (the old bug invalidated only
      // ['clusters'], so a fresh order didn't show until the next poll tick),
      // then invalidate the order surfaces to reconcile with the server.
      queryClient.setQueryData<ClusterOperation[]>(
        ["cluster-operations"],
        (prev) => [op, ...(prev ?? []).filter((o) => o.id !== op.id)],
      );
      queryClient.invalidateQueries({ queryKey: ["cluster-operations"] });
      queryClient.invalidateQueries({ queryKey: ["ops-feed"] });
      queryClient.invalidateQueries({ queryKey: ["clusters"] });
      toast.success(`Provisioning ${name.trim()} · self-registers when ready`, {
        action: { label: "Ops", onClick: () => navigate("/ops") },
      });
      setOrderedId(op.id); // morph to the live view — don't close
    },
    onError: (e: unknown) => {
      toast.error(
        (e as { message?: string })?.message ?? "Failed to order cluster",
      );
    },
  });

  // Live op for the result view. Shares the ['cluster-operations'] cache, but
  // polls it itself while an order is in flight so the timeline advances
  // queued → committed → building → active no matter which page opened the
  // drawer (don't lean on ClusterList being mounted). Self-limiting: stops once
  // the op reaches a terminal or a portal-side failure.
  const { data: ops } = useQuery({
    queryKey: ["cluster-operations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/cluster-orders");
      if (error) throw error;
      return data!;
    },
    enabled: orderedId !== null,
    refetchInterval: (query) => {
      if (orderedId === null) return false;
      const op = query.state.data?.find((o) => o.id === orderedId);
      if (!op) return 3000; // just placed, not in cache yet — keep checking
      if (op.status === "active" || op.status === "failed" || op.status === "deprovisioned")
        return false;
      if ("failed" in (op.vend_phases ?? {})) return false;
      return 3000;
    },
  });
  const liveOp = ops?.find((o) => o.id === orderedId) ?? null;

  const handleClose = () => {
    reset();
    onClose();
  };

  const regionInvalid = region !== "" && !AWS_REGION_RE.test(region);
  const teamInvalid = team !== "" && !K8S_NAME_RE.test(team);
  const nameInvalid = name !== "" && !K8S_NAME_RE.test(name);
  const canSubmit =
    accountID !== "" &&
    name.trim() !== "" &&
    !nameInvalid &&
    team.trim() !== "" &&
    !teamInvalid &&
    AWS_REGION_RE.test(region);

  const ordered = orderedId !== null;

  return (
    <Drawer open={open} onClose={handleClose}>
      <DrawerHeader onClose={handleClose}>
        <DrawerTitle>
          {ordered ? `Provisioning ${name.trim()}` : "Provision Cluster"}
        </DrawerTitle>
        <DrawerDescription>
          {ordered
            ? "Portal committed the Cluster definition. Crossplane vends it; it registers here once its API is up."
            : "Order a new EKS cluster. Portal commits a Cluster definition to the fleet; Crossplane vends it."}
        </DrawerDescription>
      </DrawerHeader>

      <DrawerBody>
        {ordered ? (
          <div className="animate-fade-in space-y-6">
            {liveOp ? (
              <VendTimeline op={liveOp} />
            ) : (
              <div className="text-xs text-muted-foreground">
                Committing the order…
              </div>
            )}
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-[12px]">
              <Detail label="Account" value={selectedAccount?.name ?? "—"} />
              <Detail label="Region" value={region} mono />
              <Detail label="Environment" value={environment} />
              <Detail label="Team" value={team.trim()} mono />
            </dl>
            <p className="text-[12px] leading-relaxed text-muted-foreground">
              The full journey — commit → build → active — streams in{" "}
              <span className="text-foreground">Ops</span>. You can close this and
              it keeps going.
            </p>
          </div>
        ) : (
          <div className="space-y-4">
            <Field label="Account">
              <Select
                value={accountID}
                onChange={(e) => onPickAccount(e.target.value)}
              >
                <option value="">Pick an account…</option>
                {accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name} ({a.aws_account_id})
                  </option>
                ))}
              </Select>
            </Field>

            <Field
              label="Name"
              error={
                nameInvalid ? "Lowercase letters, digits, and dashes" : null
              }
            >
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="eks"
                className="font-mono"
              />
            </Field>

            <Field
              label="Team"
              error={
                teamInvalid
                  ? "Lowercase letters, digits, and dashes (k8s namespace)"
                  : null
              }
            >
              <Input
                value={team}
                onChange={(e) => setTeam(e.target.value)}
                placeholder="platform"
                className="font-mono"
              />
            </Field>

            <Field label="Environment">
              <Select
                value={environment}
                onChange={(e) =>
                  setEnvironment(
                    e.target.value as "dev" | "staging" | "production",
                  )
                }
              >
                <option value="dev">dev</option>
                <option value="staging">staging</option>
                <option value="production">production</option>
              </Select>
            </Field>

            <Field
              label="Region"
              error={regionInvalid ? "Must look like us-west-2" : null}
            >
              <Input
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                placeholder="us-west-2"
                className="font-mono"
              />
            </Field>

            <Field label="Kubernetes Version (optional)">
              <Input
                value={clusterVersion}
                onChange={(e) => setClusterVersion(e.target.value)}
                placeholder="Defaults to the fleet default (e.g. 1.36)"
                className="font-mono"
              />
            </Field>

            <label className="flex items-center gap-2 text-xs text-muted-foreground cursor-pointer">
              <input
                type="checkbox"
                checked={publicAccess}
                onChange={(e) => setPublicAccess(e.target.checked)}
                className="accent-primary"
              />
              Public API endpoint
            </label>
          </div>
        )}
      </DrawerBody>

      <DrawerFooter>
        {ordered ? (
          <>
            <Button variant="ghost" size="sm" onClick={handleClose}>
              Done
            </Button>
            <Button
              size="sm"
              onClick={() => {
                navigate("/ops");
                handleClose();
              }}
            >
              Watch in Ops
            </Button>
          </>
        ) : (
          <>
            <Button variant="ghost" size="sm" onClick={handleClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => orderMutation.mutate()}
              disabled={!canSubmit || orderMutation.isPending}
            >
              {orderMutation.isPending ? "Provisioning…" : "Provision Cluster"}
            </Button>
          </>
        )}
      </DrawerFooter>
    </Drawer>
  );
}

function Field({
  label,
  error,
  children,
}: {
  label: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {error && <p className="text-[11px] text-destructive mt-1">{error}</p>}
    </div>
  );
}

function Detail({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn("text-foreground", mono && "font-mono")}>{value}</dd>
    </>
  );
}
