import { useState } from 'react';
import { Navigate, useNavigate } from 'react-router-dom';
import { Alert, Button, Card, Center, PasswordInput, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';

import { ApiError } from '@/api/client';
import { useSession } from '@/hooks/useSession';

const schema = z.object({
  email: z.string().min(1, 'יש להזין כתובת דוא״ל').email('כתובת הדוא״ל אינה תקינה'),
  password: z.string().min(1, 'יש להזין סיסמה'),
});

type FormValues = z.infer<typeof schema>;

export function LoginPage() {
  const { user, login } = useSession();
  const navigate = useNavigate();
  const [serverError, setServerError] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { email: '', password: '' } });

  if (user) return <Navigate to="/" replace />;

  const onSubmit = async (values: FormValues) => {
    setServerError(null);
    try {
      const loggedIn = await login(values.email, values.password);
      navigate(loggedIn.must_change_password ? '/change-password' : '/', { replace: true });
    } catch (error) {
      // The server's message is already Hebrew and deliberately vague about
      // which half of the credentials was wrong.
      setServerError(error instanceof ApiError ? error.message : 'אירעה שגיאה. נסו שוב.');
    }
  };

  return (
    <Center mih="100vh" p="md">
      <Card withBorder shadow="sm" padding="lg" w="100%" maw={420}>
        <form onSubmit={handleSubmit(onSubmit)} noValidate>
          <Stack gap="md">
            <div>
              <Title order={3}>כניסה למערכת</Title>
              <Text size="sm" c="dimmed">
                מערכת חשבוניות וקבלות
              </Text>
            </div>

            {serverError && (
              <Alert color="red" variant="light" role="alert">
                {serverError}
              </Alert>
            )}

            <TextInput
              label="דוא״ל"
              type="email"
              autoComplete="username"
              inputMode="email"
              dir="ltr"
              error={errors.email?.message}
              {...register('email')}
            />

            <PasswordInput
              label="סיסמה"
              autoComplete="current-password"
              error={errors.password?.message}
              {...register('password')}
            />

            <Button type="submit" loading={isSubmitting} fullWidth>
              כניסה
            </Button>
          </Stack>
        </form>
      </Card>
    </Center>
  );
}
