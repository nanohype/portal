import { create } from 'zustand';

export type Theme = 'light' | 'dark';

const KEY = 'portal_theme';

// Saved preference wins; otherwise follow the OS. Mirrors the inline script in
// index.html that sets the class pre-paint to avoid a flash on load.
function readInitial(): Theme {
  const saved = localStorage.getItem(KEY);
  if (saved === 'light' || saved === 'dark') return saved;
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

function apply(theme: Theme) {
  const root = document.documentElement;
  root.classList.toggle('light', theme === 'light');
  root.classList.toggle('dark', theme === 'dark');
}

interface ThemeState {
  theme: Theme;
  toggle: () => void;
  setTheme: (t: Theme) => void;
}

export const useTheme = create<ThemeState>((set, get) => {
  const initial = readInitial();
  apply(initial); // keep the DOM in step with the store on first use

  const commit = (theme: Theme) => {
    apply(theme);
    localStorage.setItem(KEY, theme);
    set({ theme });
  };

  return {
    theme: initial,
    toggle: () => commit(get().theme === 'dark' ? 'light' : 'dark'),
    setTheme: commit,
  };
});
