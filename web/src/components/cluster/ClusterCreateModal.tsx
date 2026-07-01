import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Account } from "@/api/models";
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

export function ClusterCreateModal({
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
  const [description, setDescription] = useState("");
  const [environment, setEnvironment] = useState<
    "development" | "staging" | "production"
  >("production");
  const [apiEndpoint, setApiEndpoint] = useState("");
  const [caBundle, setCaBundle] = useState("");
  const [saToken, setSaToken] = useState("");
  const [region, setRegion] = useState("");

  const reset = () => {
    setAccountID("");
    setName("");
    setDescription("");
    setEnvironment("production");
    setApiEndpoint("");
    setCaBundle("");
    setSaToken("");
    setRegion("");
  };

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/clusters", {
        body: {
          account_id: accountID,
          name: name.trim(),
          description: description.trim() || undefined,
          environment,
          api_endpoint: apiEndpoint.trim(),
          ca_bundle: caBundle,
          sa_token: saToken,
          region: region.trim() || undefined,
        },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["clusters"] });
      toast.success("Cluster created · connection test running");
      reset();
      onClose();
    },
    onError: (e: unknown) => {
      const msg =
        (e as { message?: string })?.message ?? "Failed to create cluster";
      toast.error(msg);
    },
  });

  const apiEndpointInvalid =
    apiEndpoint !== "" && !apiEndpoint.startsWith("https://");
  const caBundleInvalid =
    caBundle !== "" &&
    !(caBundle.trimStart().startsWith("-----BEGIN") &&
      caBundle.includes("-----END"));
  const regionInvalid = region !== "" && !AWS_REGION_RE.test(region);

  const canSubmit =
    accountID !== "" &&
    name.trim() !== "" &&
    apiEndpoint.startsWith("https://") &&
    caBundle.trimStart().startsWith("-----BEGIN") &&
    caBundle.includes("-----END") &&
    saToken.trim() !== "" &&
    !regionInvalid;

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>New Cluster</DialogTitle>
          <DialogDescription>
            Register a Kubernetes cluster. The connection test runs
            asynchronously once you save.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Field label="Account">
            <Select
              value={accountID}
              onChange={(e) => setAccountID(e.target.value)}
            >
              <option value="">Pick an account…</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.aws_account_id})
                </option>
              ))}
            </Select>
          </Field>

          <Field label="Name">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="production-eks"
            />
          </Field>

          <Field label="Description (optional)">
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Primary production cluster"
            />
          </Field>

          <Field label="Environment">
            <Select
              value={environment}
              onChange={(e) =>
                setEnvironment(
                  e.target.value as "development" | "staging" | "production",
                )
              }
            >
              <option value="development">development</option>
              <option value="staging">staging</option>
              <option value="production">production</option>
            </Select>
          </Field>

          <Field
            label="API Endpoint"
            error={apiEndpointInvalid ? "Must start with https://" : null}
          >
            <Input
              value={apiEndpoint}
              onChange={(e) => setApiEndpoint(e.target.value)}
              placeholder="https://A1B2C3.gr7.us-west-2.eks.amazonaws.com"
              className="font-mono"
            />
          </Field>

          <Field
            label="CA Bundle (PEM)"
            error={
              caBundleInvalid
                ? "Must be a PEM-encoded certificate (-----BEGIN…-----END)"
                : null
            }
          >
            <textarea
              value={caBundle}
              onChange={(e) => setCaBundle(e.target.value)}
              placeholder="-----BEGIN CERTIFICATE-----&#10;MIID...&#10;-----END CERTIFICATE-----"
              rows={6}
              className="w-full border border-border/60 rounded-md bg-background/40 px-3 py-2 text-xs font-mono focus:outline-none focus:border-primary/40"
            />
          </Field>

          <Field label="Service Account Token">
            <Input
              type="password"
              value={saToken}
              onChange={(e) => setSaToken(e.target.value)}
              placeholder="Bearer token for a cluster-admin service account"
            />
          </Field>

          <Field
            label="Region (optional)"
            error={regionInvalid ? "Must look like us-west-2" : null}
          >
            <Input
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              placeholder="Defaults to parent account's region"
              className="font-mono"
            />
          </Field>

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => createMutation.mutate()}
              disabled={!canSubmit || createMutation.isPending}
            >
              {createMutation.isPending ? "Creating..." : "Create Cluster"}
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
      {error && (
        <p className="text-[11px] text-destructive mt-1">{error}</p>
      )}
    </div>
  );
}
