import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { navigate } from "@/hooks/useNavigate";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Link } from "@/components/ui/link";
import { formatRelativeTime } from "@/lib/utils";
import { statusBadge } from "./ClusterList";
import {
  ArrowLeft,
  Server,
  Trash2,
  Save,
  Lock,
  Activity,
} from "lucide-react";

export function ClusterDetail({ clusterId }: { clusterId: string }) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery({
    queryKey: ["cluster", clusterId],
    queryFn: async () => {
      const { data, error } = await api.GET("/clusters/{clusterId}", {
        params: { path: { clusterId } },
      });
      if (error) throw error;
      return data!;
    },
    // Poll while connection test is in flight
    refetchInterval: (query) => {
      const s = query.state.data?.connection_status;
      return s === "pending" || s === "connecting" ? 2000 : false;
    },
  });

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [apiEndpoint, setApiEndpoint] = useState("");
  const [region, setRegion] = useState("");
  const [editingCreds, setEditingCreds] = useState(false);
  const [caBundle, setCaBundle] = useState("");
  const [saToken, setSaToken] = useState("");

  useEffect(() => {
    if (!data) return;
    setName(data.name);
    setDescription(data.description ?? "");
    setApiEndpoint(data.api_endpoint);
    setRegion(data.region);
  }, [data]);

  const updateMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, string> = {};
      if (data) {
        if (name !== data.name) body.name = name.trim();
        if (description !== (data.description ?? ""))
          body.description = description.trim();
        if (apiEndpoint !== data.api_endpoint)
          body.api_endpoint = apiEndpoint.trim();
        if (region !== data.region) body.region = region.trim();
      }
      if (editingCreds) {
        if (caBundle.trim() !== "") body.ca_bundle = caBundle;
        if (saToken.trim() !== "") body.sa_token = saToken;
      }
      const { data: updated, error } = await api.PUT(
        "/clusters/{clusterId}",
        { params: { path: { clusterId } }, body },
      );
      if (error) throw error;
      return updated!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["cluster", clusterId] });
      queryClient.invalidateQueries({ queryKey: ["clusters"] });
      toast.success("Cluster updated");
      setEditingCreds(false);
      setCaBundle("");
      setSaToken("");
    },
    onError: () => toast.error("Failed to update cluster"),
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/clusters/{clusterId}", {
        params: { path: { clusterId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["clusters"] });
      toast.success("Cluster deleted");
      navigate("/clusters");
    },
    onError: () => toast.error("Failed to delete cluster"),
  });

  const testMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/clusters/{clusterId}/test-connection",
        { params: { path: { clusterId } } },
      );
      if (error) throw error;
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["cluster", clusterId] });
      toast.success("Connection test enqueued");
    },
    onError: () => toast.error("Failed to enqueue connection test"),
  });

  if (isLoading) {
    return (
      <div className="flex justify-center py-20">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="p-6">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load cluster.
        </div>
      </div>
    );
  }

  const hasChanges =
    name !== data.name ||
    description !== (data.description ?? "") ||
    apiEndpoint !== data.api_endpoint ||
    region !== data.region ||
    (editingCreds && (caBundle.trim() !== "" || saToken.trim() !== ""));

  return (
    <div className="p-6 w-full max-w-3xl mx-auto flex-1 flex flex-col justify-center animate-fade-up">
      <Link
        href="/clusters"
        className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
      >
        <ArrowLeft className="w-3 h-3" />
        Clusters
      </Link>

      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-primary/8 flex items-center justify-center">
            <Server className="w-4 h-4 text-primary/70" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-lg font-semibold tracking-tight">
                {data.name}
              </h1>
              {statusBadge(data.connection_status)}
            </div>
            <p className="text-[12px] text-muted-foreground mt-0.5">
              {data.region}
              {data.k8s_version && ` · ${data.k8s_version}`}
              {data.node_count > 0 &&
                ` · ${data.node_count} node${data.node_count === 1 ? "" : "s"}`}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          {isAdmin && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => testMutation.mutate()}
              disabled={testMutation.isPending}
            >
              <Activity className="w-3 h-3" />
              Test connection
            </Button>
          )}
          {isAdmin && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                if (
                  confirm(`Delete cluster "${data.name}"? This cannot be undone.`)
                ) {
                  deleteMutation.mutate();
                }
              }}
              disabled={deleteMutation.isPending}
              className="text-destructive hover:text-destructive hover:bg-destructive/10 hover:border-destructive/30"
            >
              <Trash2 className="w-3 h-3" />
              Delete
            </Button>
          )}
        </div>
      </div>

      {data.connection_status === "failed" && data.connection_error && (
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-3 text-xs mb-4 break-words">
          <span className="font-semibold">Connection error:</span>{" "}
          {data.connection_error}
        </div>
      )}

      {data.last_connected_at && (
        <p className="text-[11px] text-muted-foreground/70 mb-4">
          Last reached {formatRelativeTime(data.last_connected_at)}
        </p>
      )}

      <div className="space-y-5">
        <FormRow label="Name">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={!isAdmin}
          />
        </FormRow>

        <FormRow label="Description">
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            disabled={!isAdmin}
            placeholder="Optional"
          />
        </FormRow>

        <FormRow
          label="Account"
          hint="Immutable. A cluster lives in exactly one account."
        >
          <div className="flex items-center gap-2">
            <Input value={data.account_id} disabled className="font-mono" />
            <Lock className="w-3.5 h-3.5 text-muted-foreground/50" />
          </div>
        </FormRow>

        <FormRow label="API Endpoint">
          <Input
            value={apiEndpoint}
            onChange={(e) => setApiEndpoint(e.target.value)}
            disabled={!isAdmin}
            className="font-mono"
          />
        </FormRow>

        <FormRow label="Region">
          <Input
            value={region}
            onChange={(e) => setRegion(e.target.value)}
            disabled={!isAdmin}
            className="font-mono"
          />
        </FormRow>

        <FormRow
          label="Credentials"
          hint="CA bundle + service-account token. Stored encrypted; replace below if they rotate."
        >
          {editingCreds ? (
            <div className="space-y-2">
              <textarea
                value={caBundle}
                onChange={(e) => setCaBundle(e.target.value)}
                placeholder="-----BEGIN CERTIFICATE----- (paste new CA, leave empty to keep)"
                rows={4}
                className="w-full border border-border/60 rounded-md bg-background/40 px-3 py-2 text-xs font-mono focus:outline-none focus:border-primary/40"
              />
              <Input
                type="password"
                value={saToken}
                onChange={(e) => setSaToken(e.target.value)}
                placeholder="New SA token (leave empty to keep)"
              />
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setEditingCreds(false);
                  setCaBundle("");
                  setSaToken("");
                }}
              >
                Cancel
              </Button>
            </div>
          ) : (
            isAdmin && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setEditingCreds(true)}
              >
                Replace credentials
              </Button>
            )
          )}
        </FormRow>

        {isAdmin && (
          <div className="flex justify-end pt-2">
            <Button
              size="sm"
              onClick={() => updateMutation.mutate()}
              disabled={
                !hasChanges || updateMutation.isPending || name.trim() === ""
              }
            >
              <Save className="w-3 h-3" />
              {updateMutation.isPending ? "Saving..." : "Save changes"}
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}

function FormRow({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {hint && (
        <p className="text-[11px] text-muted-foreground/70 mt-1">{hint}</p>
      )}
    </div>
  );
}
