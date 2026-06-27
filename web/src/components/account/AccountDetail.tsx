import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import { navigate } from "@/hooks/useNavigate";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Badge } from "@/components/ui/badge";
import { Link } from "@/components/ui/link";
import { useConfirm } from "@/components/ui/confirm-context";
import {
  ArrowLeft,
  Cloud,
  Trash2,
  Save,
  Pencil,
  Lock,
  CheckCircle2,
} from "lucide-react";

const AWS_ROLE_ARN_RE = /^arn:aws[a-z-]*:iam::(\d{12}):role\/.+$/;
const AWS_REGION_RE = /^[a-z]{2}-[a-z]+-\d$/;

export function AccountDetail({ accountId }: { accountId: string }) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const queryClient = useQueryClient();
  const confirm = useConfirm();

  const { data, isLoading, isError } = useQuery({
    queryKey: ["account", accountId],
    queryFn: async () => {
      const { data, error } = await api.GET("/accounts/{accountId}", {
        params: { path: { accountId } },
      });
      if (error) throw error;
      return data!;
    },
  });

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [assumeRoleARN, setAssumeRoleARN] = useState("");
  const [defaultRegion, setDefaultRegion] = useState("");
  const [editingExternalID, setEditingExternalID] = useState(false);
  const [externalID, setExternalID] = useState("");

  useEffect(() => {
    if (!data) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional one-time sync of editable form fields from fetched data
    setName(data.name);
    setDescription(data.description ?? "");
    setAssumeRoleARN(data.assume_role_arn);
    setDefaultRegion(data.default_region);
  }, [data]);

  const updateMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, string> = {};
      if (data) {
        if (name !== data.name) body.name = name.trim();
        if (description !== (data.description ?? ""))
          body.description = description.trim();
        if (assumeRoleARN !== data.assume_role_arn)
          body.assume_role_arn = assumeRoleARN.trim();
        if (defaultRegion !== data.default_region)
          body.default_region = defaultRegion.trim();
      }
      if (editingExternalID && externalID.trim() !== "") {
        body.external_id = externalID.trim();
      }
      const { data: updated, error } = await api.PUT(
        "/accounts/{accountId}",
        {
          params: { path: { accountId } },
          body,
        },
      );
      if (error) throw error;
      return updated!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["account", accountId] });
      queryClient.invalidateQueries({ queryKey: ["accounts"] });
      toast.success("Account updated");
      setEditingExternalID(false);
      setExternalID("");
    },
    onError: () => toast.error("Failed to update account"),
  });

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/accounts/{accountId}", {
        params: { path: { accountId } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["accounts"] });
      toast.success("Account deleted");
      navigate("/accounts");
    },
    onError: () => toast.error("Failed to delete account"),
  });

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Spinner className="w-6 h-6 text-primary" />
      </div>
    );
  }

  if (isError || !data) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center">
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load account.
        </div>
      </div>
    );
  }

  const arnAccountMatch = assumeRoleARN.match(AWS_ROLE_ARN_RE)?.[1];
  const arnMismatch =
    arnAccountMatch !== undefined && arnAccountMatch !== data.aws_account_id;
  const arnInvalid = assumeRoleARN !== "" && !AWS_ROLE_ARN_RE.test(assumeRoleARN);
  const regionInvalid =
    defaultRegion !== "" && !AWS_REGION_RE.test(defaultRegion);

  const hasChanges =
    name !== data.name ||
    description !== (data.description ?? "") ||
    assumeRoleARN !== data.assume_role_arn ||
    defaultRegion !== data.default_region ||
    (editingExternalID && externalID.trim() !== "");

  const canSave =
    hasChanges &&
    name.trim() !== "" &&
    !arnInvalid &&
    !arnMismatch &&
    !regionInvalid;

  return (
    <div className="p-6 w-full max-w-3xl mx-auto flex-1 flex flex-col animate-fade-up">
      <Link
        href="/accounts"
        className="text-xs text-muted-foreground hover:text-foreground inline-flex items-center gap-1 mb-3 transition-colors"
      >
        <ArrowLeft className="w-3 h-3" />
        Accounts
      </Link>

      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-primary/8 flex items-center justify-center">
            <Cloud className="w-4 h-4 text-primary/70" />
          </div>
          <div>
            <h1 className="text-lg font-semibold tracking-tight">
              {data.name}
            </h1>
            <p className="text-[12px] text-muted-foreground mt-0.5 font-mono">
              {data.aws_account_id}
            </p>
          </div>
        </div>
        {isAdmin && (
          <Button
            variant="outline"
            size="sm"
            onClick={async () => {
              if (
                await confirm({
                  title: `Delete account "${data.name}"?`,
                  message: "This cannot be undone.",
                  confirmLabel: "Delete",
                })
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
          label="AWS Account ID"
          hint="Immutable. The account ID identifies the AWS account itself."
        >
          <div className="flex items-center gap-2">
            <Input
              value={data.aws_account_id}
              disabled
              className="font-mono"
            />
            <Lock className="w-3.5 h-3.5 text-muted-foreground/50" />
          </div>
        </FormRow>

        <FormRow
          label="Assume Role ARN"
          error={
            arnInvalid
              ? "Must look like arn:aws:iam::<account>:role/<name>"
              : arnMismatch
              ? "Account in ARN does not match aws_account_id"
              : null
          }
        >
          <Input
            value={assumeRoleARN}
            onChange={(e) => setAssumeRoleARN(e.target.value)}
            disabled={!isAdmin}
            className="font-mono"
          />
        </FormRow>

        <FormRow
          label="Default Region"
          error={regionInvalid ? "Must look like us-west-2" : null}
        >
          <Input
            value={defaultRegion}
            onChange={(e) => setDefaultRegion(e.target.value)}
            disabled={!isAdmin}
            className="font-mono"
          />
        </FormRow>

        <FormRow
          label="External ID"
          hint="Shared secret used in the role's trust policy. Stored encrypted; never displayed."
        >
          {editingExternalID ? (
            <div className="flex items-center gap-2">
              <Input
                type="password"
                value={externalID}
                onChange={(e) => setExternalID(e.target.value)}
                placeholder="Enter new value"
                autoFocus
              />
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setEditingExternalID(false);
                  setExternalID("");
                }}
              >
                Cancel
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <Badge variant={data.external_id_set ? "success" : "secondary"}>
                {data.external_id_set ? (
                  <>
                    <CheckCircle2 className="w-3 h-3" />
                    Set
                  </>
                ) : (
                  "Not set"
                )}
              </Badge>
              {isAdmin && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setEditingExternalID(true)}
                >
                  <Pencil className="w-3 h-3" />
                  {data.external_id_set ? "Replace" : "Set"}
                </Button>
              )}
            </div>
          )}
        </FormRow>

        {isAdmin && (
          <div className="flex justify-end pt-2">
            <Button
              size="sm"
              onClick={() => updateMutation.mutate()}
              disabled={!canSave || updateMutation.isPending}
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
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <div>
      <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
        {label}
      </label>
      {children}
      {error ? (
        <p className="text-[11px] text-destructive mt-1">{error}</p>
      ) : hint ? (
        <p className="text-[11px] text-muted-foreground/70 mt-1">{hint}</p>
      ) : null}
    </div>
  );
}
