import { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  Grid,
  Group,
  List,
  Modal,
  Select,
  Stack,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { IconAlertTriangle } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { Customer, CustomerDuplicate, CustomerInput } from '@/api/types';
import { CUSTOMER_TYPE_LABELS, DELIVERY_LABELS } from '@/lib/labels';

const schema = z.object({
  customer_type: z.enum(['PERSON', 'BUSINESS']),
  display_name: z.string().min(1, 'יש להזין שם'),
  legal_name: z.string(),
  business_or_id_number: z.string(),
  phone: z.string(),
  email: z.string(),
  address: z.string(),
  notes: z.string(),
  preferred_delivery: z.enum(['EMAIL', 'WHATSAPP', 'NONE']),
});

type FormValues = z.infer<typeof schema>;

const EMPTY: FormValues = {
  customer_type: 'PERSON',
  display_name: '',
  legal_name: '',
  business_or_id_number: '',
  phone: '',
  email: '',
  address: '',
  notes: '',
  preferred_delivery: 'NONE',
};

interface Props {
  opened: boolean;
  onClose: () => void;
  /** Absent when creating. */
  customer?: Customer;
  onSaved?: (customer: Customer) => void;
}

/**
 * Create or edit a customer. On create the server may answer with possible
 * duplicates; they are shown for the operator to judge, and the same form is
 * resubmitted with confirmation if they decide it really is a new customer
 * (plan.md 8).
 */
export function CustomerFormModal({ opened, onClose, customer, onSaved }: Props) {
  const queryClient = useQueryClient();
  const [duplicates, setDuplicates] = useState<CustomerDuplicate[]>([]);
  const isEdit = Boolean(customer);

  const { register, handleSubmit, reset, setValue, watch, setError, formState } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: EMPTY,
  });

  useEffect(() => {
    if (!opened) return;
    setDuplicates([]);
    if (customer) {
      const { id: _id, active: _active, created_at: _c, updated_at: _u, ...values } = customer;
      reset(values);
    } else {
      reset(EMPTY);
    }
  }, [opened, customer, reset]);

  const applyServerErrors = (error: unknown): boolean => {
    if (!(error instanceof ApiError)) return false;
    let handled = false;
    for (const [field, message] of Object.entries(error.fieldErrors())) {
      if (field in EMPTY) {
        setError(field as keyof FormValues, { message });
        handled = true;
      }
    }
    return handled;
  };

  const saveMutation = useMutation({
    mutationFn: (input: CustomerInput & { confirm_duplicate?: boolean }) =>
      isEdit
        ? api.put<Customer>(`/customers/${customer!.id}`, stripConfirm(input))
        : api.post<Customer>('/customers/', input),
    onSuccess: (saved) => {
      queryClient.invalidateQueries({ queryKey: ['customers'] });
      queryClient.invalidateQueries({ queryKey: ['customer', saved.id] });
      notifications.show({ color: 'teal', message: isEdit ? 'הלקוח עודכן.' : 'הלקוח נוצר.' });
      onSaved?.(saved);
      onClose();
    },
    onError: (error) => {
      if (error instanceof ApiError && error.details?.code === 'DUPLICATE_CUSTOMER') {
        setDuplicates((error.details.duplicates as CustomerDuplicate[]) ?? []);
        return;
      }
      if (!applyServerErrors(error)) {
        notifications.show({
          color: 'red',
          message: error instanceof ApiError ? error.message : 'שמירת הלקוח נכשלה.',
        });
      }
    },
  });

  const submit = (values: FormValues, confirmDuplicate = false) => {
    saveMutation.mutate({ ...values, confirm_duplicate: confirmDuplicate });
  };

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={isEdit ? 'עריכת לקוח' : 'לקוח חדש'}
      size="lg"
      centered
    >
      <form onSubmit={handleSubmit((values) => submit(values))} noValidate>
        <Stack gap="md">
          {duplicates.length > 0 && (
            <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} title="נמצאו לקוחות דומים">
              <Stack gap="xs">
                <List size="sm" spacing={4}>
                  {duplicates.map((duplicate) => (
                    <List.Item key={duplicate.customer.id}>
                      {duplicate.customer.display_name}
                      {duplicate.customer.phone && ` · ${duplicate.customer.phone}`}
                      <Text span size="xs" c="dimmed">
                        {' '}
                        ({duplicate.reason_hebrew})
                      </Text>
                    </List.Item>
                  ))}
                </List>
                <Text size="sm">אם זה לקוח חדש באמת, אפשר להמשיך.</Text>
                <Group>
                  <Button
                    size="xs"
                    variant="light"
                    color="orange"
                    loading={saveMutation.isPending}
                    onClick={handleSubmit((values) => submit(values, true))}
                  >
                    זה לקוח חדש, המשך
                  </Button>
                </Group>
              </Stack>
            </Alert>
          )}

          <Grid>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <Select
                label="סוג לקוח"
                data={Object.entries(CUSTOMER_TYPE_LABELS).map(([value, label]) => ({ value, label }))}
                value={watch('customer_type')}
                onChange={(value) => value && setValue('customer_type', value as FormValues['customer_type'])}
                allowDeselect={false}
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="שם"
                withAsterisk
                error={formState.errors.display_name?.message}
                {...register('display_name')}
              />
            </Grid.Col>

            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput label="שם רשמי" {...register('legal_name')} />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="מספר עוסק / ת״ז"
                description="לא חובה"
                dir="ltr"
                inputMode="numeric"
                {...register('business_or_id_number')}
              />
            </Grid.Col>

            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="טלפון"
                dir="ltr"
                inputMode="tel"
                error={formState.errors.phone?.message}
                {...register('phone')}
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput
                label="דוא״ל"
                dir="ltr"
                inputMode="email"
                error={formState.errors.email?.message}
                {...register('email')}
              />
            </Grid.Col>

            <Grid.Col span={{ base: 12, sm: 6 }}>
              <Select
                label="אמצעי משלוח מועדף"
                data={Object.entries(DELIVERY_LABELS).map(([value, label]) => ({ value, label }))}
                value={watch('preferred_delivery')}
                onChange={(value) =>
                  value && setValue('preferred_delivery', value as FormValues['preferred_delivery'])
                }
                error={formState.errors.preferred_delivery?.message}
                allowDeselect={false}
              />
            </Grid.Col>
            <Grid.Col span={{ base: 12, sm: 6 }}>
              <TextInput label="כתובת" {...register('address')} />
            </Grid.Col>

            <Grid.Col span={12}>
              <Textarea label="הערות" autosize minRows={2} {...register('notes')} />
            </Grid.Col>
          </Grid>

          <Group justify="flex-end">
            <Button variant="default" onClick={onClose}>
              ביטול
            </Button>
            <Button type="submit" loading={saveMutation.isPending}>
              שמירה
            </Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

/** The update endpoint rejects unknown fields, so confirm_duplicate is dropped. */
function stripConfirm(input: CustomerInput & { confirm_duplicate?: boolean }): CustomerInput {
  const { confirm_duplicate: _confirm, ...rest } = input;
  return rest;
}
