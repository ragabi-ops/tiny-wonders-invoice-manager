import { useState } from 'react';
import {
  Badge,
  Button,
  Card,
  Group,
  Modal,
  MultiSelect,
  PasswordInput,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { IconUserPlus } from '@tabler/icons-react';

import { ApiError, api } from '@/api/client';
import type { Role, RoleOption, User } from '@/api/types';
import { formatDateTime } from '@/lib/format';
import { ROLE_LABELS } from '@/lib/labels';
import { useSession } from '@/hooks/useSession';

const MIN_LENGTH = 12;

const schema = z.object({
  email: z.string().min(1, 'יש להזין דוא״ל').email('כתובת הדוא״ל אינה תקינה'),
  display_name: z.string().min(1, 'יש להזין שם'),
  password: z.string().min(MIN_LENGTH, `הסיסמה חייבת להכיל לפחות ${MIN_LENGTH} תווים`),
  roles: z.array(z.string()).min(1, 'יש לבחור לפחות תפקיד אחד'),
});

type FormValues = z.infer<typeof schema>;

export function UsersPage() {
  const queryClient = useQueryClient();
  const { user: currentUser } = useSession();
  const [createOpen, setCreateOpen] = useState(false);

  const usersQuery = useQuery({
    queryKey: ['users'],
    queryFn: () => api.get<{ users: User[] }>('/users/'),
  });

  const rolesQuery = useQuery({
    queryKey: ['roles'],
    queryFn: () => api.get<{ roles: RoleOption[] }>('/users/roles'),
    staleTime: Infinity,
  });

  const roleOptions = (rolesQuery.data?.roles ?? []).map((option) => ({
    value: option.role,
    label: option.hebrew,
  }));

  const { register, handleSubmit, reset, setValue, watch, setError, formState } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { email: '', display_name: '', password: '', roles: ['OPERATOR'] },
  });

  const createMutation = useMutation({
    mutationFn: (values: FormValues) => api.post<User>('/users/', values),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['users'] });
      notifications.show({ color: 'teal', message: 'המשתמש נוצר. בכניסה הראשונה תידרש החלפת סיסמה.' });
      setCreateOpen(false);
      reset();
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        for (const [field, message] of Object.entries(error.fieldErrors())) {
          if (['email', 'display_name', 'password', 'roles'].includes(field)) {
            setError(field as keyof FormValues, { message });
          }
        }
        notifications.show({ color: 'red', message: error.message });
        return;
      }
      notifications.show({ color: 'red', message: 'יצירת המשתמש נכשלה.' });
    },
  });

  const activeMutation = useMutation({
    mutationFn: ({ id, active }: { id: string; active: boolean }) =>
      api.put<User>(`/users/${id}/active`, { active, reason: '' }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ['users'] });
      notifications.show({
        color: 'teal',
        message: variables.active ? 'המשתמש הופעל.' : 'המשתמש הושבת וכל החיבורים שלו נותקו.',
      });
    },
    onError: (error) => {
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'עדכון המשתמש נכשל.',
      });
    },
  });

  return (
    <Stack gap="lg">
      <Group justify="space-between">
        <Title order={2}>משתמשים</Title>
        <Button leftSection={<IconUserPlus size={18} />} onClick={() => setCreateOpen(true)}>
          משתמש חדש
        </Button>
      </Group>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={640}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>שם</Table.Th>
                <Table.Th>דוא״ל</Table.Th>
                <Table.Th>תפקידים</Table.Th>
                <Table.Th>כניסה אחרונה</Table.Th>
                <Table.Th>פעיל</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(usersQuery.data?.users ?? []).map((user) => (
                <Table.Tr key={user.id}>
                  <Table.Td>
                    <Group gap="xs">
                      <Text>{user.display_name}</Text>
                      {user.must_change_password && (
                        <Badge size="xs" color="orange" variant="light">
                          סיסמה זמנית
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td className="wrap-anywhere" dir="ltr">
                    {user.email}
                  </Table.Td>
                  <Table.Td>
                    <Group gap={4}>
                      {user.roles.map((role: Role) => (
                        <Badge key={role} variant="light">
                          {ROLE_LABELS[role]}
                        </Badge>
                      ))}
                    </Group>
                  </Table.Td>
                  <Table.Td className="numeric">{formatDateTime(user.last_login_at)}</Table.Td>
                  <Table.Td>
                    <Switch
                      checked={user.active}
                      aria-label={user.active ? 'השבתת משתמש' : 'הפעלת משתמש'}
                      // Deactivating yourself would lock you out of the session
                      // you are using; the server refuses it too.
                      disabled={user.id === currentUser?.id || activeMutation.isPending}
                      onChange={(event) =>
                        activeMutation.mutate({ id: user.id, active: event.currentTarget.checked })
                      }
                    />
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Card>

      <Modal opened={createOpen} onClose={() => setCreateOpen(false)} title="משתמש חדש" centered>
        <form onSubmit={handleSubmit((values) => createMutation.mutate(values))} noValidate>
          <Stack gap="md">
            <TextInput
              label="שם"
              withAsterisk
              error={formState.errors.display_name?.message}
              {...register('display_name')}
            />
            <TextInput
              label="דוא״ל"
              withAsterisk
              dir="ltr"
              inputMode="email"
              error={formState.errors.email?.message}
              {...register('email')}
            />
            <PasswordInput
              label="סיסמה ראשונית"
              withAsterisk
              description={`לפחות ${MIN_LENGTH} תווים. המשתמש יידרש להחליף אותה בכניסה הראשונה.`}
              error={formState.errors.password?.message}
              {...register('password')}
            />
            <MultiSelect
              label="תפקידים"
              withAsterisk
              data={roleOptions}
              value={watch('roles')}
              onChange={(value) => setValue('roles', value, { shouldValidate: true })}
              error={formState.errors.roles?.message}
            />

            <Group justify="flex-end">
              <Button variant="default" onClick={() => setCreateOpen(false)}>
                ביטול
              </Button>
              <Button type="submit" loading={createMutation.isPending}>
                יצירה
              </Button>
            </Group>
          </Stack>
        </form>
      </Modal>
    </Stack>
  );
}
