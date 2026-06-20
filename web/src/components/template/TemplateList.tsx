import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useAuth } from "@/hooks/useAuth";
import type { Template } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { useConfirm } from "@/components/ui/confirm";
import { TemplateEditorDialog } from "./TemplateEditorDialog";
import { LayoutTemplate, Plus, Trash2, Pencil } from "lucide-react";

export function TemplateList() {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";
  const queryClient = useQueryClient();
  const confirm = useConfirm();
  const [editing, setEditing] = useState<Template | null>(null);
  const [creating, setCreating] = useState(false);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["templates"],
    queryFn: async () => {
      const { data, error } = await api.GET("/templates");
      if (error) throw error;
      return data?.data ?? [];
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/templates/{templateID}", {
        params: { path: { templateID: id } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["templates"] });
      toast.success("Template deleted");
    },
    onError: () => toast.error("Failed to delete template"),
  });

  const templates = data ?? [];

  return (
    <div className="p-6 flex flex-col flex-1">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Templates</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            Curated tenant flavors. Operators instantiate from these.
          </p>
        </div>
        {isAdmin && (
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus className="w-3.5 h-3.5" />
            New Template
          </Button>
        )}
      </div>

      {isLoading ? (
        <div className="flex-1 flex items-center justify-center">
          <Spinner className="w-6 h-6 text-primary" />
        </div>
      ) : isError ? (
        <div className="flex-1 flex flex-col items-center justify-center">
          <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
            Failed to load templates.
          </div>
        </div>
      ) : templates.length === 0 ? (
        <div className="flex-1 flex flex-col items-center justify-center text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <LayoutTemplate className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No templates yet</h2>
          <p className="text-xs text-muted-foreground mb-5 max-w-[320px]">
            Define curated tenant flavors so operators can instantiate them
            without filling out every field.
          </p>
          {isAdmin && (
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus className="w-3.5 h-3.5" />
              Create Template
            </Button>
          )}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3">
          {templates.map((t, i) => (
            <div
              key={t.id}
              className="group block border border-border/60 rounded-lg px-4 py-3.5 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
              style={{ animationDelay: `${i * 30}ms` }}
            >
              <div className="flex items-start justify-between mb-2">
                <div className="min-w-0">
                  <div className="flex items-center gap-2 mb-0.5">
                    <span className="text-sm font-medium">{t.name}</span>
                    <Badge variant="secondary">{t.persona}</Badge>
                  </div>
                  {t.description && (
                    <p className="text-[11px] text-muted-foreground break-words">
                      {t.description}
                    </p>
                  )}
                </div>
                {isAdmin && (
                  <div className="flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
                    <button
                      onClick={() => setEditing(t)}
                      className="p-1.5 rounded-md hover:bg-hover text-muted-foreground hover:text-foreground cursor-pointer"
                      aria-label="Edit"
                    >
                      <Pencil className="w-3 h-3" />
                    </button>
                    <button
                      onClick={async () => {
                        if (
                          await confirm({
                            title: `Delete template "${t.name}"?`,
                            confirmLabel: "Delete",
                          })
                        ) {
                          deleteMutation.mutate(t.id);
                        }
                      }}
                      className="p-1.5 rounded-md hover:bg-destructive/10 text-muted-foreground hover:text-destructive cursor-pointer"
                      aria-label="Delete"
                    >
                      <Trash2 className="w-3 h-3" />
                    </button>
                  </div>
                )}
              </div>
              <div className="flex flex-wrap items-center gap-1.5 mt-2 text-[10px] text-muted-foreground">
                {t.max_budget_usd > 0 && (
                  <span className="font-mono">
                    ≤ ${t.max_budget_usd.toLocaleString()}/mo
                  </span>
                )}
                {t.allowed_model_families.length > 0 && (
                  <span className="font-mono">
                    {t.allowed_model_families.join(" · ")}
                  </span>
                )}
                {t.required_compliance.length > 0 && (
                  <span className="text-warning font-mono">
                    requires {t.required_compliance.join(", ")}
                  </span>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <TemplateEditorDialog
        open={creating || !!editing}
        onClose={() => {
          setCreating(false);
          setEditing(null);
        }}
        existing={editing}
      />
    </div>
  );
}
