import { useEffect, useState } from 'react';
import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Grid,
  Group,
  Loader,
  Modal,
  NumberInput,
  Select,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
  Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { IconEdit, IconPlus, IconSearch } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { ServiceItem, ServicePage } from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { useSession } from '@/hooks/useSession';
import { formatMoney } from '@/lib/format';
import { agorotToShekels, shekelsToAgorot } from '@/lib/money';

const schema = z.object({
  name: z.string().min(1, 'יש להזין שם שירות'),
  description: z.string(),
  price_shekels: z.number().min(0, 'המחיר אינו יכול להיות שלילי'),
  unit: z.string(),
  category: z.string(),
});

type FormValues = z.infer<typeof schema>;

const EMPTY: FormValues = { name: '', description: '', price_shekels: 0, unit: '', category: '' };

export function ServicesPage() {
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [query, setQuery] = useState('');
  const [category, setCategory] = useState<string | null>(null);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [editing, setEditing] = useState<ServiceItem | undefined>();
  const [formOpen, setFormOpen] = useState(false);

  const debouncedQuery = useDebounced(query, 300);

  const services = useQuery({
    queryKey: ['services', debouncedQuery, category, includeArchived],
    queryFn: () =>
      api.get<ServicePage>(
        `/services/?q=${encodeURIComponent(debouncedQuery)}` +
          `&category=${encodeURIComponent(category ?? '')}&include_archived=${includeArchived}`,
      ),
  });

  const categories = useQuery({
    queryKey: ['service-categories'],
    queryFn: () => api.get<{ categories: string[] }>('/services/categories'),
  });

  const { register, handleSubmit, reset, setValue, watch, setError, formState } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: EMPTY,
  });

  useEffect(() => {
    if (!formOpen) return;
    reset(
      editing
        ? {
            name: editing.name,
            description: editing.description,
            price_shekels: agorotToShekels(editing.default_price_agorot),
            unit: editing.unit,
            category: editing.category,
          }
        : EMPTY,
    );
  }, [formOpen, editing, reset]);

  const saveMutation = useMutation({
    mutationFn: (values: FormValues) => {
      // The API speaks agorot; the form speaks shekels. The conversion happens
      // here, once, at the edge.
      const payload = {
        name: values.name,
        description: values.description,
        default_price_agorot: shekelsToAgorot(values.price_shekels),
        unit: values.unit,
        category: values.category,
      };
      return editing
        ? api.put<ServiceItem>(`/services/${editing.id}`, payload)
        : api.post<ServiceItem>('/services/', payload);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['services'] });
      queryClient.invalidateQueries({ queryKey: ['service-categories'] });
      notifications.show({ color: 'teal', message: editing ? 'השירות עודכן.' : 'השירות נוצר.' });
      setFormOpen(false);
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const fields = error.fieldErrors();
        if (fields.name) setError('name', { message: fields.name });
        if (fields.default_price_agorot) setError('price_shekels', { message: fields.default_price_agorot });
        notifications.show({ color: 'red', message: error.message });
        return;
      }
      notifications.show({ color: 'red', message: 'שמירת השירות נכשלה.' });
    },
  });

  const activeMutation = useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) =>
      api.put<ServiceItem>(`/services/${id}/active`, { active }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['services'] }),
    onError: () => notifications.show({ color: 'red', message: 'עדכון השירות נכשל.' }),
  });

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <div>
          <Title order={2}>שירותים</Title>
          <Text c="dimmed" size="sm">
            קטלוג לשימוש חוזר במסמכים. אפשר תמיד להוסיף שורה חופשית במסמך עצמו.
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
            שירות חדש
          </Button>
        )}
      </Group>

      <Card withBorder padding="md">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <TextInput
            placeholder="חיפוש שירות"
            leftSection={<IconSearch size={18} />}
            rightSection={services.isFetching ? <Loader size="xs" /> : null}
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
          <Switch
            label="הצג גם ארכיון"
            checked={includeArchived}
            onChange={(event) => setIncludeArchived(event.currentTarget.checked)}
          />
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={680}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>שם</Table.Th>
                <Table.Th>קטגוריה</Table.Th>
                <Table.Th>מחיר ברירת מחדל</Table.Th>
                <Table.Th>יחידה</Table.Th>
                <Table.Th>פעיל</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(services.data?.services ?? []).map((service) => (
                <Table.Tr key={service.id}>
                  <Table.Td>
                    <Group gap="xs">
                      <Text>{service.name}</Text>
                      {!service.active && (
                        <Badge size="xs" color="gray" variant="light">
                          ארכיון
                        </Badge>
                      )}
                    </Group>
                    {service.description && (
                      <Text size="xs" c="dimmed">
                        {service.description}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>{service.category || '—'}</Table.Td>
                  <Table.Td className="numeric">{formatMoney(service.default_price_agorot)}</Table.Td>
                  <Table.Td>{service.unit || '—'}</Table.Td>
                  <Table.Td>
                    <Switch
                      checked={service.active}
                      disabled={!canEdit || activeMutation.isPending}
                      aria-label={service.active ? 'העברה לארכיון' : 'שחזור מהארכיון'}
                      onChange={(event) =>
                        activeMutation.mutate({ id: service.id, active: event.currentTarget.checked })
                      }
                    />
                  </Table.Td>
                  <Table.Td>
                    {canEdit && (
                      <Tooltip label="עריכה">
                        <ActionIcon
                          variant="subtle"
                          aria-label="עריכת שירות"
                          onClick={() => {
                            setEditing(service);
                            setFormOpen(true);
                          }}
                        >
                          <IconEdit size={18} />
                        </ActionIcon>
                      </Tooltip>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        {!services.isLoading && (services.data?.services.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            עדיין אין שירותים בקטלוג.
          </Text>
        )}
      </Card>

      <Modal
        opened={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? 'עריכת שירות' : 'שירות חדש'}
        centered
      >
        <form onSubmit={handleSubmit((values) => saveMutation.mutate(values))} noValidate>
          <Stack gap="md">
            <TextInput label="שם" withAsterisk error={formState.errors.name?.message} {...register('name')} />
            <Textarea label="תיאור" autosize minRows={2} {...register('description')} />

            <Grid>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <NumberInput
                  label="מחיר ברירת מחדל"
                  suffix=" ₪"
                  decimalScale={2}
                  fixedDecimalScale
                  min={0}
                  step={0.5}
                  value={watch('price_shekels')}
                  onChange={(value) =>
                    setValue('price_shekels', typeof value === 'number' ? value : Number(value) || 0, {
                      shouldValidate: true,
                    })
                  }
                  error={formState.errors.price_shekels?.message}
                />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="יחידה" placeholder="שעה, יחידה, סדנה" {...register('unit')} />
              </Grid.Col>
            </Grid>

            <TextInput label="קטגוריה" {...register('category')} />

            <Group justify="flex-end">
              <Button variant="default" onClick={() => setFormOpen(false)}>
                ביטול
              </Button>
              <Button type="submit" loading={saveMutation.isPending}>
                שמירה
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </Stack>
  );
}
