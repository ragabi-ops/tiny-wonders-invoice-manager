import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Group,
  Skeleton,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconAlertTriangle, IconDatabaseExport } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { BackupRun, BackupStatus } from '@/api/types';
import { formatDateTime } from '@/lib/format';

/** Renders a byte count the way a person reads one. */
function formatBytes(bytes: number | null | undefined): string {
  if (!bytes) return '—';
  const units = ['B', 'KB', 'MB', 'GB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

/**
 * Backup status, OWNER-only. A backup nobody checks is not a backup, so this is
 * deliberately blunt about staleness rather than reassuring (plan.md 18).
 */
export function BackupsCard() {
  const queryClient = useQueryClient();

  const status = useQuery({
    queryKey: ['backups', 'status'],
    queryFn: () => api.get<BackupStatus>('/backups/status'),
  });

  const runs = useQuery({
    queryKey: ['backups', 'runs'],
    queryFn: () => api.get<{ runs: BackupRun[] }>('/backups/runs'),
  });

  const runNow = useMutation({
    mutationFn: () => api.post<BackupRun>('/backups/run'),
    onSuccess: (run) => {
      queryClient.invalidateQueries({ queryKey: ['backups'] });
      notifications.show({
        color: 'teal',
        message: `הגיבוי הושלם: ${formatBytes(run.archive_bytes)}, ${run.file_count ?? 0} קבצים.`,
      });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        autoClose: 10000,
        message: error instanceof ApiError ? error.message : 'הגיבוי נכשל.',
      }),
  });

  const data = status.data;

  return (
    <Card withBorder padding="lg">
      <Group justify="space-between" wrap="wrap" mb="sm">
        <div>
          <Title order={4}>גיבויים</Title>
          <Text size="sm" c="dimmed">
            גיבוי יומי של מסד הנתונים, קובצי המסמכים והקבצים המצורפים.
          </Text>
        </div>
        <Button
          leftSection={<IconDatabaseExport size={18} />}
          loading={runNow.isPending}
          disabled={!data?.configured}
          onClick={() => runNow.mutate()}
        >
          גיבוי עכשיו
        </Button>
      </Group>

      {status.isLoading ? (
        <Skeleton height={120} />
      ) : !data?.configured ? (
        <Alert color="red" variant="light" icon={<IconAlertTriangle size={18} />} title="גיבויים אינם מוגדרים">
          לא הוגדרה תיקיית גיבוי. יש להגדיר <Code>BACKUP_DIR</Code> ולהפעיל מחדש. עד אז לא מתבצע
          גיבוי כלל.
        </Alert>
      ) : (
        <Stack gap="md">
          {data.stale && (
            <Alert color="red" variant="light" icon={<IconAlertTriangle size={18} />} title="הגיבוי אינו עדכני">
              {data.last_succeeded_at
                ? 'לא הושלם גיבוי מוצלח ביממה וחצי האחרונות. יש לבדוק מה מונע את הגיבוי היומי.'
                : 'טרם הושלם גיבוי מוצלח כלל.'}
            </Alert>
          )}

          {data.tools?.version_mismatch && (
            <Alert color="orange" variant="light" title="אי-התאמת גרסאות">
              {data.tools.version_warning}
            </Alert>
          )}

          {data.last_run_state === 'FAILED' && data.last_run_error && (
            <Alert color="red" variant="light" title="הגיבוי האחרון נכשל">
              <Code block style={{ whiteSpace: 'pre-wrap' }}>
                {data.last_run_error}
              </Code>
            </Alert>
          )}

          <Group gap="xl" wrap="wrap">
            <div>
              <Text size="sm" c="dimmed">
                גיבוי מוצלח אחרון
              </Text>
              <Text fw={600} c={data.stale ? 'red' : undefined}>
                {data.last_succeeded_at ? formatDateTime(data.last_succeeded_at) : 'טרם בוצע'}
              </Text>
            </div>
            <div>
              <Text size="sm" c="dimmed">
                גודל
              </Text>
              <Text fw={600} className="numeric">
                {formatBytes(data.last_archive_bytes)}
              </Text>
            </div>
            <div>
              <Text size="sm" c="dimmed">
                מועד יומי
              </Text>
              <Text fw={600}>{data.schedule_local}</Text>
            </div>
            <div>
              <Text size="sm" c="dimmed">
                שמירה בתיקייה
              </Text>
              <Text fw={600}>{data.retention_days} ימים</Text>
            </div>
          </Group>

          <div>
            <Text size="sm" c="dimmed" mb={4}>
              תיקיית הגיבוי
            </Text>
            <Code dir="ltr" style={{ overflowWrap: 'anywhere' }}>
              {data.directory}
            </Code>
            <Text size="xs" c="dimmed" mt={6}>
              הקובץ אינו מוצפן ומכיל פרטי לקוחות ורשומות כספיות. יש להגן על התיקייה שאליה הוא מסונכרן.
              מחיקת גיבויים ישנים היא ניקוי דיסק בלבד — הרשומות עצמן נשמרות במסד הנתונים ואינן נמחקות.
            </Text>
          </div>

          {(runs.data?.runs.length ?? 0) > 0 && (
            <Table.ScrollContainer minWidth={480}>
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>מועד</Table.Th>
                    <Table.Th>מצב</Table.Th>
                    <Table.Th>סוג</Table.Th>
                    <Table.Th>גודל</Table.Th>
                    <Table.Th>קבצים</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(runs.data?.runs ?? []).slice(0, 8).map((run) => (
                    <Table.Tr key={run.id}>
                      <Table.Td className="numeric">{formatDateTime(run.started_at)}</Table.Td>
                      <Table.Td>
                        <Badge
                          variant="light"
                          color={
                            run.state === 'SUCCEEDED' ? 'teal' : run.state === 'FAILED' ? 'red' : 'gray'
                          }
                        >
                          {run.state === 'SUCCEEDED' ? 'הושלם' : run.state === 'FAILED' ? 'נכשל' : 'רץ'}
                        </Badge>
                      </Table.Td>
                      <Table.Td>{run.trigger === 'MANUAL' ? 'ידני' : 'מתוזמן'}</Table.Td>
                      <Table.Td className="numeric">{formatBytes(run.archive_bytes)}</Table.Td>
                      <Table.Td className="numeric">{run.file_count ?? '—'}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </Stack>
      )}
    </Card>
  );
}
