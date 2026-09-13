import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import {
  ActionIcon,
  Alert,
  Button,
  Card,
  Grid,
  Group,
  NumberInput,
  Select,
  Skeleton,
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
import { IconArrowRight, IconPlus, IconTrash } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type {
  ActivityPage,
  CustomerPage,
  DocumentDraftInput,
  DocumentType,
  InvoiceDocument,
  ServicePage,
} from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { formatMoney } from '@/lib/format';
import { DOCUMENT_TYPE_LABELS } from '@/lib/labels';
import { agorotToShekels, milliToQuantity, quantityToMilli, shekelsToAgorot } from '@/lib/money';

/** A line as the form holds it: shekels and decimal quantities, not agorot. */
interface FormLine {
  serviceId: string | null;
  description: string;
  unit: string;
  quantity: number;
  priceShekels: number;
}

const EMPTY_LINE: FormLine = { serviceId: null, description: '', unit: '', quantity: 1, priceShekels: 0 };

function todayInJerusalem(): string {
  // The API expects a business date already in Asia/Jerusalem.
  return new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Jerusalem' }).format(new Date());
}

/**
 * Create or edit a draft. Only drafts reach this page: an issued document is
 * immutable, and the UI never offers an edit action for one (plan.md 7).
 */
export function DocumentEditorPage() {
  const { id } = useParams();
  const isEdit = Boolean(id);
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  const [documentType, setDocumentType] = useState<DocumentType>('TRANSACTION_INVOICE');
  const [customerId, setCustomerId] = useState<string | null>(null);
  const [activityId, setActivityId] = useState<string | null>(null);
  const [documentDate, setDocumentDate] = useState(todayInJerusalem());
  const [dueDate, setDueDate] = useState('');
  const [notes, setNotes] = useState('');
  const [lines, setLines] = useState<FormLine[]>([{ ...EMPTY_LINE }]);
  const [customerSearch, setCustomerSearch] = useState('');
  const [formError, setFormError] = useState<string | null>(null);

  const debouncedCustomerSearch = useDebounced(customerSearch, 250);

  const existing = useQuery({
    queryKey: ['document', id],
    queryFn: () => api.get<InvoiceDocument>(`/documents/${id}`),
    enabled: isEdit,
  });

  const customers = useQuery({
    queryKey: ['customers', 'picker', debouncedCustomerSearch],
    queryFn: () =>
      api.get<CustomerPage>(`/customers/?q=${encodeURIComponent(debouncedCustomerSearch)}&limit=50`),
  });

  const services = useQuery({
    queryKey: ['services', 'picker'],
    queryFn: () => api.get<ServicePage>('/services/?limit=200'),
  });

  const activities = useQuery({
    queryKey: ['activities', 'picker'],
    queryFn: () => api.get<ActivityPage>('/activities/?limit=200'),
  });

  useEffect(() => {
    const document = existing.data;
    if (!document) return;

    setDocumentType(document.document_type);
    setCustomerId(document.customer_id);
    setActivityId(document.activity_id);
    setDocumentDate(document.document_date);
    setDueDate(document.due_date ?? '');
    setNotes(document.notes);
    setLines(
      document.lines.map((line) => ({
        serviceId: line.service_id,
        description: line.description,
        unit: line.unit,
        quantity: milliToQuantity(line.quantity_milli),
        priceShekels: agorotToShekels(line.unit_price_agorot),
      })),
    );
  }, [existing.data]);

  // A local preview only. The server recomputes every total on save and on
  // issuance, and its answer is the one that counts (plan.md 3.2).
  const previewTotal = useMemo(
    () =>
      lines.reduce(
        (sum, line) => sum + Math.round(shekelsToAgorot(line.priceShekels) * quantityToMilli(line.quantity) / 1000),
        0,
      ),
    [lines],
  );

  const updateLine = (index: number, patch: Partial<FormLine>) => {
    setLines((current) => current.map((line, at) => (at === index ? { ...line, ...patch } : line)));
  };

  const applyService = (index: number, serviceId: string | null) => {
    const service = services.data?.services.find((item) => item.id === serviceId);
    updateLine(index, {
      serviceId,
      ...(service
        ? {
            description: service.name,
            unit: service.unit,
            priceShekels: agorotToShekels(service.default_price_agorot),
          }
        : {}),
    });
  };

  const saveMutation = useMutation({
    mutationFn: (): Promise<InvoiceDocument> => {
      const payload: DocumentDraftInput = {
        document_type: documentType,
        customer_id: customerId ?? '',
        activity_id: activityId,
        document_date: documentDate,
        due_date: dueDate || null,
        notes,
        lines: lines.map((line) => ({
          service_id: line.serviceId,
          description: line.description,
          unit: line.unit,
          quantity_milli: quantityToMilli(line.quantity),
          unit_price_agorot: shekelsToAgorot(line.priceShekels),
        })),
      };
      return isEdit
        ? api.put<InvoiceDocument>(`/documents/${id}`, payload)
        : api.post<InvoiceDocument>('/documents/', payload);
    },
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      queryClient.invalidateQueries({ queryKey: ['document', saved.id] });
      notifications.show({ color: 'teal', message: 'הטיוטה נשמרה.' });
      navigate(`/documents/${saved.id}`);
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const fields = error.fieldErrors();
        const messages = Object.values(fields);
        setFormError(messages.length > 0 ? messages.join(' · ') : error.message);
        return;
      }
      setFormError('שמירת הטיוטה נכשלה.');
    },
  });

  if (isEdit && existing.isLoading) return <Skeleton height={420} />;

  if (isEdit && existing.data && existing.data.state !== 'DRAFT') {
    return (
      <Stack gap="md">
        <Alert color="orange" variant="light" title="המסמך אינו ניתן לעריכה">
          מסמך שהופק אינו ניתן לעריכה או למחיקה.
        </Alert>
        <Button component="a" href={`/documents/${id}`} w="fit-content">
          מעבר למסמך
        </Button>
      </Stack>
    );
  }

  return (
    <Stack gap="lg">
      <Group gap="sm">
        <Button variant="subtle" leftSection={<IconArrowRight size={18} />} onClick={() => navigate('/documents')}>
          מסמכים
        </Button>
        <Title order={2}>{isEdit ? 'עריכת טיוטה' : 'מסמך חדש'}</Title>
      </Group>

      {formError && (
        <Alert color="red" variant="light" role="alert" onClose={() => setFormError(null)} withCloseButton>
          {formError}
        </Alert>
      )}

      <Card withBorder padding="lg">
        <Grid>
          <Grid.Col span={{ base: 12, sm: 6, md: 3 }}>
            <Select
              label="סוג מסמך"
              data={Object.entries(DOCUMENT_TYPE_LABELS).map(([value, label]) => ({ value, label }))}
              value={documentType}
              onChange={(value) => value && setDocumentType(value as DocumentType)}
              allowDeselect={false}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 6, md: 5 }}>
            <Select
              label="לקוח"
              withAsterisk
              searchable
              placeholder="חיפוש לקוח"
              data={(customers.data?.customers ?? []).map((customer) => ({
                value: customer.id,
                label: customer.phone ? `${customer.display_name} · ${customer.phone}` : customer.display_name,
              }))}
              value={customerId}
              onChange={setCustomerId}
              searchValue={customerSearch}
              onSearchChange={setCustomerSearch}
              nothingFoundMessage="לא נמצאו לקוחות"
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 6, md: 4 }}>
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

          <Grid.Col span={{ base: 12, sm: 6, md: 3 }}>
            <TextInput
              label="תאריך המסמך"
              type="date"
              dir="ltr"
              value={documentDate}
              onChange={(event) => setDocumentDate(event.currentTarget.value)}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, sm: 6, md: 3 }}>
            <TextInput
              label="לתשלום עד"
              type="date"
              dir="ltr"
              value={dueDate}
              onChange={(event) => setDueDate(event.currentTarget.value)}
            />
          </Grid.Col>
        </Grid>
      </Card>

      <Card withBorder padding="lg">
        <Group justify="space-between" mb="sm">
          <Title order={4}>שורות</Title>
          <Button
            size="xs"
            variant="light"
            leftSection={<IconPlus size={16} />}
            onClick={() => setLines((current) => [...current, { ...EMPTY_LINE }])}
          >
            הוספת שורה
          </Button>
        </Group>

        <Table.ScrollContainer minWidth={760}>
          <Table>
            <Table.Thead>
              <Table.Tr>
                <Table.Th style={{ width: '22%' }}>שירות מהקטלוג</Table.Th>
                <Table.Th style={{ width: '26%' }}>תיאור</Table.Th>
                <Table.Th style={{ width: '12%' }}>יחידה</Table.Th>
                <Table.Th style={{ width: '12%' }}>כמות</Table.Th>
                <Table.Th style={{ width: '14%' }}>מחיר ליחידה</Table.Th>
                <Table.Th style={{ width: '10%' }}>סה״כ</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {lines.map((line, index) => {
                const lineTotal = Math.round(
                  (shekelsToAgorot(line.priceShekels) * quantityToMilli(line.quantity)) / 1000,
                );
                return (
                  <Table.Tr key={index}>
                    <Table.Td>
                      <Select
                        placeholder="שורה חופשית"
                        searchable
                        clearable
                        data={(services.data?.services ?? []).map((service) => ({
                          value: service.id,
                          label: service.name,
                        }))}
                        value={line.serviceId}
                        onChange={(value) => applyService(index, value)}
                        size="sm"
                      />
                    </Table.Td>
                    <Table.Td>
                      <TextInput
                        size="sm"
                        value={line.description}
                        onChange={(event) => updateLine(index, { description: event.currentTarget.value })}
                      />
                    </Table.Td>
                    <Table.Td>
                      <TextInput
                        size="sm"
                        value={line.unit}
                        onChange={(event) => updateLine(index, { unit: event.currentTarget.value })}
                      />
                    </Table.Td>
                    <Table.Td>
                      <NumberInput
                        size="sm"
                        min={0}
                        step={0.5}
                        decimalScale={3}
                        value={line.quantity}
                        onChange={(value) =>
                          updateLine(index, { quantity: typeof value === 'number' ? value : Number(value) || 0 })
                        }
                      />
                    </Table.Td>
                    <Table.Td>
                      <NumberInput
                        size="sm"
                        min={0}
                        step={1}
                        decimalScale={2}
                        fixedDecimalScale
                        suffix=" ₪"
                        value={line.priceShekels}
                        onChange={(value) =>
                          updateLine(index, {
                            priceShekels: typeof value === 'number' ? value : Number(value) || 0,
                          })
                        }
                      />
                    </Table.Td>
                    <Table.Td className="numeric">{formatMoney(lineTotal)}</Table.Td>
                    <Table.Td>
                      <Tooltip label="מחיקת שורה">
                        <ActionIcon
                          variant="subtle"
                          color="red"
                          aria-label="מחיקת שורה"
                          disabled={lines.length === 1}
                          onClick={() => setLines((current) => current.filter((_, at) => at !== index))}
                        >
                          <IconTrash size={18} />
                        </ActionIcon>
                      </Tooltip>
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        <Group justify="flex-start" mt="md">
          <Text c="dimmed">סה״כ (חישוב מקדים)</Text>
          <Text fw={700} className="numeric">
            {formatMoney(previewTotal)}
          </Text>
        </Group>
        <Text size="xs" c="dimmed">
          הסכום הסופי מחושב בשרת בעת השמירה וההפקה.
        </Text>
      </Card>

      <Card withBorder padding="lg">
        <Textarea
          label="הערות למסמך"
          autosize
          minRows={2}
          value={notes}
          onChange={(event) => setNotes(event.currentTarget.value)}
        />
      </Card>

      <Group justify="flex-end">
        <Button variant="default" onClick={() => navigate('/documents')}>
          ביטול
        </Button>
        <Button
          loading={saveMutation.isPending}
          onClick={() => {
            setFormError(null);
            saveMutation.mutate();
          }}
        >
          שמירת טיוטה
        </Button>
      </Group>
    </Stack>
  );
}
