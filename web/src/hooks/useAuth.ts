import { useQuery } from '@tanstack/react-query';
import { useAuthStore } from '@/stores/auth';
import { api } from '@/api/client';

export function useAuth() {
  const { token, user, isAuthenticated, setUser, logout } = useAuthStore();

  // Refetched whenever a token is present, not only when the store is empty.
  // The server resolves the caller's role from the users table on every
  // request, so the UI has to keep asking too — a role cached from the session
  // that started an hour ago would put controls on screen that the API refuses,
  // or hide ones it would allow.
  const { isLoading } = useQuery({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      const { data, error } = await api.GET('/auth/me');
      if (error) throw error;
      setUser(data);
      return data;
    },
    enabled: !!token,
    retry: false,
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });

  return { user, isAuthenticated, isLoading, logout };
}
