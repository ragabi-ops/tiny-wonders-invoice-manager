import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Alert,
  Badge,
  Button,
  Card,
  Grid,
  Group,
  Progress,
  SegmentedControl,
  Skeleton,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconDownload, IconFileExport } from '@tabler/icons-react';

import { API_BASE, ApiError, api } from '@/api/client';
import type { CategoryTotal, ExportStatus, MethodTotal, RevenueRow, Turnover, UnpaidRow } from '@/api/types';
import { formatDate, formatMoney, percentOf } from '@/lib/format';
import { DOCUMENT_TYPE_LABELS } from '@/lib/labels';

const BREAKDOWNS = [
  { value: 'period', label: 'לפי תקופה' },
  { value: 'customer', label: 'לפי לקוח' },
  { value: 'service', label: 'לפי שירות' },
  { value: 'activity', label: 'לפי פעילות' },
];

export function ReportsPage() {
  const queryClient = useQueryClient();

  const [by, setBy] = useState('period');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [exportJob, setExportJob] = useState<string | null>(null);

  const range = `from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`;

  const revenue = useQuery({
    queryKey: ['reports', 'revenue', by, from, to],
    queryFn: () => api.get<{ rows: RevenueRow[] }>(`/reports/revenue?by=${by}&${range}`),
  });

  const unpaid = useQuery({
    queryKey: ['reports', 'unpaid'],
    queryFn: () => api.get<{ documents: UnpaidRow[] }>('/reports/unpaid'),
  });

  const turnover = useQuery({
    queryKey: ['reports', 'turnover'],
    queryFn: () => api.get<Turnover>('/reports/turnover'),
  });

  const byMethod = useQuery({
    queryKey: ['payments', 'by-method', from, to],
    queryFn: () => api.get<{ totals: MethodTotal[] }>(`/payments/by-method?${range}`),
  });

  const byCategory = useQuery({
    queryKey: ['expenses', 'by-category', from, to],
    queryFn: () => api.get<{ totals: CategoryTotal[] }>(`/expenses/by-category?${range}`),
  });

  // Building can take a while, so the server queues it and the UI polls.
  const exportStatus = useQuery({
    queryKey: ['export', exportJob],
    queryFn: () => api.get<ExportStatus>(`/exports/${exportJob}`),
    enabled: Boolean(exportJob),
    refetchInterval: (query) => {
      const state = query.state.data?.state;
      return state === 'SUCCEEDED' || state === 'FAILED' ? false : 1500;
    },
  });

  const requestExport = useMutation({
    mutationFn: () => api.post<{ job_id: string }>('/exports/', { from: from || undefined, to: to || undefined }),
    onSuccess: (queued) => {
      setExportJob(queued.job_id);
      queryClient.invalidateQueries({ queryKey: ['export'] });
      notifications.show({ color: 'teal', message: 'הייצוא נכנס לתור. ההכנה עשויה לקחת מספר שניות.' });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'בקשת הייצוא נכשלה.',
      }),
  });

  const revenueTotal = (revenue.data?.rows ?? []).reduce((sum, row) => sum + row.amount_agorot, 0);

  return (
    <Stack gap="lg">
      <Title order={2}>דוחות</Title>

      <Card withBorder padding="md">
        <Group gap="sm" wrap="wrap">
          <TextInput
            label="מתאריך"
            type="date"
            dir="ltr"
            value={from}
            onChange={(event) => setFrom(event.currentTarget.value)}
          />
          <TextInput
            label="עד תאריך"
            type="date"
            dir="ltr"
            value={to}
            onChange={(event) => setTo(event.currentTarget.value)}
          />
          <Text size="xs" c="dimmed" mt="lg">
            טווח ריק מציג את כל הנתונים.
          </Text>
        </Group>
      </Card>

      <Card withBorder padding="lg">
        <Group justify="space-between" mb="sm" wrap="wrap">
          <Title order={4}>הכנסות</Title>
          <SegmentedControl data={BREAKDOWNS} value={by} onChange={setBy} size="xs" />
        </Group>

        {revenue.isLoading ? (
          <Skeleton height={160} />
        ) : (
          <>
            <Table.ScrollContainer minWidth={420}>
              <Table striped>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{by === 'period' ? 'תקופה' : 'פריט'}</Table.Th>
                    <Table.Th>מספר מסמכים</Table.Th>
                    <Table.Th>סכום</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(revenue.data?.rows ?? []).map((row) => (
                    <Table.Tr key={row.key}>
                      <Table.Td className={by === 'period' ? 'numeric' : undefined}>{row.label}</Table.Td>
                      <Table.Td className="numeric">{row.count}</Table.Td>
                      <Table.Td className="numeric">{formatMoney(row.amount_agorot)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>

            {(revenue.data?.rows.length ?? 0) === 0 ? (
              <Text c="dimmed" ta="center" py="md">
                אין הכנסות בטווח שנבחר.
              </Text>
            ) : (
              <Group mt="sm">
                <Text c="dimmed">סה״כ</Text>
                <Text fw={700} className="numeric">
                  {formatMoney(revenueTotal)}
                </Text>
              </Group>
            )}
          </>
        )}
      </Card>

      <Grid>
        <Grid.Col span={{ base: 12, md: 6 }}>
          <Card withBorder padding="lg" h="100%">
            <Title order={4} mb="sm">
              תשלומים לפי אמצעי
            </Title>
            {(byMethod.data?.totals ?? []).map((total) => (
              <Group key={total.method} justify="space-between">
                <Text c="dimmed">
                  {total.method_hebrew} · {total.count}
                </Text>
                <Text className="numeric">{formatMoney(total.total_agorot)}</Text>
              </Group>
            ))}
            {(byMethod.data?.totals.length ?? 0) === 0 && <Text c="dimmed">אין תשלומים בטווח.</Text>}
          </Card>
        </Grid.Col>

        <Grid.Col span={{ base: 12, md: 6 }}>
          <Card withBorder padding="lg" h="100%">
            <Title order={4} mb="sm">
              הוצאות לפי קטגוריה
            </Title>
            {(byCategory.data?.totals ?? []).map((total) => (
              <Group key={total.category} justify="space-between">
                <Text c="dimmed">
                  {total.category} · {total.count}
                </Text>
                <Text className="numeric">{formatMoney(total.total_agorot)}</Text>
              </Group>
            ))}
            {(byCategory.data?.totals.length ?? 0) === 0 && <Text c="dimmed">אין הוצאות בטווח.</Text>}
          </Card>
        </Grid.Col>
      </Grid>

      <Card withBorder padding="lg">
        <Title order={4} mb="sm">
          תקרת מחזור שנתית
        </Title>
        {turnover.isLoading ? (
          <Skeleton height={80} />
        ) : turnover.data?.threshold_known ? (
          <Stack gap="xs">
            <Group justify="space-between">
              <Text c="dimmed">מחזור {turnover.data.year}</Text>
              <Text fw={600} className="numeric">
                {formatMoney(turnover.data.revenue_agorot)} מתוך {formatMoney(turnover.data.threshold_agorot)}
              </Text>
            </Group>
            <Progress
              value={percentOf(turnover.data.revenue_agorot, turnover.data.threshold_agorot)}
              color={turnover.data.percent_used > 85 ? 'red' : 'teal'}
              aria-label="התקדמות מחזור שנתי"
            />
            <Text size="sm" c="dimmed">
              נותרו {formatMoney(turnover.data.remaining_agorot)} עד התקרה.
            </Text>
          </Stack>
        ) : (
          <Alert color="orange" variant="light">
            לא מוגדרת תקרת מחזור לשנה זו. יש להוסיף אותה בהגדרות הרגולטוריות — המערכת לא תנחש ערך.
          </Alert>
        )}
      </Card>

      <Card withBorder padding={0}>
        <Title order={4} p="md" pb="xs">
          מסמכים שלא שולמו
        </Title>
        <Table.ScrollContainer minWidth={720}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>מסמך</Table.Th>
                <Table.Th>לקוח</Table.Th>
                <Table.Th>תאריך</Table.Th>
                <Table.Th>לתשלום עד</Table.Th>
                <Table.Th>יתרה</Table.Th>
                <Table.Th>איחור</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(unpaid.data?.documents ?? []).map((row) => (
                <Table.Tr key={row.document_id}>
                  <Table.Td>
                    <Link to={`/documents/${row.document_id}`} className="numeric">
                      {row.full_number}
                    </Link>
                    <Text size="xs" c="dimmed">
                      {DOCUMENT_TYPE_LABELS[row.document_type]}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Link to={`/customers/${row.customer_id}`}>{row.customer_name}</Link>
                  </Table.Td>
                  <Table.Td className="numeric">{formatDate(`${row.document_date}T00:00:00Z`)}</Table.Td>
                  <Table.Td className="numeric">
                    {row.due_date ? formatDate(`${row.due_date}T00:00:00Z`) : '—'}
                  </Table.Td>
                  <Table.Td className="numeric">{formatMoney(row.outstanding_agorot)}</Table.Td>
                  <Table.Td>
                    {row.days_overdue > 0 ? (
                      <Badge color="red" variant="light">
                        {row.days_overdue} ימים
                      </Badge>
                    ) : (
                      '—'
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        {!unpaid.isLoading && (unpaid.data?.documents.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            אין מסמכים פתוחים. הכול שולם.
          </Text>
        )}
      </Card>

      <Card withBorder padding="lg">
        <Group justify="space-between" wrap="wrap" mb="sm">
          <div>
            <Title order={4}>ייצוא לרואה חשבון</Title>
            <Text size="sm" c="dimmed">
              קובץ ZIP הכולל קבצי CSV, קובצי ה־PDF של המסמכים והקבצים המצורפים להוצאות.
            </Text>
          </div>
          <Button
            leftSection={<IconFileExport size={18} />}
            loading={requestExport.isPending}
            onClick={() => requestExport.mutate()}
          >
            הכנת ייצוא
          </Button>
        </Group>

        {exportJob && exportStatus.data && (
          <Group gap="sm">
            {exportStatus.data.state === 'SUCCEEDED' ? (
              <>
                <Badge color="teal" variant="light">
                  מוכן
                </Badge>
                <Button
                  component="a"
                  href={`${API_BASE}/exports/${exportJob}/download`}
                  variant="light"
                  leftSection={<IconDownload size={16} />}
                >
                  הורדת {exportStatus.data.filename}
                </Button>
              </>
            ) : exportStatus.data.state === 'FAILED' ? (
              <Alert color="red" variant="light" w="100%">
                הכנת הייצוא נכשלה: {exportStatus.data.error}
              </Alert>
            ) : (
              <Group gap="xs">
                <Badge variant="light">בהכנה…</Badge>
                <Text size="sm" c="dimmed">
                  אפשר להמשיך לעבוד; ההכנה רצה ברקע.
                </Text>
              </Group>
            )}
          </Group>
        )}
      </Card>
    </Stack>
  );
}
