import { useEffect } from "react";
import { Command } from "cmdk";
import { Search } from "lucide-react";
import { navCategories } from "@/lib/nav";
import { navigate } from "@/hooks/useNavigate";
import { useAuth } from "@/hooks/useAuth";

export function CommandPalette({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin" || user?.role === "owner";

  // Close on Escape
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onOpenChange(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onOpenChange]);

  if (!open) return null;

  const go = (href: string) => {
    navigate(href);
    onOpenChange(false);
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center pt-[18vh] bg-black/50 backdrop-blur-sm animate-fade-overlay"
      onClick={() => onOpenChange(false)}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-lg mx-4 glass rounded-xl shadow-2xl shadow-black/40 overflow-hidden animate-scale-in"
      >
        <Command loop className="outline-none">
          <div className="flex items-center gap-2 px-3.5 border-b border-border">
            <Search className="w-4 h-4 text-dim shrink-0" />
            <Command.Input
              autoFocus
              placeholder="Search or jump to…"
              className="flex-1 bg-transparent py-3 text-[13px] text-foreground placeholder:text-dim outline-none"
            />
          </div>
          <Command.List className="max-h-80 overflow-auto p-2">
            <Command.Empty className="py-8 text-center text-[12px] text-muted-foreground">
              No results.
            </Command.Empty>
            {navCategories.map((cat) => {
              const items = cat.items.filter((i) => !i.adminOnly || isAdmin);
              if (!items.length) return null;
              return (
                <Command.Group key={cat.label} heading={cat.label}>
                  {items.map((item) => (
                    <Command.Item
                      key={item.href}
                      value={item.label}
                      keywords={[cat.label]}
                      onSelect={() => go(item.href)}
                      className="flex items-center gap-2.5 px-2.5 py-2 rounded-[6px] text-[13px] text-muted-foreground cursor-pointer data-[selected=true]:bg-primary/10 data-[selected=true]:text-primary"
                    >
                      <item.icon className="w-4 h-4 shrink-0" />
                      {item.label}
                    </Command.Item>
                  ))}
                </Command.Group>
              );
            })}
          </Command.List>
        </Command>
      </div>
    </div>
  );
}
