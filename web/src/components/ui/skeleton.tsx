import { cn } from "@/lib/utils";

// A loading placeholder block: a muted base with the shimmer sweeping across it
// (the same shimmer used elsewhere). Size it with className and compose a few
// into the shape of the content they stand in for, so the layout keeps its
// footprint while data loads instead of snapping in from a centered spinner.
export function Skeleton({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "relative overflow-hidden rounded-[6px] bg-muted/40",
        className,
      )}
      aria-hidden="true"
    >
      <div className="absolute inset-0 animate-shimmer" />
    </div>
  );
}

// Dense bordered-row lists (clusters, accounts, pipelines, tenants): an icon
// square, two stacked text lines, and a short right-aligned meta bar — the shape
// every one of those rows shares.
export function SkeletonRows({ rows = 5 }: { rows?: number }) {
  return (
    <div className="space-y-2" role="status" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div
          key={i}
          className="flex items-center justify-between rounded-lg border border-border/60 px-4 py-3.5"
        >
          <div className="flex min-w-0 flex-1 items-center gap-3">
            <Skeleton className="h-8 w-8 shrink-0 rounded-lg" />
            <div className="min-w-0 flex-1 space-y-2">
              <Skeleton className="h-3 w-40 max-w-[35%]" />
              <Skeleton className="h-2.5 w-56 max-w-[55%]" />
            </div>
          </div>
          <Skeleton className="h-2.5 w-14 shrink-0" />
        </div>
      ))}
    </div>
  );
}

// Card grid (workspaces): a title bar with a status pill, then a row of meta
// lines — matching the workspace card so the grid doesn't reflow on load.
export function SkeletonCards({ cards = 4 }: { cards?: number }) {
  return (
    <div className="grid gap-3" role="status" aria-label="Loading">
      {Array.from({ length: cards }).map((_, i) => (
        <div key={i} className="rounded-lg border border-border bg-card p-5">
          <div className="mb-3 flex items-center gap-3">
            <Skeleton className="h-4 w-44 max-w-[30%]" />
            <Skeleton className="h-4 w-20 rounded-full" />
          </div>
          <div className="flex items-center gap-4">
            <Skeleton className="h-2.5 w-24" />
            <Skeleton className="h-2.5 w-20" />
            <Skeleton className="h-2.5 w-16" />
          </div>
        </div>
      ))}
    </div>
  );
}
