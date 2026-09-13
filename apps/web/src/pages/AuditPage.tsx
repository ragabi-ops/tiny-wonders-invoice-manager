import { Badge, Card, Group, Stack, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';

import { api } from '@/api/client';
import type { AuditEvent } from '@/api/types';
import { formatDateTime } from '@/lib/format';
import { auditLabel } from '@/lib/labels';

/**
 * The activity log is read-only everywhere: events are written by the actions
 * themselves and the database refuses updates and deletes (plan.md 16).
 */
export function AuditPage() {
  const eventsQuery = useQuery({
    queryKey: ['audit'],
    queryFn: () => api.get<{ events: AuditEvent[] }>('/audit/?limit=100'),
  });

  const failedLogin = (operation: string) =>
    operation === 'AUTH_LOGIN_FAILED' || operation === 'AUTH_LOGIN_BLOCKED';

  return (
    <Stack gap="lg">
      <div>
        <Title order={2}>יומן פעילות</Title>
        <Text c="dimmed">רישום בלתי ניתן לשינוי של פעולות אבטחה והגדרות במערכת.</Text>
      </div>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={720}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>מועד</Table.Th>
                <Table.Th>פעולה</Table.Th>
                <Table.Th>מבצע</Table.Th>
                <Table.Th>ישות</Table.Th>
                <Table.Th>הערה</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(eventsQuery.data?.events ?? []).map((event) => (
                <Table.Tr key={event.id}>
                  <Table.Td className="numeric">{formatDateTime(event.occurred_at)}</Table.Td>
                  <Table.Td>
                    <Group gap="xs">
                      <Text>{auditLabel(event.operation)}</Text>
                      {failedLogin(event.operation) && (
                        <Badge size="xs" color="red" variant="light">
                          אבטחה
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td className="wrap-anywhere" dir="ltr">
                    {event.actor_email ?? '—'}
                  </Table.Td>
                  <Table.Td className="wrap-anywhere">{event.entity_type ?? '—'}</Table.Td>
                  <Table.Td className="wrap-anywhere">{event.reason ?? '—'}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>
    </Stack>
  );
}
