import { useEffect, useRef, useState } from 'react';
import {
  ActionIcon,
  Anchor,
  Button,
  Card,
  FileButton,
  Grid,
  Group,
  Loader,
  Modal,
  NumberInput,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
  Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { IconEdit, IconPaperclip, IconPlus, IconSearch, IconTrash } from '@tabler/icons-react';

import { API_BASE, ApiError, api } from '@/api/client';
import type { ActivityPage, CategoryTotal, Expense, ExpensePage, PaymentMethod } from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { useSession } from '@/hooks/useSession';
import { formatDate, formatMoney } from '@/lib/format';
import { PAYMENT_METHOD_LABELS } from '@/lib/labels';
import { agorotToShekels, shekelsToAgorot } from '@/lib/money';

function todayInJerusalem(): string {
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Jerusalem' }).format(new Date());
}

interface FormState {
  supplier: string;
  expense_date: string;
  amountShekels: number;
  category: string;
  payment_method: PaymentMethod;
  reference: string;
  notes: string;
  activity_id: string | null;
}

const EMPTY: FormState = {
  supplier: '',
  expense_date: todayInJerusalem(),
  amountShekels: 0,
  category: '',
  payment_method: 'CREDIT_CARD',
  reference: '',
  notes: '',
  activity_id: null,
};

export function ExpensesPage() {
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<string | null>(null);
  const [editing, setEditing] = useState<Expense | undefined>();
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY);
  const [formError, setFormError] = useState<string | null>(null);
  const resetFileRef = useRef<() => void>(null);

  const debouncedQuery = useDebounced(query, 300);

  const expenses = useQuery({
    queryKey: ['expenses', debouncedQuery, category],
    queryFn: () =>
      api.get<ExpensePage>(
        `/expenses/?q=${encodeURIComponent(debouncedQuery)}&category=${encodeURIComponent(category ?? '')}`,
      ),
  });

  const categories = useQuery({
    queryKey: ['expense-categories'],
    queryFn: () => api.get<{ categories: string[] }>('/expenses/categories'),
  });

  const byCategory = useQuery({
    queryKey: ['expenses', 'by-category'],
    queryFn: () => api.get<{ totals: CategoryTotal[] }>('/expenses/by-category'),
  });

  const activities = useQuery({
    queryKey: ['activities', 'picker'],
    queryFn: () => api.get<ActivityPage>('/activities/?limit=200'),
  });

  useEffect(() => {
    if (!formOpen) return;
    setFormError(null);
    setForm(
      editing
        ? {
            supplier: editing.supplier,
            expense_date: editing.expense_date,
            amountShekels: agorotToShekels(editing.amount_agorot),
            category: editing.category,
            payment_method: editing.payment_method,
            reference: editing.reference,
            notes: editing.notes,
            activity_id: editing.activity_id,
          }
        : EMPTY,
    );
  }, [formOpen, editing]);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['expenses'] });
    queryClient.invalidateQueries({ queryKey: ['expense-categories'] });
    queryClient.invalidateQueries({ queryKey: ['dashboard'] });
  };

  const saveMutation = useMutation({
    mutationFn: () => {
      const payload = {
        supplier: form.supplier,
        expense_date: form.expense_date,
        amount_agorot: shekelsToAgorot(form.amountShekels),
        category: form.category,
        payment_method: form.payment_method,
        reference: form.reference,
        notes: form.notes,
        activity_id: form.activity_id,
      };
      return editing
        ? api.put<Expense>(`/expenses/${editing.id}`, payload)
        : api.post<Expense>('/expenses/', payload);
    },
    onSuccess: () => {
      invalidate();
      notifications.show({ color: 'teal', message: editing ? 'ההוצאה עודכנה.' : 'ההוצאה נרשמה.' });
      setFormOpen(false);
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const messages = Object.values(error.fieldErrors());
        setFormError(messages.length > 0 ? messages.join(' · ') : error.message);
        return;
      }
      setFormError('שמירת ההוצאה נכשלה.');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete<void>(`/expenses/${id}`),
    onSuccess: () => {
      invalidate();
      notifications.show({ color: 'teal', message: 'ההוצאה נמחקה.' });
    },
    onError: () => notifications.show({ color: 'red', message: 'מחיקת ההוצאה נכשלה.' }),
  });

  const uploadMutation = useMutation({
    mutationFn: async ({ expenseId, file }: { expenseId: string; file: File }) => {
      // Uploads are multipart, so they bypass the JSON helper.
      const body = new FormData();
      body.append('file', file);

      const csrf = document.cookie.match(/(?:^|; )csrf_token=([^;]*)/)?.[1];
      const response = await fetch(`${API_BASE}/expenses/${expenseId}/attachments`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: csrf ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {},
        body,
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => null);
        throw new Error(payload?.error?.message ?? 'העלאת הקובץ נכשלה.');
      }
      return response.json();
    },
    onSuccess: () => {
      invalidate();
      notifications.show({ color: 'teal', message: 'הקובץ צורף.' });
      resetFileRef.current?.();
    },
    onError: (error) =>
      notifications.show({ color: 'red', message: error instanceof Error ? error.message : 'העלאה נכשלה.' }),
  });

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <div>
          <Title order={2}>הוצאות</Title>
          <Text c="dimmed" size="sm">
            מעקב הוצאות לצורכי הנהלת חשבונות. עוסק פטור אינו מקזז מע״מ תשומות.
          </Text>
        </div>
        {canEdit && (
          <Button
            leftSection={<IconPlus size={18} />}
            onClick={() => {
              setEditing(undefined);
              setFormOpen(true);
            }}
          >
            הוצאה חדשה
          </Button>
        )}
      </Group>

      {(byCategory.data?.totals.length ?? 0) > 0 && (
        <Card withBorder padding="md">
          <Text fw={600} mb="xs">
            סיכום לפי קטגוריה
          </Text>
          <Group gap="lg" wrap="wrap">
            {(byCategory.data?.totals ?? []).map((total) => (
              <div key={total.category}>
                <Text size="sm" c="dimmed">
                  {total.category} · {total.count}
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
          <TextInput
            placeholder="חיפוש ספק, קטגוריה או אסמכתא"
            leftSection={<IconSearch size={18} />}
            rightSection={expenses.isFetching ? <Loader size="xs" /> : null}
            value={query}
            onChange={(event) => setQuery(event.currentTarget.value)}
            style={{ flex: 1, minWidth: 240 }}
          />
          <Select
            placeholder="כל הקטגוריות"
            data={(categories.data?.categories ?? []).map((value) => ({ value, label: value }))}
            value={category}
            onChange={setCategory}
            clearable
            w={200}
          />
          <Group gap="xs">
            <Text size="sm" c="dimmed">
              סה״כ:
            </Text>
            <Text fw={600} className="numeric">
              {formatMoney(expenses.data?.total_amount_agorot ?? 0)}
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
                <Table.Th>ספק</Table.Th>
                <Table.Th>סכום</Table.Th>
                <Table.Th>קטגוריה</Table.Th>
                <Table.Th>אמצעי</Table.Th>
                <Table.Th>קבצים</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(expenses.data?.expenses ?? []).map((expense) => (
                <Table.Tr key={expense.id}>
                  <Table.Td className="numeric">
                    {formatDate(`${expense.expense_date}T00:00:00Z`)}
                  </Table.Td>
                  <Table.Td>
                    <Text>{expense.supplier}</Text>
                    {expense.notes && (
                      <Text size="xs" c="dimmed">
                        {expense.notes}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td className="numeric">{formatMoney(expense.amount_agorot)}</Table.Td>
                  <Table.Td>{expense.category || '—'}</Table.Td>
                  <Table.Td>{expense.payment_method_hebrew}</Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      {expense.attachments.map((attachment) => (
                        <Anchor
                          key={attachment.id}
                          size="xs"
                          href={`${API_BASE}/expenses/attachments/${attachment.id}`}
                          className="wrap-anywhere"
                        >
                          {attachment.original_filename}
                        </Anchor>
                      ))}
                      {canEdit && (
                        <FileButton
                          resetRef={resetFileRef}
                          accept="image/jpeg,image/png,image/heic,image/webp,application/pdf"
                          onChange={(file) => file && uploadMutation.mutate({ expenseId: expense.id, file })}
                        >
                          {(props) => (
                            <Button
                              {...props}
                              size="compact-xs"
                              variant="subtle"
                              leftSection={<IconPaperclip size={14} />}
                              loading={uploadMutation.isPending}
                            >
                              צירוף
                            </Button>
                          )}
                        </FileButton>
                      )}
                    </Stack>
                  </Table.Td>
                  <Table.Td>
                    {canEdit && (
                      <Group gap={4} wrap="nowrap">
                        <Tooltip label="עריכה">
                          <ActionIcon
                            variant="subtle"
                            aria-label="עריכת הוצאה"
                            onClick={() => {
                              setEditing(expense);
                              setFormOpen(true);
                            }}
                          >
                            <IconEdit size={18} />
                          </ActionIcon>
                        </Tooltip>
                        <Tooltip label="מחיקה">
                          <ActionIcon
                            variant="subtle"
                            color="red"
                            aria-label="מחיקת הוצאה"
                            onClick={() => deleteMutation.mutate(expense.id)}
                          >
                            <IconTrash size={18} />
                          </ActionIcon>
                        </Tooltip>
                      </Group>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        {!expenses.isLoading && (expenses.data?.expenses.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            עדיין לא נרשמו הוצאות.
          </Text>
        )}
      </Card>

      <Modal
        opened={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? 'עריכת הוצאה' : 'הוצאה חדשה'}
        centered
      >
        <Stack gap="md">
          {formError && (
            <Text c="red" size="sm" role="alert">
              {formError}
            </Text>
          )}

          <TextInput
            label="ספק"
            withAsterisk
            value={form.supplier}
            onChange={(event) => setForm({ ...form, supplier: event.currentTarget.value })}
          />

          <Grid>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <NumberInput
                label="סכום"
                withAsterisk
                min={0}
                step={10}
                decimalScale={2}
                fixedDecimalScale
                suffix=" ₪"
                value={form.amountShekels}
                onChange={(value) =>
                  setForm({ ...form, amountShekels: typeof value === 'number' ? value : Number(value) || 0 })
                }
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="תאריך"
                type="date"
                dir="ltr"
                value={form.expense_date}
                onChange={(event) => setForm({ ...form, expense_date: event.currentTarget.value })}
              />
            </Grid.Col>
          </Grid>

          <Grid>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="קטגוריה"
                value={form.category}
                onChange={(event) => setForm({ ...form, category: event.currentTarget.value })}
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <Select
                label="אמצעי תשלום"
                data={Object.entries(PAYMENT_METHOD_LABELS).map(([value, label]) => ({ value, label }))}
                value={form.payment_method}
                onChange={(value) => value && setForm({ ...form, payment_method: value as PaymentMethod })}
                allowDeselect={false}
              />
            </Grid.Col>
          </Grid>

          <Grid>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="אסמכתא"
                value={form.reference}
                onChange={(event) => setForm({ ...form, reference: event.currentTarget.value })}
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <Select
                label="פעילות"
                placeholder="ללא"
                searchable
                clearable
                data={(activities.data?.activities ?? []).map((activity) => ({
                  value: activity.id,
                  label: activity.name,
                }))}
                value={form.activity_id}
                onChange={(value) => setForm({ ...form, activity_id: value })}
              />
            </Grid.Col>
          </Grid>

          <Textarea
            label="הערות"
            autosize
            minRows={2}
            value={form.notes}
            onChange={(event) => setForm({ ...form, notes: event.currentTarget.value })}
          />

          <Group justify="flex-end">
            <Button variant="default" onClick={() => setFormOpen(false)}>
              ביטול
            </Button>
            <Button loading={saveMutation.isPending} onClick={() => saveMutation.mutate()}>
              שמירה
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
