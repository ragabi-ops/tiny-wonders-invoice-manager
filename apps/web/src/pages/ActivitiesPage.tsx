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
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { IconCalendarPlus, IconEdit, IconSearch } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { Activity, ActivityPage, ActivityStatus } from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { useSession } from '@/hooks/useSession';
import { formatDateTime } from '@/lib/format';
import { ACTIVITY_STATUS_COLORS, ACTIVITY_STATUS_LABELS } from '@/lib/labels';

const schema = z
  .object({
    name: z.string().min(1, 'יש להזין שם פעילות'),
    start_at: z.string(),
    end_at: z.string(),
    location: z.string(),
    status: z.enum(['PLANNED', 'ACTIVE', 'DONE', 'CANCELLED']),
    notes: z.string(),
  })
  .refine(
    (values) => !values.start_at || !values.end_at || new Date(values.end_at) >= new Date(values.start_at),
    { path: ['end_at'], message: 'מועד הסיום אינו יכול להקדים את מועד ההתחלה' },
  );

type FormValues = z.infer<typeof schema>;

const EMPTY: FormValues = {
  name: '',
  start_at: '',
  end_at: '',
  location: '',
  status: 'PLANNED',
  notes: '',
};

/**
 * `datetime-local` inputs speak local wall-clock time with no zone. These two
 * helpers convert between that and the RFC 3339 instants the API uses.
 */
function toLocalInput(iso: string | null): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const offsetMs = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offsetMs).toISOString().slice(0, 16);
}

function fromLocalInput(value: string): string | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

export function ActivitiesPage() {
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<string | null>(null);
  const [editing, setEditing] = useState<Activity | undefined>();
  const [formOpen, setFormOpen] = useState(false);

  const debouncedQuery = useDebounced(query, 300);

  const activities = useQuery({
    queryKey: ['activities', debouncedQuery, status],
    queryFn: () =>
      api.get<ActivityPage>(
        `/activities/?q=${encodeURIComponent(debouncedQuery)}&status=${encodeURIComponent(status ?? '')}`,
      ),
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
            start_at: toLocalInput(editing.start_at),
            end_at: toLocalInput(editing.end_at),
            location: editing.location,
            status: editing.status,
            notes: editing.notes,
          }
        : EMPTY,
    );
  }, [formOpen, editing, reset]);

  const saveMutation = useMutation({
    mutationFn: (values: FormValues) => {
      const payload = {
        name: values.name,
        start_at: fromLocalInput(values.start_at),
        end_at: fromLocalInput(values.end_at),
        location: values.location,
        status: values.status,
        notes: values.notes,
      };
      return editing
        ? api.put<Activity>(`/activities/${editing.id}`, payload)
        : api.post<Activity>('/activities/', payload);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['activities'] });
      notifications.show({ color: 'teal', message: editing ? 'הפעילות עודכנה.' : 'הפעילות נוצרה.' });
      setFormOpen(false);
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const fields = error.fieldErrors();
        if (fields.name) setError('name', { message: fields.name });
        if (fields.end_at) setError('end_at', { message: fields.end_at });
        notifications.show({ color: 'red', message: error.message });
        return;
      }
      notifications.show({ color: 'red', message: 'שמירת הפעילות נכשלה.' });
    },
  });

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <div>
          <Title order={2}>פעילויות</Title>
          <Text c="dimmed" size="sm">
            אירועים וימי מכירה. מסמכים, תשלומים והוצאות יוכלו להשתייך לפעילות לצורך דוח רווחיות.
          </Text>
        </div>
        {canEdit && (
          <Button
            leftSection={<IconCalendarPlus size={18} />}
            onClick={() => {
              setEditing(undefined);
              setFormOpen(true);
            }}
          >
            פעילות חדשה
          </Button>
        )}
      </Group>

      <Card withBorder padding="md">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <TextInput
            placeholder="חיפוש פעילות"
            leftSection={<IconSearch size={18} />}
            rightSection={activities.isFetching ? <Loader size="xs" /> : null}
            value={query}
            onChange={(event) => setQuery(event.currentTarget.value)}
            style={{ flex: 1, minWidth: 240 }}
          />
          <Select
            placeholder="כל הסטטוסים"
            data={Object.entries(ACTIVITY_STATUS_LABELS).map(([value, label]) => ({ value, label }))}
            value={status}
            onChange={setStatus}
            clearable
            w={200}
          />
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={720}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>שם</Table.Th>
                <Table.Th>סטטוס</Table.Th>
                <Table.Th>התחלה</Table.Th>
                <Table.Th>סיום</Table.Th>
                <Table.Th>מיקום</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(activities.data?.activities ?? []).map((activity) => (
                <Table.Tr key={activity.id}>
                  <Table.Td>{activity.name}</Table.Td>
                  <Table.Td>
                    <Badge variant="light" color={ACTIVITY_STATUS_COLORS[activity.status as ActivityStatus]}>
                      {ACTIVITY_STATUS_LABELS[activity.status as ActivityStatus]}
                    </Badge>
                  </Table.Td>
                  <Table.Td className="numeric">{formatDateTime(activity.start_at)}</Table.Td>
                  <Table.Td className="numeric">{formatDateTime(activity.end_at)}</Table.Td>
                  <Table.Td>{activity.location || '—'}</Table.Td>
                  <Table.Td>
                    {canEdit && (
                      <Tooltip label="עריכה">
                        <ActionIcon
                          variant="subtle"
                          aria-label="עריכת פעילות"
                          onClick={() => {
                            setEditing(activity);
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

        {!activities.isLoading && (activities.data?.activities.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            עדיין אין פעילויות במערכת.
          </Text>
        )}
      </Card>

      <Modal
        opened={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? 'עריכת פעילות' : 'פעילות חדשה'}
        centered
      >
        <form onSubmit={handleSubmit((values) => saveMutation.mutate(values))} noValidate>
          <Stack gap="md">
            <TextInput label="שם" withAsterisk error={formState.errors.name?.message} {...register('name')} />

            <Grid>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="התחלה" type="datetime-local" dir="ltr" {...register('start_at')} />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput
                  label="סיום"
                  type="datetime-local"
                  dir="ltr"
                  error={formState.errors.end_at?.message}
                  {...register('end_at')}
                />
              </Grid.Col>
            </Grid>

            <Grid>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="מיקום" {...register('location')} />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <Select
                  label="סטטוס"
                  data={Object.entries(ACTIVITY_STATUS_LABELS).map(([value, label]) => ({ value, label }))}
                  value={watch('status')}
                  onChange={(value) => value && setValue('status', value as ActivityStatus)}
                  allowDeselect={false}
                />
              </Grid.Col>
            </Grid>

            <Textarea label="הערות" autosize minRows={2} {...register('notes')} />

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
