import { useQuery } from '@tanstack/react-query';
import { useAuthStore } from '@/stores/auth';
import { api } from '@/api/client';

export function useAuth() {
  const { token, user, isAuthenticated, setUser, logout } = useAuthStore();

  const { isLoading } = useQuery({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      const { data, error } = await api.GET('/auth/me');
      if (error) throw error;
      setUser(data);
      return data;
    },
    enabled: !!token && !user,
    retry: false,
  });

  return { user, isAuthenticated, isLoading, logout };
}
