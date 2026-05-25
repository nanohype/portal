import { useState, useRef, useEffect } from "react";
import type { ReactNode } from "react";
import { useAuth } from "@/hooks/useAuth";
import { useLocation } from "@/hooks/useNavigate";
import { Link } from "@/components/ui/link";
import { Box, Settings, LogOut } from "lucide-react";

function NavLink({
  href,
  active,
  children,
}: {
  href: string;
  active: boolean;
  children: ReactNode;
}) {
  return (
    <Link
      href={href}
      className={`px-3 py-1.5 rounded-[6px] text-[13px] font-medium transition-all duration-150 ${
        active
          ? "text-foreground bg-hover"
          : "text-dim hover:text-foreground hover:bg-hover"
      }`}
    >
      {children}
    </Link>
  );
}

export function AppLayout({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const location = useLocation();
  const path = location.split("?")[0];
  const [showUserMenu, setShowUserMenu] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  const isAdmin = user?.role === "admin" || user?.role === "owner";

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
    <div className="h-screen flex flex-col">
      {/* Header */}
      <header className="h-11 shrink-0 border-b border-border bg-frosted backdrop-blur-xl sticky top-0 z-40">
        <div className="h-full max-w-6xl mx-auto px-5 flex items-center">
          {/* Logo */}
          <Link href="/" className="flex items-center gap-2 shrink-0 mr-8">
            <div className="w-6 h-6 rounded-[5px] bg-primary/10 flex items-center justify-center">
              <Box className="w-3 h-3 text-primary" />
            </div>
            <span className="font-mono text-[11px] uppercase tracking-[0.12em] text-dim">tofui</span>
          </Link>

          {/* Nav */}
          <nav className="flex items-center gap-1" aria-label="Primary">
            <NavLink href="/" active={path === "/" || path.startsWith("/workspaces")}>
              Workspaces
            </NavLink>
            <NavLink href="/pipelines" active={path.startsWith("/pipelines")}>
              Pipelines
            </NavLink>
            <NavLink href="/teams" active={path === "/teams"}>
              Teams
            </NavLink>
            {isAdmin && (
              <NavLink href="/users" active={path === "/users"}>
                Users
              </NavLink>
            )}
            {isAdmin && (
              <NavLink href="/audit-logs" active={path === "/audit-logs"}>
                Audit Logs
              </NavLink>
            )}
          </nav>

          {/* Right section */}
          <div className="ml-auto flex items-center gap-2">
            {isAdmin && (
              <Link
                href="/settings"
                className={`p-1.5 rounded-[6px] transition-colors duration-150 ${
                  path === "/settings"
                    ? "text-foreground bg-hover"
                    : "text-dim hover:text-foreground hover:bg-hover"
                }`}
              >
                <Settings className="w-3.5 h-3.5" />
              </Link>
            )}

            {/* User menu */}
            {user && (
              <div ref={menuRef} className="relative">
                <button
                  onClick={() => setShowUserMenu(!showUserMenu)}
                  className="flex items-center gap-2 p-1 rounded-[6px] hover:bg-hover transition-colors cursor-pointer"
                >
                  {user.avatar_url ? (
                    <img
                      src={user.avatar_url}
                      alt={user.name}
                      className="w-6 h-6 rounded-full ring-1 ring-border"
                    />
                  ) : (
                    <div className="w-6 h-6 rounded-full bg-primary/10 flex items-center justify-center text-[10px] font-semibold text-primary">
                      {user.name[0]}
                    </div>
                  )}
                </button>

                {showUserMenu && (
                  <div className="absolute right-0 top-full mt-1 w-56 rounded-[8px] border border-border bg-card/80 backdrop-blur-xl shadow-xl shadow-black/30 py-1 animate-fade-in">
                    <div className="px-3 py-2 border-b border-border">
                      <div className="text-[13px] font-medium">{user.name}</div>
                      <div className="text-[11px] text-muted-foreground">{user.email}</div>
                    </div>
                    <button
                      onClick={() => { setShowUserMenu(false); logout(); }}
                      className="w-full flex items-center gap-2 px-3 py-2 text-[13px] text-muted-foreground hover:text-foreground hover:bg-hover transition-colors cursor-pointer"
                    >
                      <LogOut className="w-3.5 h-3.5" />
                      Sign out
                    </button>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </header>

      {/* Main content */}
      <main className="flex-1 overflow-auto" role="main">
        <div className="max-w-6xl mx-auto">{children}</div>
      </main>
    </div>
  );
}
