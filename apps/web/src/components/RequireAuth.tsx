import { Center, Loader } from '@mantine/core';
import { Navigate, useLocation } from 'react-router-dom';
import type { ReactNode } from 'react';

import { useSession } from '@/hooks/useSession';
import type { Role } from '@/api/types';

/**
 * Route guard. This is convenience, not security: the API enforces every role
 * itself, and hiding a page only keeps the UI honest (SECURITY.md 5).
 */
export function RequireAuth({ children, roles }: { children: ReactNode; roles?: Role[] }) {
  const { user, isLoading, can } = useSession();
  const location = useLocation();

  if (isLoading) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    );
  }

  if (!user) {
    return <Navigate to="/login" state={{ from: location.pathname }} replace />;
  }

  // A password chosen by someone else must be replaced before anything else.
  if (user.must_change_password && location.pathname !== '/change-password') {
    return <Navigate to="/change-password" replace />;
  }

  if (roles && roles.length > 0 && !can(...roles)) {
    return <Navigate to="/" replace />;
  }

  return <>{children}</>;
}
