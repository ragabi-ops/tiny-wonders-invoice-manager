import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Alert, Button, Card, Center, PasswordInput, Stack, Text, Title } from '@mantine/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';

import { ApiError, api } from '@/api/client';
import { useSession } from '@/hooks/useSession';

// The minimum mirrors auth.MinPasswordLength; the server enforces it.
const MIN_LENGTH = 12;

const schema = z
  .object({
    current_password: z.string().min(1, 'יש להזין את הסיסמה הנוכחית'),
    new_password: z.string().min(MIN_LENGTH, `הסיסמה חייבת להכיל לפחות ${MIN_LENGTH} תווים`),
    confirm_password: z.string().min(1, 'יש לאשר את הסיסמה החדשה'),
  })
  .refine((values) => values.new_password === values.confirm_password, {
    path: ['confirm_password'],
    message: 'הסיסמאות אינן זהות',
  });

type FormValues = z.infer<typeof schema>;

export function ChangePasswordPage() {
  const navigate = useNavigate();
  const { user, logout } = useSession();
  const [serverError, setServerError] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    setError,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema) });

  const onSubmit = async (values: FormValues) => {
    setServerError(null);
    try {
      await api.post('/auth/change-password', {
        current_password: values.current_password,
        new_password: values.new_password,
      });
      // Changing a password ends every session, this one included.
      await logout();
      navigate('/login', { replace: true });
    } catch (error) {
      if (error instanceof ApiError) {
        const fields = error.fieldErrors();
        let handled = false;
        for (const [field, message] of Object.entries(fields)) {
          if (field === 'current_password' || field === 'new_password') {
            setError(field, { message });
            handled = true;
          }
        }
        if (!handled) setServerError(error.message);
        return;
      }
      setServerError('אירעה שגיאה. נסו שוב.');
    }
  };

  return (
    <Center mih="100vh" p="md">
      <Card withBorder shadow="sm" padding="lg" w="100%" maw={460}>
        <form onSubmit={handleSubmit(onSubmit)} noValidate>
          <Stack gap="md">
            <div>
              <Title order={3}>שינוי סיסמה</Title>
              {user?.must_change_password && (
                <Text size="sm" c="dimmed">
                  יש להחליף את הסיסמה הזמנית לפני שממשיכים.
                </Text>
              )}
            </div>

            {serverError && (
              <Alert color="red" variant="light" role="alert">
                {serverError}
              </Alert>
            )}

            <PasswordInput
              label="סיסמה נוכחית"
              autoComplete="current-password"
              error={errors.current_password?.message}
              {...register('current_password')}
            />
            <PasswordInput
              label="סיסמה חדשה"
              autoComplete="new-password"
              description={`לפחות ${MIN_LENGTH} תווים`}
              error={errors.new_password?.message}
              {...register('new_password')}
            />
            <PasswordInput
              label="אישור סיסמה חדשה"
              autoComplete="new-password"
              error={errors.confirm_password?.message}
              {...register('confirm_password')}
            />

            <Text size="xs" c="dimmed">
              לאחר השינוי תתבצע התנתקות מכל המכשירים ויהיה צורך להתחבר מחדש.
            </Text>

            <Button type="submit" loading={isSubmitting} fullWidth>
              שמירה
            </Button>
          </Stack>
        </form>
      </Card>
    </Center>
  );
}
