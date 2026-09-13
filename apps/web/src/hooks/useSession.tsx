import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { ApiError, api } from '@/api/client';
import type { MeResponse, Role, User } from '@/api/types';

interface SessionValue {
  user: User | null;
  isLoading: boolean;
  login: (email: string, password: string) => Promise<User>;
  logout: () => Promise<void>;
  /** True when the user holds the role, or is an OWNER, who may do anything. */
  can: (...roles: Role[]) => boolean;
}

const SessionContext = createContext<SessionValue | null>(null);

const ME_KEY = ['auth', 'me'] as const;

export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();

  const meQuery = useQuery({
    queryKey: ME_KEY,
    queryFn: async () => {
      try {
        return await api.get<MeResponse>('/auth/me');
      } catch (error) {
        // Not being logged in is an expected state, not a failure to report.
        if (error instanceof ApiError && error.status === 401) return null;
        throw error;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  const loginMutation = useMutation({
    mutationFn: (input: { email: string; password: string }) =>
      api.post<MeResponse>('/auth/login', input),
    onSuccess: (data) => queryClient.setQueryData(ME_KEY, data),
  });

  const logoutMutation = useMutation({
    mutationFn: () => api.post<void>('/auth/logout'),
    onSettled: () => {
      // Whatever the server said, this browser is done: drop every cached
      // answer so no data from the previous session lingers on screen.
      queryClient.setQueryData(ME_KEY, null);
      queryClient.clear();
    },
  });

  const user = meQuery.data?.user ?? null;

  const can = useCallback(
    (...roles: Role[]) => {
      if (!user) return false;
      if (user.roles.includes('OWNER')) return true;
      return roles.some((role) => user.roles.includes(role));
    },
    [user],
  );

  const value = useMemo<SessionValue>(
    () => ({
      user,
      isLoading: meQuery.isLoading,
      login: async (email, password) => {
        const result = await loginMutation.mutateAsync({ email, password });
        return result.user;
      },
      logout: async () => {
        await logoutMutation.mutateAsync();
      },
      can,
    }),
    [user, meQuery.isLoading, loginMutation, logoutMutation, can],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error('useSession must be used inside SessionProvider');
  return value;
}
