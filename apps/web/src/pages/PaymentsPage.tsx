import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  Group,
  Loader,
  Pagination,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconCashBanknote, IconFileCheck, IconReceipt } from '@tabler/icons-react';

import { API_BASE, ApiError, api } from '@/api/client';
import type { MethodTotal, Payment, PaymentPage } from '@/api/types';
import { PaymentFormModal } from '@/components/PaymentFormModal';
import { useSession } from '@/hooks/useSession';
import { formatDate, formatMoney } from '@/lib/format';
import { PAYMENT_METHOD_LABELS } from '@/lib/labels';

const PAGE_SIZE = 25;

export function PaymentsPage() {
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [method, setMethod] = useState<string | null>(null);
  const [onlyUnreceipted, setOnlyUnreceipted] = useState(false);
  const [page, setPage] = useState(1);
  const [formOpen, setFormOpen] = useState(false);

  const offset = (page - 1) * PAGE_SIZE;

  const payments = useQuery({
    queryKey: ['payments', method, onlyUnreceipted, offset],
    queryFn: () =>
      api.get<PaymentPage>(
        `/payments/?method=${encodeURIComponent(method ?? '')}` +
          `&unreceipted=${onlyUnreceipted}&limit=${PAGE_SIZE}&offset=${offset}`,
      ),
  });

  const byMethod = useQuery({
    queryKey: ['payments', 'by-method'],
    queryFn: () => api.get<{ totals: MethodTotal[] }>('/payments/by-method'),
  });

  const receiptMutation = useMutation({
    mutationFn: (paymentId: string) => api.post<Payment>(`/payments/${paymentId}/receipt`),
    onSuccess: (payment) => {
      queryClient.invalidateQueries({ queryKey: ['payments'] });
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      notifications.show({ color: 'teal', message: `הופקה קבלה ${payment.receipt_number}` });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        autoClose: 8000,
        message: error instanceof ApiError ? error.message : 'הפקת הקבלה נכשלה.',
      }),
  });

  const totalPages = Math.max(1, Math.ceil((payments.data?.total ?? 0) / PAGE_SIZE));

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <Title order={2}>תשלומים</Title>
        {canEdit && (
          <Button leftSection={<IconCashBanknote size={18} />} onClick={() => setFormOpen(true)}>
            רישום תשלום
          </Button>
        )}
      </Group>

      {(byMethod.data?.totals.length ?? 0) > 0 && (
        <Card withBorder padding="md">
          <Text fw={600} mb="xs">
            סיכום לפי אמצעי תשלום
          </Text>
          <Group gap="lg" wrap="wrap">
            {(byMethod.data?.totals ?? []).map((total) => (
              <div key={total.method}>
                <Text size="sm" c="dimmed">
                  {total.method_hebrew} · {total.count}
                </Text>
                <Text fw={600} className="numeric">
                  {formatMoney(total.total_agorot)}
                </Text>
              </div>
            ))}
          </Group>
        </Card>
      )}

      <Card withBorder padding="md">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <Select
            placeholder="כל האמצעים"
            data={Object.entries(PAYMENT_METHOD_LABELS).map(([value, label]) => ({ value, label }))}
            value={method}
            onChange={(value) => {
              setMethod(value);
              setPage(1);
            }}
            clearable
            w={200}
          />
          <Switch
            label="רק ללא קבלה"
            checked={onlyUnreceipted}
            onChange={(event) => {
              setOnlyUnreceipted(event.currentTarget.checked);
              setPage(1);
            }}
          />
          <Group gap="xs">
            {payments.isFetching && <Loader size="xs" />}
            <Text size="sm" c="dimmed">
              סה״כ בסינון:
            </Text>
            <Text fw={600} className="numeric">
              {formatMoney(payments.data?.total_amount_agorot ?? 0)}
            </Text>
          </Group>
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={820}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>תאריך</Table.Th>
                <Table.Th>לקוח</Table.Th>
                <Table.Th>סכום</Table.Th>
                <Table.Th>אמצעי</Table.Th>
                <Table.Th>שיוך</Table.Th>
                <Table.Th>קבלה</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(payments.data?.payments ?? []).map((payment) => (
                <Table.Tr key={payment.id}>
                  <Table.Td className="numeric">
                    {formatDate(`${payment.received_at}T00:00:00Z`)}
                  </Table.Td>
                  <Table.Td>
                    <Link to={`/customers/${payment.customer_id}`}>{payment.customer_name}</Link>
                  </Table.Td>
                  <Table.Td className="numeric">{formatMoney(payment.amount_agorot)}</Table.Td>
                  <Table.Td>
                    <Text size="sm">{payment.method_hebrew}</Text>
                    {payment.reference && (
                      <Text size="xs" c="dimmed" className="wrap-anywhere">
                        {payment.reference}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {payment.allocations.length > 0 ? (
                      <Stack gap={2}>
                        {payment.allocations.map((allocation) => (
                          <Text key={allocation.document_id} size="xs" className="numeric">
                            {allocation.full_number} · {formatMoney(allocation.amount_agorot)}
                          </Text>
                        ))}
                        {payment.on_account_agorot > 0 && (
                          <Text size="xs" c="dimmed">
                            על החשבון {formatMoney(payment.on_account_agorot)}
                          </Text>
                        )}
                      </Stack>
                    ) : (
                      <Text size="xs" c="dimmed">
                        על החשבון
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {payment.receipt_document_id ? (
                      <Group gap={4} wrap="nowrap">
                        <Badge variant="light" color="teal" className="numeric">
                          {payment.receipt_number}
                        </Badge>
                        <Tooltip label="הורדת קבלה">
                          <Button
                            component="a"
                            href={`${API_BASE}/documents/${payment.receipt_document_id}/pdf`}
                            size="compact-xs"
                            variant="subtle"
                            aria-label="הורדת קבלה"
                          >
                            <IconReceipt size={16} />
                          </Button>
                        </Tooltip>
                      </Group>
                    ) : canEdit ? (
                      <Button
                        size="compact-xs"
                        variant="light"
                        color="orange"
                        leftSection={<IconFileCheck size={14} />}
                        loading={receiptMutation.isPending}
                        onClick={() => receiptMutation.mutate(payment.id)}
                      >
                        הפקת קבלה
                      </Button>
                    ) : (
                      <Badge variant="light" color="orange">
                        ללא קבלה
                      </Badge>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        {!payments.isLoading && (payments.data?.payments.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            עדיין לא נרשמו תשלומים.
          </Text>
        )}
      </Card>

      {totalPages > 1 && (
        <Group justify="center">
          <Pagination total={totalPages} value={page} onChange={setPage} />
        </Group>
      )}

      <PaymentFormModal opened={formOpen} onClose={() => setFormOpen(false)} />
    </Stack>
  );
}
