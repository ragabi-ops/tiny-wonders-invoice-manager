import { useState } from 'react';
import { NavLink as RouterNavLink, Outlet, useLocation, useNavigate } from 'react-router-dom';
import {
  ActionIcon,
  AppShell,
  Badge,
  Burger,
  Button,
  Group,
  Menu,
  NavLink,
  ScrollArea,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { useQuery } from '@tanstack/react-query';
import {
  IconBuildingStore,
  IconChevronDown,
  IconCoin,
  IconFileInvoice,
  IconHome,
  IconLogout,
  IconReceipt2,
  IconReportMoney,
  IconSettings,
  IconUsers,
  IconUsersGroup,
  IconHistory,
  IconListDetails,
  IconCalendarEvent,
  IconSearch,
} from '@tabler/icons-react';

import { api } from '@/api/client';
import type { ComplianceStatus, Role } from '@/api/types';
import { GlobalSearch } from '@/components/GlobalSearch';
import { useSession } from '@/hooks/useSession';

interface NavItem {
  label: string;
  to: string;
  icon: typeof IconHome;
  /** Phase 3+ pages are listed but disabled, so the shell tells the truth. */
  available: boolean;
  /** Roles allowed to see the item. Absent means every authenticated role. */
  roles?: Role[];
}

// Navigation follows plan.md section 7. "פעילויות" is the operational
// dimension from section 8; the append-only security log is "יומן פעילות".
const NAV_ITEMS: NavItem[] = [
  { label: 'ראשי', to: '/', icon: IconHome, available: true },
  { label: 'מסמכים', to: '/documents', icon: IconFileInvoice, available: true },
  { label: 'לקוחות', to: '/customers', icon: IconUsersGroup, available: true },
  { label: 'שירותים', to: '/services', icon: IconListDetails, available: true },
  { label: 'פעילויות', to: '/activities', icon: IconCalendarEvent, available: true },
  { label: 'תשלומים', to: '/payments', icon: IconCoin, available: true },
  { label: 'הוצאות', to: '/expenses', icon: IconReceipt2, available: true },
  { label: 'דוחות', to: '/reports', icon: IconReportMoney, available: true },
  { label: 'יומן פעילות', to: '/audit', icon: IconHistory, available: true, roles: ['OWNER', 'ACCOUNTANT'] },
  { label: 'משתמשים', to: '/users', icon: IconUsers, available: true, roles: ['OWNER'] },
  { label: 'הגדרות', to: '/settings', icon: IconSettings, available: true },
];

export function AppLayout() {
  const [opened, { toggle, close }] = useDisclosure();
  const [searchOpen, { open: openSearch, close: closeSearch }] = useDisclosure(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const { user, logout, can } = useSession();
  const navigate = useNavigate();
  const location = useLocation();

  const compliance = useQuery({
    queryKey: ['compliance'],
    queryFn: () => api.get<ComplianceStatus>('/compliance/'),
    staleTime: 5 * 60_000,
  });

  const handleLogout = async () => {
    setLoggingOut(true);
    try {
      await logout();
      navigate('/login', { replace: true });
    } finally {
      setLoggingOut(false);
    }
  };

  return (
    <AppShell
      header={{ height: 60 }}
      navbar={{ width: 240, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="md"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" aria-label="תפריט" />
            <IconBuildingStore size={22} />
            <Title order={4} visibleFrom="xs">
              מערכת חשבוניות וקבלות
            </Title>
          </Group>

          <Group gap="xs" wrap="nowrap">
            <Tooltip label="חיפוש">
              <ActionIcon variant="subtle" size="lg" aria-label="חיפוש" onClick={openSearch}>
                <IconSearch size={20} />
              </ActionIcon>
            </Tooltip>

            {/* The operator must always be able to see that real issuance is
                still blocked by the compliance gate (plan.md 2). */}
            {compliance.data && !compliance.data.real_issuance_enabled && (
              <Tooltip
                multiline
                w={260}
                label="הפקת מסמכים רשמיים חסומה עד להשלמת בדיקת ההתאמה הרגולטורית מול רואה חשבון."
              >
                <Badge color="orange" variant="light" style={{ cursor: 'help' }}>
                  מצב בדיקה
                </Badge>
              </Tooltip>
            )}
            {compliance.data && (
              <Badge color="gray" variant="light" visibleFrom="sm">
                {compliance.data.mode_hebrew}
              </Badge>
            )}

            <Menu position="bottom-end" withinPortal>
              <Menu.Target>
                <Button variant="subtle" color="gray" rightSection={<IconChevronDown size={16} />}>
                  {user?.display_name ?? ''}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                <Menu.Label>{user?.email}</Menu.Label>
                <Menu.Item onClick={() => navigate('/change-password')}>שינוי סיסמה</Menu.Item>
                <Menu.Divider />
                <Menu.Item
                  color="red"
                  leftSection={<IconLogout size={16} />}
                  disabled={loggingOut}
                  onClick={handleLogout}
                >
                  התנתקות
                </Menu.Item>
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="sm">
        <ScrollArea>
          {NAV_ITEMS.filter((item) => !item.roles || can(...item.roles)).map((item) => {
            const Icon = item.icon;
            const active = item.to === '/' ? location.pathname === '/' : location.pathname.startsWith(item.to);

            if (!item.available) {
              return (
                <NavLink
                  key={item.to}
                  label={item.label}
                  description="בקרוב"
                  leftSection={<Icon size={18} />}
                  disabled
                />
              );
            }

            return (
              <NavLink
                key={item.to}
                component={RouterNavLink}
                to={item.to}
                label={item.label}
                leftSection={<Icon size={18} />}
                active={active}
                onClick={close}
              />
            );
          })}
        </ScrollArea>

        <Text size="xs" c="dimmed" mt="auto" pt="sm">
          שלב 1 — תשתית
        </Text>
      </AppShell.Navbar>

      <AppShell.Main>
        <Outlet />
      </AppShell.Main>

      <GlobalSearch opened={searchOpen} onClose={closeSearch} />
    </AppShell>
  );
}
