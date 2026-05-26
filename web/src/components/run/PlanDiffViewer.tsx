import { useMemo, useState } from "react";
import { cn } from "@/lib/utils";
import type { TofuPlanJSON, TofuResourceChange } from "@/api/types";
import { ChevronDown, ChevronRight } from "lucide-react";

interface Props {
  planOutput: string;
  planJSON?: TofuPlanJSON | null;
}

interface ResourceBlock {
  header: string;
  action: "create" | "update" | "destroy" | "read" | "replace" | "unknown";
  lines: string[];
}

function stripAnsi(str: string): string {
  // eslint-disable-next-line no-control-regex -- ANSI escape sequences begin with \x1b (ESC)
  return str.replace(/\x1b\[[0-9;]*m/g, "");
}

function parseAction(header: string): ResourceBlock["action"] {
  if (header.includes("will be created")) return "create";
  if (header.includes("will be updated") || header.includes("will be changed"))
    return "update";
  if (header.includes("will be destroyed")) return "destroy";
  if (header.includes("will be read")) return "read";
  if (header.includes("must be replaced")) return "replace";
  return "unknown";
}

function parsePlanOutput(raw: string): {
  preamble: string[];
  blocks: ResourceBlock[];
  summary: string[];
} {
  const clean = stripAnsi(raw);
  const lines = clean.split(/\r?\n/);

  const preamble: string[] = [];
  const blocks: ResourceBlock[] = [];
  const summary: string[] = [];

  let current: ResourceBlock | null = null;
  let inSummary = false;

  for (const line of lines) {
    if (inSummary) {
      summary.push(line);
      continue;
    }

    if (/^\s*#\s+\S+\s+will be /.test(line) || /^\s*#\s+\S+\s+must be /.test(line)) {
      if (current) blocks.push(current);
      current = {
        header: line.trim(),
        action: parseAction(line),
        lines: [],
      };
      continue;
    }

    if (/^Plan:/.test(line.trim()) || /^(No changes|Apply complete)/.test(line.trim())) {
      if (current) {
        blocks.push(current);
        current = null;
      }
      inSummary = true;
      summary.push(line);
      continue;
    }

    if (current) {
      current.lines.push(line);
    } else {
      preamble.push(line);
    }
  }

  if (current) blocks.push(current);

  return { preamble, blocks, summary };
}

const actionStyles: Record<ResourceBlock["action"], { border: string; bg: string; text: string; label: string }> = {
  create: { border: "border-green-500/30", bg: "bg-green-500/10", text: "text-green-400", label: "+" },
  update: { border: "border-yellow-500/30", bg: "bg-yellow-500/10", text: "text-yellow-400", label: "~" },
  destroy: { border: "border-red-500/30", bg: "bg-red-500/10", text: "text-red-400", label: "-" },
  replace: { border: "border-orange-500/30", bg: "bg-orange-500/10", text: "text-orange-400", label: "±" },
  read: { border: "border-blue-500/30", bg: "bg-blue-500/10", text: "text-blue-400", label: "≡" },
  unknown: { border: "border-border", bg: "bg-accent/10", text: "text-muted-foreground", label: "?" },
};

function actionsToType(actions: string[]): ResourceBlock["action"] {
  if (actions.includes("create") && actions.includes("delete")) return "replace";
  if (actions.includes("create")) return "create";
  if (actions.includes("delete")) return "destroy";
  if (actions.includes("update")) return "update";
  if (actions.includes("read")) return "read";
  return "unknown";
}

function formatValue(val: unknown): string {
  if (val === null || val === undefined) return "null";
  if (typeof val === "string") return `"${val}"`;
  if (typeof val === "object") return JSON.stringify(val, null, 2);
  return String(val);
}

function JSONResourceChange({ rc }: { rc: TofuResourceChange }) {
  const [expanded, setExpanded] = useState(false);
  const action = actionsToType(rc.change.actions);
  const style = actionStyles[action];

  const changedKeys = useMemo(() => {
    const before = rc.change.before ?? {};
    const after = rc.change.after ?? {};
    const allKeys = new Set([...Object.keys(before), ...Object.keys(after)]);
    const changed: { key: string; before: unknown; after: unknown; type: "added" | "removed" | "changed" }[] = [];
    for (const key of allKeys) {
      const b = before[key];
      const a = after[key];
      if (JSON.stringify(b) !== JSON.stringify(a)) {
        changed.push({
          key,
          before: b,
          after: a,
          type: b === undefined ? "added" : a === undefined ? "removed" : "changed",
        });
      }
    }
    return changed;
  }, [rc.change.before, rc.change.after]);

  return (
    <div className={cn("rounded-lg border overflow-hidden", style.border)}>
      <button
        onClick={() => setExpanded(!expanded)}
        className={cn("w-full px-4 py-2 flex items-center gap-2 text-sm font-medium font-mono text-left cursor-pointer", style.bg, style.text)}
      >
        <span className="text-base leading-none">{style.label}</span>
        <span className="flex-1">{rc.address}</span>
        {changedKeys.length > 0 && (
          <span className="text-xs opacity-70">{changedKeys.length} attribute(s)</span>
        )}
        {expanded ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
      </button>
      {expanded && changedKeys.length > 0 && (
        <div className="divide-y divide-border/50">
          {changedKeys.map(({ key, before, after, type }) => (
            <div key={key} className="px-4 py-2 text-xs font-mono">
              <span className="text-muted-foreground">{key}: </span>
              {type === "added" ? (
                <span className="text-green-400">{formatValue(after)}</span>
              ) : type === "removed" ? (
                <span className="text-red-400 line-through">{formatValue(before)}</span>
              ) : (
                <>
                  <span className="text-red-400 line-through">{formatValue(before)}</span>
                  <span className="text-muted-foreground mx-1">{"->"}</span>
                  <span className="text-green-400">{formatValue(after)}</span>
                </>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function PlanDiffViewer({ planOutput, planJSON }: Props) {
  // If we have structured JSON plan, render attribute-level diffs
  if (planJSON?.resource_changes?.length) {
    const changes = planJSON.resource_changes.filter(
      (rc) => !rc.change.actions.every((a) => a === "no-op")
    );

    if (changes.length > 0) {
      // Group by action
      const groups = {
        create: changes.filter((rc) => actionsToType(rc.change.actions) === "create"),
        update: changes.filter((rc) => actionsToType(rc.change.actions) === "update"),
        replace: changes.filter((rc) => actionsToType(rc.change.actions) === "replace"),
        destroy: changes.filter((rc) => actionsToType(rc.change.actions) === "destroy"),
        read: changes.filter((rc) => actionsToType(rc.change.actions) === "read"),
      };

      return (
        <div className="p-6 space-y-4">
          {/* Summary */}
          <div className="flex items-center gap-4 text-sm font-mono">
            {groups.create.length > 0 && <span className="text-green-400">+{groups.create.length} to add</span>}
            {groups.update.length > 0 && <span className="text-yellow-400">~{groups.update.length} to change</span>}
            {groups.replace.length > 0 && <span className="text-orange-400">±{groups.replace.length} to replace</span>}
            {groups.destroy.length > 0 && <span className="text-red-400">-{groups.destroy.length} to destroy</span>}
            {groups.read.length > 0 && <span className="text-blue-400">≡{groups.read.length} to read</span>}
          </div>
          {changes.map((rc) => (
            <JSONResourceChange key={rc.address} rc={rc} />
          ))}
        </div>
      );
    }
  }

  // Fall back to text-based parsing
  const { preamble, blocks, summary } = parsePlanOutput(planOutput);

  if (!blocks.length) {
    return (
      <div className="p-6 font-mono text-sm text-muted-foreground whitespace-pre-wrap">
        {stripAnsi(planOutput)}
      </div>
    );
  }

  return (
    <div className="p-6 space-y-4">
      {preamble.some((l) => l.trim()) && (
        <pre className="text-xs text-muted-foreground font-mono whitespace-pre-wrap">
          {preamble.join("\n").trim()}
        </pre>
      )}

      {blocks.map((block, i) => {
        const style = actionStyles[block.action];
        return (
          <div key={i} className={cn("rounded-lg border overflow-hidden", style.border)}>
            <div className={cn("px-4 py-2 flex items-center gap-2 text-sm font-medium font-mono", style.bg, style.text)}>
              <span className="text-base leading-none">{style.label}</span>
              <span>{block.header.replace(/^#\s*/, "")}</span>
            </div>
            {block.lines.some((l) => l.trim()) && (
              <pre className="px-4 py-3 text-xs font-mono text-foreground/80 overflow-x-auto whitespace-pre-wrap bg-card/50">
                {block.lines.join("\n").trim()}
              </pre>
            )}
          </div>
        );
      })}

      {summary.some((l) => l.trim()) && (
        <div className="rounded-lg border border-border bg-accent/20 px-4 py-3">
          <pre className="text-sm font-mono text-foreground whitespace-pre-wrap">
            {summary.join("\n").trim()}
          </pre>
        </div>
      )}
    </div>
  );
}
