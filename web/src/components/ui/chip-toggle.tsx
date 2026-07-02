import type { ReactNode } from 'react';
import { Check } from 'lucide-react';
import { cn } from '@/lib/utils';

// A pill toggle — the compact, low-clutter stand-in for a checkbox + label in
// dense forms. Filled with a check when active; quiet outline when not.
export function ChipToggle({
  active,
  onClick,
  children,
  mono = false,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
  mono?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] transition-colors cursor-pointer',
        mono && 'font-mono',
        active
          ? 'border-primary/40 bg-primary/12 text-primary'
          : 'border-border text-muted-foreground hover:bg-hover hover:text-foreground',
      )}
    >
      {active && <Check className="h-3 w-3 shrink-0" />}
      {children}
    </button>
  );
}
