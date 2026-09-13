import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  Grid,
  Group,
  Skeleton,
  Stack,
  Switch,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconArrowRight, IconCashBanknote, IconEdit } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { Customer, CustomerBalance, TimelineEntry } from '@/api/types';
import { CustomerFormModal } from '@/components/CustomerFormModal';
import { PaymentFormModal } from '@/components/PaymentFormModal';
import { useSession } from '@/hooks/useSession';
import { formatDate, formatDateTime, formatMoney } from '@/lib/format';
import {
  CUSTOMER_TYPE_LABELS,
  DELIVERY_LABELS,
  DOCUMENT_STATE_COLORS,
  DOCUMENT_STATE_LABELS,
  DOCUMENT_TYPE_LABELS,
  PAYMENT_METHOD_LABELS,
} from '@/lib/labels';

function Field({ label, value }: { label: string; value: string }) {
  return (
    <Grid.Col span={{ base: 12, sm: 6 }}>
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text className="wrap-anywhere">{value || '—'}</Text>
    </Grid.Col>
  );
}

export function CustomerDetailPage() {
  const { id = '' } = useParams();
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');
  const [formOpen, setFormOpen] = useState(false);
  const [paymentOpen, setPaymentOpen] = useState(false);

  const customerQuery = useQuery({
    queryKey: ['customer', id],
    queryFn: () => api.get<Customer>(`/customers/${id}`),
    enabled: Boolean(id),
  });

  const balanceQuery = useQuery({
    queryKey: ['balance', id],
    queryFn: () => api.get<CustomerBalance>(`/customers/${id}/balance`),
    enabled: Boolean(id),
  });

  const timelineQuery = useQuery({
    queryKey: ['timeline', id],
    queryFn: () => api.get<{ entries: TimelineEntry[] }>(`/customers/${id}/timeline`),
    enabled: Boolean(id),
  });

  const activeMutation = useMutation({
    mutationFn: (active: boolean) => api.put<Customer>(`/customers/${id}/active`, { active, reason: '' }),
    onSuccess: (saved) => {
      queryClient.setQueryData(['customer', id], saved);
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      notifications.show({
        color: 'teal',
        message: saved.active ? 'הלקוח שוחזר מהארכיון.' : 'הלקוח הועבר לארכיון.',
      });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'עדכון הלקוח נכשל.',
      }),
  });

  if (customerQuery.isLoading) return <Skeleton height={320} />;

  if (customerQuery.isError || !customerQuery.data) {
    return (
      <Stack gap="md">
        <Title order={2}>הלקוח לא נמצא</Title>
        <Button component={Link} to="/customers" leftSection={<IconArrowRight size={18} />} w="fit-content">
          חזרה לרשימת הלקוחות
        </Button>
      </Stack>
    );
  }

  const customer = customerQuery.data;

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm">
          <Button
            component={Link}
            to="/customers"
            variant="subtle"
            leftSection={<IconArrowRight size={18} />}
          >
            לקוחות
          </Button>
          <Title order={2}>{customer.display_name}</Title>
          {!customer.active && (
            <Badge color="gray" variant="light">
              ארכיון
            </Badge>
          )}
        </Group>

        {canEdit && (
          <Group gap="sm">
            <Button
              variant="default"
              leftSection={<IconCashBanknote size={18} />}
              onClick={() => setPaymentOpen(true)}
            >
              רישום תשלום
            </Button>
            <Button leftSection={<IconEdit size={18} />} onClick={() => setFormOpen(true)}>
              עריכה
            </Button>
          </Group>
        )}
      </Group>

      <Card withBorder padding="lg">
        <Grid>
          <Field label="סוג לקוח" value={CUSTOMER_TYPE_LABELS[customer.customer_type]} />
          <Field label="שם רשמי" value={customer.legal_name} />
          <Field label="מספר עוסק / ת״ז" value={customer.business_or_id_number} />
          <Field label="טלפון" value={customer.phone} />
          <Field label="דוא״ל" value={customer.email} />
          <Field label="אמצעי משלוח מועדף" value={DELIVERY_LABELS[customer.preferred_delivery]} />
          <Field label="כתובת" value={customer.address} />
          <Field label="עודכן" value={formatDateTime(customer.updated_at)} />
          {customer.notes && (
            <Grid.Col span={12}>
              <Text size="sm" c="dimmed">
                הערות
              </Text>
              <Text style={{ whiteSpace: 'pre-wrap' }}>{customer.notes}</Text>
            </Grid.Col>
          )}
        </Grid>

        {canEdit && (
          <Group mt="lg" justify="space-between">
            <Switch
              label="לקוח פעיל"
              checked={customer.active}
              disabled={activeMutation.isPending}
              onChange={(event) => activeMutation.mutate(event.currentTarget.checked)}
            />
            <Text size="xs" c="dimmed">
              לקוחות מועברים לארכיון ואינם נמחקים: מסמכים שהופקו מפנים אליהם.
            </Text>
          </Group>
        )}
      </Card>

      <Card withBorder padding="lg">
        <Title order={4} mb="sm">
          יתרה
        </Title>
        {balanceQuery.isLoading ? (
          <Skeleton height={70} />
        ) : balanceQuery.data ? (
          <Group gap="xl" wrap="wrap">
            <div>
              <Text size="sm" c="dimmed">
                חויב
              </Text>
              <Text fw={600} className="numeric">
                {formatMoney(balanceQuery.data.invoiced_agorot)}
              </Text>
            </div>
            <div>
              <Text size="sm" c="dimmed">
                שולם
              </Text>
              <Text fw={600} className="numeric">
                {formatMoney(balanceQuery.data.paid_agorot)}
              </Text>
            </div>
            <div>
              <Text size="sm" c="dimmed">
                יתרה פתוחה
              </Text>
              <Text
                fw={700}
                size="lg"
                className="numeric"
                c={balanceQuery.data.open_agorot > 0 ? 'red' : undefined}
              >
                {formatMoney(balanceQuery.data.open_agorot)}
              </Text>
            </div>
            {balanceQuery.data.on_account_agorot > 0 && (
              <div>
                <Text size="sm" c="dimmed">
                  על החשבון
                </Text>
                <Text fw={600} className="numeric" c="teal">
                  {formatMoney(balanceQuery.data.on_account_agorot)}
                </Text>
              </div>
            )}
          </Group>
        ) : (
          <Text c="dimmed">לא ניתן לחשב את היתרה.</Text>
        )}
        <Text size="xs" c="dimmed" mt="sm">
          היתרה הפתוחה היא ההפרש בין מסמכים שהופקו לבין תשלומים ששויכו אליהם. תשלום על החשבון אינו
          מקוזז אוטומטית — יש לשייך אותו למסמך.
        </Text>
      </Card>

      <Card withBorder padding={0}>
        <Group justify="space-between" p="md" pb="xs">
          <Title order={4}>היסטוריית מסמכים ותשלומים</Title>
        </Group>

        {timelineQuery.isLoading ? (
          <Skeleton height={160} m="md" />
        ) : (timelineQuery.data?.entries.length ?? 0) === 0 ? (
          <Text c="dimmed" ta="center" py="xl">
            עדיין אין מסמכים או תשלומים ללקוח זה.
          </Text>
        ) : (
          <Table.ScrollContainer minWidth={640}>
            <Table striped highlightOnHover>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>תאריך</Table.Th>
                  <Table.Th>סוג</Table.Th>
                  <Table.Th>מספר</Table.Th>
                  <Table.Th>סכום</Table.Th>
                  <Table.Th>מצב</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {(timelineQuery.data?.entries ?? []).map((entry) => (
                  <Table.Tr key={`${entry.kind}-${entry.id}`}>
                    <Table.Td className="numeric">{formatDate(`${entry.date}T00:00:00Z`)}</Table.Td>
                    <Table.Td>
                      {entry.kind === 'DOCUMENT' ? (
                        <Link to={`/documents/${entry.id}`}>
                          {DOCUMENT_TYPE_LABELS[entry.title as keyof typeof DOCUMENT_TYPE_LABELS] ??
                            entry.title}
                        </Link>
                      ) : (
                        <Text>
                          תשלום ·{' '}
                          {PAYMENT_METHOD_LABELS[entry.title as keyof typeof PAYMENT_METHOD_LABELS] ??
                            entry.title}
                        </Text>
                      )}
                      {entry.detail && (
                        <Text size="xs" c="dimmed" className="wrap-anywhere">
                          {entry.detail}
                        </Text>
                      )}
                    </Table.Td>
                    <Table.Td className="numeric">{entry.full_number || '—'}</Table.Td>
                    <Table.Td className="numeric">{formatMoney(entry.amount_agorot)}</Table.Td>
                    <Table.Td>
                      {entry.kind === 'DOCUMENT' ? (
                        <Badge
                          variant="light"
                          color={
                            DOCUMENT_STATE_COLORS[entry.state as keyof typeof DOCUMENT_STATE_COLORS] ??
                            'gray'
                          }
                        >
                          {DOCUMENT_STATE_LABELS[entry.state as keyof typeof DOCUMENT_STATE_LABELS] ??
                            entry.state}
                        </Badge>
                      ) : (
                        <Badge variant="light" color={entry.state === 'RECEIPTED' ? 'teal' : 'orange'}>
                          {entry.state === 'RECEIPTED' ? 'הופקה קבלה' : 'ללא קבלה'}
                        </Badge>
                      )}
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          </Table.ScrollContainer>
        )}
      </Card>

      <CustomerFormModal opened={formOpen} onClose={() => setFormOpen(false)} customer={customer} />
      <PaymentFormModal
        opened={paymentOpen}
        onClose={() => setPaymentOpen(false)}
        customerId={customer.id}
      />
    </Stack>
  );
}
