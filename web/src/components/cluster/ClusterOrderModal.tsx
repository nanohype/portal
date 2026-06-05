import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Account } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";

const AWS_REGION_RE = /^[a-z]{2}-[a-z]+-\d$/;
const K8S_NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

// ClusterOrderModal is the vend "order desk": it produces an eks-fleet Cluster
// CR (committed to the clusters GitOps repo) rather than registering an
// existing cluster. Once the cluster comes up, the provision watch-back
// auto-registers it — so there's no credential entry here.
export function ClusterOrderModal({
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

  const selectedAccount = accounts.find((a) => a.id === accountID);

  const reset = () => {
    setAccountID("");
    setName("");
    setTeam("");
    setEnvironment("dev");
    setRegion("");
    setClusterVersion("");
    setPublicAccess(true);
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
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["clusters"] });
      toast.success(
        "Cluster ordered · provisioning, then it self-registers when ready",
      );
      reset();
      onClose();
    },
    onError: (e: unknown) => {
      const msg =
        (e as { message?: string })?.message ?? "Failed to order cluster";
      toast.error(msg);
    },
  });

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

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Provision Cluster</DialogTitle>
          <DialogDescription>
            Order a new EKS cluster. Portal commits a Cluster definition to the
            fleet; Crossplane vends it, and it registers itself here once its
            API is up.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Field label="Account">
            <Select value={accountID} onChange={(e) => onPickAccount(e.target.value)}>
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
            error={nameInvalid ? "Lowercase letters, digits, and dashes" : null}
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
              placeholder="Defaults to the fleet default (e.g. 1.35)"
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

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => orderMutation.mutate()}
              disabled={!canSubmit || orderMutation.isPending}
            >
              {orderMutation.isPending ? "Ordering..." : "Provision Cluster"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
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
