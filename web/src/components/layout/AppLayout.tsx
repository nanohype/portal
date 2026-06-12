import { useState, useRef, useEffect } from "react";
import type { ReactNode } from "react";
import { useAuth } from "@/hooks/useAuth";
import { useLocation } from "@/hooks/useNavigate";
import { Link } from "@/components/ui/link";
import { navCategories } from "@/lib/nav";
import { CommandPalette } from "@/components/CommandPalette";
import { Box, Search, LogOut } from "lucide-react";

export function AppLayout({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const location = useLocation();
  const path = location.split("?")[0];
  const [showUserMenu, setShowUserMenu] = useState(false);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const isAdmin = user?.role === "admin" || user?.role === "owner";

  // ⌘K / Ctrl+K toggles the command palette
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (!showUserMenu) return;
    const handleClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setShowUserMenu(false);
      }
    };
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [showUserMenu]);

  return (
    <div className="h-screen flex">
      {/* Sidebar */}
      <aside className="w-56 shrink-0 border-r border-border bg-frosted flex flex-col">
        {/* Logo */}
        <div className="h-11 shrink-0 flex items-center px-4 border-b border-border">
          <Link href="/" className="flex items-center gap-2">
            <div className="w-6 h-6 rounded-[5px] bg-primary/10 flex items-center justify-center">
              <Box className="w-3 h-3 text-primary" />
            </div>
            <span className="font-mono text-[11px] uppercase tracking-[0.12em] text-dim">
              portal
            </span>
          </Link>
        </div>

        {/* Search trigger */}
        <div className="p-3">
          <button
            onClick={() => setPaletteOpen(true)}
            className="w-full flex items-center gap-2 px-2.5 py-1.5 rounded-[6px] text-[12px] text-dim border border-input-border bg-input/40 hover:text-foreground hover:bg-hover transition-colors cursor-pointer"
          >
            <Search className="w-3.5 h-3.5 shrink-0" />
            <span>Search…</span>
            <kbd className="ml-auto text-[10px] font-mono text-dim border border-border rounded px-1 py-px">
              ⌘K
            </kbd>
          </button>
        </div>

        {/* Nav */}
        <nav className="flex-1 overflow-auto px-3 pb-3 space-y-5" aria-label="Primary">
          {navCategories.map((cat) => {
            const items = cat.items.filter((i) => !i.adminOnly || isAdmin);
            if (!items.length) return null;
            return (
              <div key={cat.label}>
                <div className="px-2.5 mb-1 text-[10px] font-medium uppercase tracking-wider text-dim">
                  {cat.label}
                </div>
                <div className="space-y-0.5">
                  {items.map((item) => {
                    const active = item.match(path);
                    return (
                      <Link
                        key={item.href}
                        href={item.href}
                        className={`flex items-center gap-2.5 px-2.5 py-1.5 rounded-[6px] text-[13px] transition-colors ${
                          active
                            ? "bg-primary/10 text-primary font-medium"
                            : "text-dim hover:text-foreground hover:bg-hover"
                        }`}
                      >
                        <item.icon className="w-4 h-4 shrink-0" />
                        {item.label}
                      </Link>
                    );
                  })}
                </div>
              </div>
            );
          })}
        </nav>

        {/* User menu */}
        {user && (
          <div ref={menuRef} className="relative shrink-0 border-t border-border p-3">
            <button
              onClick={() => setShowUserMenu(!showUserMenu)}
              className="w-full flex items-center gap-2 p-1 rounded-[6px] hover:bg-hover transition-colors cursor-pointer"
            >
              {user.avatar_url ? (
                <img
                  src={user.avatar_url}
                  alt={user.name}
                  className="w-6 h-6 rounded-full ring-1 ring-border shrink-0"
                />
              ) : (
                <div className="w-6 h-6 rounded-full bg-primary/10 flex items-center justify-center text-[10px] font-semibold text-primary shrink-0">
                  {user.name[0]}
                </div>
              )}
              <div className="text-left min-w-0">
                <div className="text-[12px] font-medium truncate">{user.name}</div>
                <div className="text-[10px] text-muted-foreground truncate">{user.email}</div>
              </div>
            </button>

            {showUserMenu && (
              <div className="absolute bottom-full left-3 right-3 mb-1 rounded-[8px] border border-border bg-card/80 backdrop-blur-xl shadow-xl shadow-black/30 py-1 animate-fade-in">
                <button
                  onClick={() => {
                    setShowUserMenu(false);
                    logout();
                  }}
                  className="w-full flex items-center gap-2 px-3 py-2 text-[13px] text-muted-foreground hover:text-foreground hover:bg-hover transition-colors cursor-pointer"
                >
                  <LogOut className="w-3.5 h-3.5" />
                  Sign out
                </button>
              </div>
            )}
          </div>
        )}
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto" role="main">
        <div className="max-w-6xl mx-auto min-h-full flex flex-col">{children}</div>
      </main>

      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  );
}
