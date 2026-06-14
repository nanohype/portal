import { useState, useRef } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { Workspace, CreatePipelineStageInput } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { SkeletonRows } from "@/components/ui/skeleton";
import { Link } from "@/components/ui/link";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { GitBranch, Plus, Trash2, GripVertical } from "lucide-react";

export function PipelineList() {
  const [showCreate, setShowCreate] = useState(false);
  const queryClient = useQueryClient();

  const {
    data: pipelines,
    isLoading,
    isError,
  } = useQuery({
    queryKey: ["pipelines"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines");
      if (error) throw error;
      return data!;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/pipelines/{pipelineId}", {
        params: { path: { pipelineId: id } },
      });
      if (error) throw error;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      toast.success("Pipeline deleted");
    },
    onError: () => toast.error("Failed to delete pipeline"),
  });

  return (
    <div className="p-6">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Pipelines</h1>
          <p className="text-[12px] text-muted-foreground mt-1">
            Orchestrate sequential workspace deployments
          </p>
        </div>
        <Button size="sm" onClick={() => setShowCreate(true)}>
          <Plus className="w-3.5 h-3.5" />
          New Pipeline
        </Button>
      </div>

      {isLoading ? (
        <SkeletonRows />
      ) : isError ? (
        <div className="bg-destructive/8 text-destructive border border-destructive/15 rounded-lg p-4 text-sm">
          Failed to load pipelines.
        </div>
      ) : pipelines && pipelines.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-20 text-center animate-fade-up">
          <div className="w-12 h-12 rounded-lg bg-primary/8 flex items-center justify-center mb-4">
            <GitBranch className="w-5 h-5 text-primary/60" />
          </div>
          <h2 className="text-sm font-semibold mb-1">No pipelines yet</h2>
          <p className="text-xs text-muted-foreground mb-5 max-w-[260px]">
            Create a pipeline to orchestrate workspace deployments in sequence.
          </p>
          <Button size="sm" onClick={() => setShowCreate(true)}>
            <Plus className="w-3.5 h-3.5" />
            Create Pipeline
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          {pipelines?.map((p, i) => (
            <Link
              key={p.id}
              href={`/pipelines/${p.id}`}
              className="group block border border-border/60 rounded-lg px-4 py-3.5 hover:bg-accent/30 hover:border-primary/15 transition-all duration-150 animate-fade-up"
              style={{ animationDelay: `${i * 30}ms` }}
            >
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-3 min-w-0">
                  <div className="w-8 h-8 rounded-lg bg-primary/8 flex items-center justify-center shrink-0">
                    <GitBranch className="w-3.5 h-3.5 text-primary/70" />
                  </div>
                  <div className="min-w-0">
                    <span className="text-sm font-medium group-hover:text-primary transition-colors">
                      {p.name}
                    </span>
                    {p.description && (
                      <p className="text-xs text-muted-foreground break-words mt-0.5">
                        {p.description}
                      </p>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-3 shrink-0">
                  <span className="text-[11px] text-muted-foreground/70">
                    {new Date(p.created_at).toLocaleDateString()}
                  </span>
                  <button
                    onClick={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      if (confirm("Delete this pipeline?")) {
                        deleteMutation.mutate(p.id);
                      }
                    }}
                    className="p-1.5 rounded-md opacity-0 group-hover:opacity-100 hover:bg-destructive/10 text-muted-foreground hover:text-destructive transition-all duration-150 cursor-pointer"
                    aria-label="Delete pipeline"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                  </button>
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}

      <CreatePipelineDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
      />
    </div>
  );
}

function CreatePipelineDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [stages, setStages] = useState<CreatePipelineStageInput[]>([]);
  const queryClient = useQueryClient();

  const { data: workspaces } = useQuery({
    queryKey: ["workspaces-all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/workspaces", {
        params: { query: { per_page: 100 } },
      });
      if (error) throw error;
      return data?.data ?? [];
    },
    enabled: open,
  });

  const createMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/pipelines", {
        body: { name, description, stages },
      });
      if (error) throw error;
      return data!;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["pipelines"] });
      toast.success("Pipeline created");
      setName("");
      setDescription("");
      setStages([]);
      onClose();
    },
    onError: () => toast.error("Failed to create pipeline"),
  });

  const addStage = (workspaceId: string) => {
    setStages([
      ...stages,
      { workspace_id: workspaceId, auto_apply: true, on_failure: "stop" },
    ]);
  };

  const removeStage = (index: number) => {
    setStages(stages.filter((_, i) => i !== index));
  };

  // Drag-and-drop reorder
  const dragIndex = useRef<number | null>(null);
  const dragOverIndex = useRef<number | null>(null);
  const [dragActive, setDragActive] = useState<number | null>(null);
  const [dropTarget, setDropTarget] = useState<number | null>(null);

  const handleDragStart = (index: number) => {
    dragIndex.current = index;
    setDragActive(index);
  };

  const handleDragOver = (e: React.DragEvent, index: number) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    dragOverIndex.current = index;
    setDropTarget(index);
  };

  const handleDragEnd = () => {
    if (
      dragIndex.current !== null &&
      dragOverIndex.current !== null &&
      dragIndex.current !== dragOverIndex.current
    ) {
      const newStages = [...stages];
      const [moved] = newStages.splice(dragIndex.current, 1);
      newStages.splice(dragOverIndex.current, 0, moved);
      setStages(newStages);
    }
    dragIndex.current = null;
    dragOverIndex.current = null;
    setDragActive(null);
    setDropTarget(null);
  };

  const toggleAutoApply = (index: number) => {
    const newStages = [...stages];
    newStages[index] = {
      ...newStages[index],
      auto_apply: !newStages[index].auto_apply,
    };
    setStages(newStages);
  };

  const toggleOnFailure = (index: number) => {
    const newStages = [...stages];
    newStages[index] = {
      ...newStages[index],
      on_failure:
        newStages[index].on_failure === "stop" ? "continue" : "stop",
    };
    setStages(newStages);
  };

  const getWorkspaceName = (id: string) =>
    workspaces?.find((w: Workspace) => w.id === id)?.name ?? id;

  const availableWorkspaces = workspaces?.filter(
    (w: Workspace) => !stages.some((s) => s.workspace_id === w.id)
  );

  return (
    <Dialog open={open} onClose={onClose}>
      <DialogContent className="max-h-[80vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Create Pipeline</DialogTitle>
          <DialogDescription>
            Define an ordered sequence of workspace deployments.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div>
            <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
              Name
            </label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. landing-zone"
            />
          </div>

          <div>
            <label className="text-xs font-medium text-muted-foreground mb-1.5 block">
              Description
            </label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional description"
            />
          </div>

          <div>
            <label className="text-xs font-medium text-muted-foreground mb-2 block">
              Stages
            </label>

            {stages.length > 0 && (
              <div className="space-y-1 mb-3">
                {stages.map((stage, i) => (
                  <div
                    key={stage.workspace_id}
                    draggable
                    onDragStart={() => handleDragStart(i)}
                    onDragOver={(e) => handleDragOver(e, i)}
                    onDragEnd={handleDragEnd}
                    onDragLeave={() => setDropTarget(null)}
                    className={`flex items-center gap-2 border rounded-lg px-3 py-2.5 transition-all duration-150 select-none ${
                      dragActive === i
                        ? "opacity-40 scale-[0.97] border-primary/30 bg-primary/5"
                        : dropTarget === i && dragActive !== null && dragActive !== i
                        ? "border-primary/40 bg-primary/8 scale-[1.01]"
                        : "border-border/60 bg-background/30"
                    }`}
                  >
                    <div className="cursor-grab active:cursor-grabbing p-0.5 text-muted-foreground/50 hover:text-muted-foreground transition-colors">
                      <GripVertical className="w-3.5 h-3.5" />
                    </div>
                    <span className="text-[10px] text-muted-foreground font-mono w-4 text-center">
                      {i}
                    </span>
                    <span className="flex-1 text-sm font-medium break-words">
                      {getWorkspaceName(stage.workspace_id)}
                    </span>
                    <button
                      type="button"
                      onClick={() => toggleAutoApply(i)}
                      className="cursor-pointer"
                    >
                      <Badge
                        variant={stage.auto_apply ? "success" : "secondary"}
                      >
                        {stage.auto_apply ? "auto" : "manual"}
                      </Badge>
                    </button>
                    <button
                      type="button"
                      onClick={() => toggleOnFailure(i)}
                      className="cursor-pointer"
                    >
                      <Badge
                        variant={
                          stage.on_failure === "stop"
                            ? "destructive"
                            : "warning"
                        }
                      >
                        {stage.on_failure}
                      </Badge>
                    </button>
                    <button
                      type="button"
                      onClick={() => removeStage(i)}
                      className="p-1 text-muted-foreground hover:text-destructive cursor-pointer transition-colors"
                    >
                      <Trash2 className="w-3 h-3" />
                    </button>
                  </div>
                ))}
              </div>
            )}

            {availableWorkspaces && availableWorkspaces.length > 0 && (
              <Select
                value=""
                placeholder="Add workspace stage..."
                onChange={(e) => {
                  if (e.target.value) addStage(e.target.value);
                }}
              >
                <option value="">Add workspace stage...</option>
                {availableWorkspaces.map((w: Workspace) => (
                  <option key={w.id} value={w.id}>
                    {w.name}
                  </option>
                ))}
              </Select>
            )}
          </div>

          <div className="flex justify-end gap-2 pt-3 border-t border-border/40">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button
              size="sm"
              onClick={() => createMutation.mutate()}
              disabled={
                !name || stages.length === 0 || createMutation.isPending
              }
            >
              {createMutation.isPending ? "Creating..." : "Create Pipeline"}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
