import { create } from 'zustand';
import type { User } from '@/api/models';

interface AuthState {
  token: string | null;
  user: User | null;
  isAuthenticated: boolean;
  setAuth: (token: string, user: User) => void;
  setUser: (user: User) => void;
  logout: () => void;
}

export const useAuthStore = create<AuthState>((set) => ({
  token: localStorage.getItem('portal_token'),
  user: null,
  isAuthenticated: !!localStorage.getItem('portal_token'),

  setAuth: (token, user) => {
    localStorage.setItem('portal_token', token);
    set({ token, user, isAuthenticated: true });
  },

  setUser: (user) => {
    set({ user });
  },

  logout: () => {
    localStorage.removeItem('portal_token');
    set({ token: null, user: null, isAuthenticated: false });
  },
}));
