import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Grid,
  Group,
  Modal,
  NumberInput,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconEye, IconInfoCircle } from '@tabler/icons-react';

import { API_BASE, ApiError, api } from '@/api/client';
import type {
  ActivityPage,
  CustomerPage,
  OutstandingDocument,
  Payment,
  PaymentInput,
  PaymentMethod,
} from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { formatDate, formatMoney } from '@/lib/format';
import { DOCUMENT_TYPE_LABELS, PAYMENT_METHOD_LABELS } from '@/lib/labels';
import { agorotToShekels, shekelsToAgorot } from '@/lib/money';

function todayInJerusalem(): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Jerusalem' }).format(new Date());
}

interface Props {
  opened: boolean;
  onClose: () => void;
  /** Pre-selects a customer, when opened from their page. */
  customerId?: string;
  /**
   * Settles one issued document, when opened from its page: the amount and the
   * allocation start at what it still owes, and the activity is carried over.
   * How the money arrived is not something the document knows, so method,
   * date and reference are still the operator's to fill in.
   */
  settleDocument?: { id: string; activityId: string | null };
}

/**
 * Record a payment and, by default, issue its receipt. The two happen in one
 * server transaction (plan.md 12); the checkbox exists for the case where the
 * PDF renderer is down and the money still has to be recorded.
 */
export function PaymentFormModal({ opened, onClose, customerId, settleDocument }: Props) {
  const queryClient = useQueryClient();

  const [customer, setCustomer] = useState<string | null>(customerId ?? null);
  const [customerSearch, setCustomerSearch] = useState('');
  const [amountShekels, setAmountShekels] = useState<number>(0);
  const [receivedAt, setReceivedAt] = useState(todayInJerusalem());
  const [method, setMethod] = useState<PaymentMethod>('BANK_TRANSFER');
  const [reference, setReference] = useState('');
  const [notes, setNotes] = useState('');
  const [activityId, setActivityId] = useState<string | null>(null);
  const [issueReceipt, setIssueReceipt] = useState(true);
  const [allocations, setAllocations] = useState<Record<string, number>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [prefilled, setPrefilled] = useState(false);

  const debouncedCustomerSearch = useDebounced(customerSearch, 250);

  useEffect(() => {
    if (!opened) return;
    setCustomer(customerId ?? null);
    setAmountShekels(0);
    setReceivedAt(todayInJerusalem());
    setMethod('BANK_TRANSFER');
    setReference('');
    setNotes('');
    setActivityId(settleDocument?.activityId ?? null);
    setIssueReceipt(true);
    setAllocations({});
    setFormError(null);
    setPrefilled(false);
  }, [opened, customerId, settleDocument?.id, settleDocument?.activityId]);

  const customers = useQuery({
    queryKey: ['customers', 'picker', debouncedCustomerSearch],
    queryFn: () =>
      api.get<CustomerPage>(`/customers/?q=${encodeURIComponent(debouncedCustomerSearch)}&limit=50`),
    enabled: opened,
  });

  const activities = useQuery({
    queryKey: ['activities', 'picker'],
    queryFn: () => api.get<ActivityPage>('/activities/?limit=200'),
    enabled: opened,
  });

  const outstanding = useQuery({
    queryKey: ['outstanding', customer],
    queryFn: () => api.get<{ documents: OutstandingDocument[] }>(`/customers/${customer}/outstanding`),
    enabled: opened && Boolean(customer),
  });

  const settledDocument = settleDocument
    ? outstanding.data?.documents.find((item) => item.id === settleDocument.id)
    : undefined;

  // The outstanding balance arrives after the modal opens, so the prefill waits
  // for it, and runs once so it never overwrites what the operator has typed.
  useEffect(() => {
    if (!opened || prefilled || !settledDocument) return;
    setAmountShekels(agorotToShekels(settledDocument.outstanding_agorot));
    setAllocations({ [settledDocument.id]: settledDocument.outstanding_agorot });
    setPrefilled(true);
  }, [opened, prefilled, settledDocument]);

  const amountAgorot = shekelsToAgorot(amountShekels);
  const allocatedAgorot = useMemo(
    () => Object.values(allocations).reduce((sum, value) => sum + value, 0),
    [allocations],
  );
  const onAccountAgorot = amountAgorot - allocatedAgorot;

  const setAllocation = (documentId: string, agorot: number) => {
    setAllocations((current) => {
      const next = { ...current };
      if (agorot <= 0) delete next[documentId];
      else next[documentId] = agorot;
      return next;
    });
  };

  /** Spreads the amount over the oldest outstanding documents first. */
  const autoAllocate = () => {
    let remaining = amountAgorot;
    const next: Record<string, number> = {};
    for (const document of [...(outstanding.data?.documents ?? [])].reverse()) {
      if (remaining <= 0) break;
      const take = Math.min(remaining, document.outstanding_agorot);
      if (take > 0) {
        next[document.id] = take;
        remaining -= take;
      }
    }
    setAllocations(next);
  };

  /** Builds the same body the save uses, so the preview shows what will issue. */
  const buildPayload = (): PaymentInput => ({
    customer_id: customer ?? '',
    amount_agorot: amountAgorot,
    received_at: receivedAt,
    method,
    reference,
    notes,
    activity_id: activityId,
    allocations: Object.entries(allocations).map(([document_id, amount_agorot]) => ({
      document_id,
      amount_agorot,
    })),
    issue_receipt: issueReceipt,
  });

  // A receipt is never a draft — it commits with the payment — so this is the
  // only chance to look at one before it becomes immutable.
  const previewReceipt = useMutation({
    mutationFn: async () => {
      const csrf = document.cookie.match(/(?:^|; )csrf_token=([^;]*)/)?.[1];
      const response = await fetch(`${API_BASE}/payments/preview`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
          ...(csrf ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {}),
        },
        body: JSON.stringify(buildPayload()),
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => null);
        throw new Error(payload?.error?.message ?? 'הכנת התצוגה המקדימה נכשלה.');
      }
      return response.blob();
    },
    onSuccess: (blob) => {
      // Opened in a tab and revoked shortly after, so nothing is left on disk.
      const url = URL.createObjectURL(blob);
      window.open(url, '_blank', 'noopener');
      window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
    },
    onError: (error) => setFormError(error instanceof Error ? error.message : 'התצוגה המקדימה נכשלה.'),
  });

  const saveMutation = useMutation({
    mutationFn: () => {
      const payload = buildPayload();
      return api.post<Payment>('/payments/', payload, {
        // A retry after a timeout must not record the money twice.
        'Idempotency-Key': `payment-${customer}-${receivedAt}-${amountAgorot}-${method}`,
      });
    },
    onSuccess: (payment) => {
      queryClient.invalidateQueries({ queryKey: ['payments'] });
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      queryClient.invalidateQueries({ queryKey: ['balance'] });
      queryClient.invalidateQueries({ queryKey: ['timeline'] });
      queryClient.invalidateQueries({ queryKey: ['outstanding'] });
      notifications.show({
        color: 'teal',
        message: payment.receipt_number
          ? `התשלום נרשם והופקה קבלה ${payment.receipt_number}`
          : 'התשלום נרשם ללא קבלה.',
      });
      onClose();
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const fields = Object.values(error.fieldErrors());
        setFormError(fields.length > 0 ? fields.join(' · ') : error.message);
        return;
      }
      setFormError('רישום התשלום נכשל.');
    },
  });

  return (
    <Modal opened={opened} onClose={onClose} title={settleDocument ? 'הפקת קבלה' : 'רישום תשלום'} size="xl" centered>
      <Stack gap="md">
        {formError && (
          <Alert color="red" variant="light" role="alert" withCloseButton onClose={() => setFormError(null)}>
            {formError}
          </Alert>
        )}

        <Grid>
          <Grid.Col span={{ base: 12, sm: 6 }}>
            <Select
              label="לקוח"
              withAsterisk
              searchable
              placeholder="חיפוש לקוח"
              data={(customers.data?.customers ?? []).map((item) => ({
                value: item.id,
                label: item.phone ? `${item.display_name} · ${item.phone}` : item.display_name,
              }))}
              value={customer}
              onChange={(value) => {
                setCustomer(value);
                setAllocations({});
              }}
              searchValue={customerSearch}
              onSearchChange={setCustomerSearch}
              nothingFoundMessage="לא נמצאו לקוחות"
              disabled={Boolean(customerId)}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 3 }}>
            <NumberInput
              label="סכום שהתקבל"
              withAsterisk
              min={0}
              step={10}
              decimalScale={2}
              fixedDecimalScale
              suffix=" ₪"
              value={amountShekels}
              onChange={(value) => {
                const shekels = typeof value === 'number' ? value : Number(value) || 0;
                setAmountShekels(shekels);
                // A partial payment against the document it was opened from
                // settles that much of it, rather than leaving an allocation
                // larger than the payment and a save that refuses to enable.
                if (settledDocument) {
                  setAllocation(
                    settledDocument.id,
                    Math.min(shekelsToAgorot(shekels), settledDocument.outstanding_agorot),
                  );
                }
              }}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 3 }}>
            <TextInput
              label="תאריך קבלה"
              type="date"
              dir="ltr"
              value={receivedAt}
              onChange={(event) => setReceivedAt(event.currentTarget.value)}
            />
          </Grid.Col>

          <Grid.Col span={{ base: 12, sm: 4 }}>
            <Select
              label="אמצעי תשלום"
              data={Object.entries(PAYMENT_METHOD_LABELS).map(([value, label]) => ({ value, label }))}
              value={method}
              onChange={(value) => value && setMethod(value as PaymentMethod)}
              allowDeselect={false}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 4 }}>
            <TextInput
              label="אסמכתא"
              description="מספר המחאה, אסמכתת העברה וכדומה"
              value={reference}
              onChange={(event) => setReference(event.currentTarget.value)}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 4 }}>
            <Select
              label="פעילות"
              placeholder="ללא"
              searchable
              clearable
              data={(activities.data?.activities ?? []).map((activity) => ({
                value: activity.id,
                label: activity.name,
              }))}
              value={activityId}
              onChange={setActivityId}
            />
          </Grid.Col>
        </Grid>

        {customer && (outstanding.data?.documents.length ?? 0) > 0 && (
          <Card withBorder padding="sm">
            <Group justify="space-between" mb="xs">
              <Text fw={600}>שיוך למסמכים פתוחים</Text>
              <Button size="xs" variant="light" onClick={autoAllocate} disabled={amountAgorot <= 0}>
                שיוך אוטומטי
              </Button>
            </Group>

            <Table.ScrollContainer minWidth={520}>
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>מסמך</Table.Th>
                    <Table.Th>תאריך</Table.Th>
                    <Table.Th>יתרה פתוחה</Table.Th>
                    <Table.Th>לשייך</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(outstanding.data?.documents ?? []).map((document) => (
                    <Table.Tr key={document.id}>
                      <Table.Td>
                        <Text size="sm">{DOCUMENT_TYPE_LABELS[document.document_type]}</Text>
                        <Text size="xs" c="dimmed" className="numeric">
                          {document.full_number}
                        </Text>
                      </Table.Td>
                      <Table.Td className="numeric">
                        {formatDate(`${document.document_date}T00:00:00Z`)}
                      </Table.Td>
                      <Table.Td className="numeric">{formatMoney(document.outstanding_agorot)}</Table.Td>
                      <Table.Td>
                        <NumberInput
                          size="sm"
                          min={0}
                          max={agorotToShekels(document.outstanding_agorot)}
                          decimalScale={2}
                          fixedDecimalScale
                          suffix=" ₪"
                          value={agorotToShekels(allocations[document.id] ?? 0)}
                          onChange={(value) =>
                            setAllocation(
                              document.id,
                              shekelsToAgorot(typeof value === 'number' ? value : Number(value) || 0),
                            )
                          }
                        />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>

            <Group mt="sm" gap="xl">
              <Text size="sm" c="dimmed">
                משויך: <span className="numeric">{formatMoney(allocatedAgorot)}</span>
              </Text>
              <Text size="sm" c={onAccountAgorot < 0 ? 'red' : 'dimmed'}>
                על החשבון: <span className="numeric">{formatMoney(onAccountAgorot)}</span>
              </Text>
            </Group>
          </Card>
        )}

        {customer && amountAgorot > 0 && (outstanding.data?.documents.length ?? 0) === 0 && (
          <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />}>
            ללקוח אין מסמכים פתוחים. התשלום יירשם על החשבון, ותופק עבורו קבלה.
          </Alert>
        )}

        <Textarea
          label="הערות"
          autosize
          minRows={2}
          value={notes}
          onChange={(event) => setNotes(event.currentTarget.value)}
        />

        <Checkbox
          label="הפקת קבלה עם רישום התשלום"
          description="ברירת המחדל. יש לבטל רק אם שירות ה־PDF אינו זמין; אפשר להפיק את הקבלה מאוחר יותר."
          checked={issueReceipt}
          onChange={(event) => setIssueReceipt(event.currentTarget.checked)}
        />

        <Group justify="space-between">
          <Button
            variant="default"
            leftSection={<IconEye size={18} />}
            loading={previewReceipt.isPending}
            disabled={!customer || amountAgorot <= 0 || onAccountAgorot < 0 || !issueReceipt}
            onClick={() => {
              setFormError(null);
              previewReceipt.mutate();
            }}
          >
            תצוגה מקדימה של הקבלה
          </Button>

          <Group>
            <Button variant="default" onClick={onClose}>
              ביטול
            </Button>
            <Button
              loading={saveMutation.isPending}
              disabled={!customer || amountAgorot <= 0 || onAccountAgorot < 0}
              onClick={() => {
                setFormError(null);
                saveMutation.mutate();
              }}
            >
              רישום תשלום
            </Button>
          </Group>
        </Group>
      </Stack>
    </Modal>
  );
}
