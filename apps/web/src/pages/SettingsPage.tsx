import { useEffect, useRef, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Code,
  FileButton,
  Grid,
  Group,
  Image,
  List,
  Skeleton,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';

import { API_BASE, ApiError, api } from '@/api/client';
import type { BusinessProfile, ComplianceStatus, RegulatoryEntry } from '@/api/types';
import { formatDate, formatDateTime } from '@/lib/format';
import { DOCUMENT_TYPE_LABELS, gateLabel } from '@/lib/labels';
import { BackupsCard } from '@/components/BackupsCard';
import { useSession } from '@/hooks/useSession';

const schema = z.object({
  legal_name: z.string().min(1, 'יש להזין שם עסק רשמי'),
  display_name: z.string(),
  business_number: z.string(),
  address: z.string(),
  phone: z.string(),
  email: z.string().email('כתובת הדוא״ל אינה תקינה').or(z.literal('')),
  website: z.string(),
  bank_details: z.string(),
  footer_note: z.string(),
});

type FormValues = z.infer<typeof schema>;

export function SettingsPage() {
  const { can } = useSession();
  const queryClient = useQueryClient();
  const canEdit = can('OWNER');

  const profileQuery = useQuery({
    queryKey: ['business-profile'],
    queryFn: () => api.get<BusinessProfile>('/settings/business-profile'),
  });

  const complianceQuery = useQuery({
    queryKey: ['compliance'],
    queryFn: () => api.get<ComplianceStatus>('/compliance/'),
  });

  const regulatoryQuery = useQuery({
    queryKey: ['regulatory'],
    queryFn: () => api.get<{ entries: RegulatoryEntry[] }>('/settings/regulatory'),
    enabled: canEdit,
  });

  const [logoError, setLogoError] = useState<string | null>(null);
  // Bumped after an upload so the browser fetches the new image instead of the
  // one it already cached.
  const [logoVersion, setLogoVersion] = useState(0);
  const resetLogoRef = useRef<() => void>(null);

  const logoMutation = useMutation({
    mutationFn: async (file: File) => {
      const body = new FormData();
      body.append('file', file);

      const csrf = document.cookie.match(/(?:^|; )csrf_token=([^;]*)/)?.[1];
      const response = await fetch(`${API_BASE}/settings/business-profile/logo`, {
        method: 'POST',
        credentials: 'same-origin',
        headers: csrf ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {},
        body,
      });
      if (!response.ok) {
        const payload = await response.json().catch(() => null);
        throw new Error(
          payload?.error?.details?.file ?? payload?.error?.message ?? 'העלאת הלוגו נכשלה.',
        );
      }
      return (await response.json()) as BusinessProfile;
    },
    onSuccess: (saved) => {
      queryClient.setQueryData(['business-profile'], saved);
      setLogoVersion((version) => version + 1);
      setLogoError(null);
      resetLogoRef.current?.();
      notifications.show({ color: 'teal', message: 'הלוגו עודכן. הוא יופיע במסמכים שיופקו מעכשיו.' });
    },
    onError: (error) => setLogoError(error instanceof Error ? error.message : 'העלאת הלוגו נכשלה.'),
  });

  const removeLogoMutation = useMutation({
    mutationFn: () => api.delete<BusinessProfile>('/settings/business-profile/logo'),
    onSuccess: (saved) => {
      queryClient.setQueryData(['business-profile'], saved);
      setLogoVersion((version) => version + 1);
      notifications.show({ color: 'teal', message: 'הלוגו הוסר ממסמכים עתידיים.' });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'הסרת הלוגו נכשלה.',
      }),
  });

  const { register, handleSubmit, reset, setError, formState } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      legal_name: '',
      display_name: '',
      business_number: '',
      address: '',
      phone: '',
      email: '',
      website: '',
      bank_details: '',
      footer_note: '',
    },
  });

  useEffect(() => {
    if (profileQuery.data) {
      const {
        updated_at: _updatedAt,
        has_logo: _hasLogo,
        logo_content_type: _logoType,
        logo_sha256: _logoSHA,
        logo_byte_size: _logoSize,
        logo_uploaded_at: _logoAt,
        ...values
      } = profileQuery.data;
      reset(values);
    }
  }, [profileQuery.data, reset]);

  const saveMutation = useMutation({
    mutationFn: (values: FormValues) =>
      api.put<BusinessProfile>('/settings/business-profile', { ...values, updated_at: new Date().toISOString() }),
    onSuccess: (saved) => {
      queryClient.setQueryData(['business-profile'], saved);
      notifications.show({ color: 'teal', message: 'פרטי העסק נשמרו.' });
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        const fields = error.fieldErrors();
        for (const [field, message] of Object.entries(fields)) {
          if (field in schema.shape) setError(field as keyof FormValues, { message });
        }
        notifications.show({ color: 'red', message: error.message });
        return;
      }
      notifications.show({ color: 'red', message: 'שמירת ההגדרות נכשלה.' });
    },
  });

  return (
    <Stack gap="lg">
      <Title order={2}>הגדרות</Title>

      <Card withBorder padding="lg">
        <Title order={4} mb="sm">
          פרטי העסק
        </Title>
        <Text size="sm" c="dimmed" mb="md">
          הפרטים האלה מוטבעים בכל מסמך שמופק. שינוי כאן אינו משנה מסמכים שכבר הופקו.
        </Text>

        {profileQuery.isLoading ? (
          <Skeleton height={280} />
        ) : (
          <form onSubmit={handleSubmit((values) => saveMutation.mutate(values))} noValidate>
            <Grid>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput
                  label="שם העסק הרשמי"
                  withAsterisk
                  disabled={!canEdit}
                  error={formState.errors.legal_name?.message}
                  {...register('legal_name')}
                />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="שם תצוגה" disabled={!canEdit} {...register('display_name')} />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput
                  label="מספר עוסק"
                  dir="ltr"
                  inputMode="numeric"
                  disabled={!canEdit}
                  {...register('business_number')}
                />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="טלפון" dir="ltr" inputMode="tel" disabled={!canEdit} {...register('phone')} />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput
                  label="דוא״ל"
                  dir="ltr"
                  inputMode="email"
                  disabled={!canEdit}
                  error={formState.errors.email?.message}
                  {...register('email')}
                />
              </Grid.Col>
              <Grid.Col span={{ base: 12, sm: 6 }}>
                <TextInput label="אתר" dir="ltr" disabled={!canEdit} {...register('website')} />
              </Grid.Col>
              <Grid.Col span={12}>
                <TextInput label="כתובת" disabled={!canEdit} {...register('address')} />
              </Grid.Col>
              <Grid.Col span={12}>
                <Textarea label="פרטי תשלום / חשבון בנק" autosize minRows={2} disabled={!canEdit} {...register('bank_details')} />
              </Grid.Col>
              <Grid.Col span={12}>
                <Textarea
                  label="הערת תחתית למסמכים"
                  autosize
                  minRows={2}
                  disabled={!canEdit}
                  {...register('footer_note')}
                />
              </Grid.Col>
            </Grid>

            <Group justify="space-between" mt="md">
              <Text size="xs" c="dimmed">
                עודכן לאחרונה: {formatDateTime(profileQuery.data?.updated_at)}
              </Text>
              {canEdit && (
                <Button type="submit" loading={saveMutation.isPending}>
                  שמירה
                </Button>
              )}
            </Group>
          </form>
        )}
      </Card>

      <Card withBorder padding="lg">
        <Title order={4} mb="xs">
          לוגו העסק
        </Title>
        <Text size="sm" c="dimmed" mb="md">
          הלוגו מודפס בראש כל מסמך. החלפת הלוגו משפיעה רק על מסמכים שיופקו מעכשיו — מסמכים שכבר הופקו
          ימשיכו להיראות בדיוק כפי שהופקו.
        </Text>

        {logoError && (
          <Alert color="red" variant="light" mb="md" withCloseButton onClose={() => setLogoError(null)}>
            {logoError}
          </Alert>
        )}

        <Group align="flex-start" gap="lg" wrap="wrap">
          <Card withBorder padding="sm" w={220} bg="gray.0">
            {profileQuery.data?.has_logo ? (
              <Image
                src={`${API_BASE}/settings/business-profile/logo?v=${logoVersion}`}
                alt="לוגו העסק"
                fit="contain"
                h={90}
              />
            ) : (
              <Text c="dimmed" size="sm" ta="center" py="lg">
                לא הועלה לוגו
              </Text>
            )}
          </Card>

          {canEdit && (
            <Stack gap="xs">
              <Group gap="sm">
                <FileButton
                  resetRef={resetLogoRef}
                  accept="image/png,image/jpeg,image/webp"
                  onChange={(file) => file && logoMutation.mutate(file)}
                >
                  {(props) => (
                    <Button {...props} loading={logoMutation.isPending}>
                      {profileQuery.data?.has_logo ? 'החלפת לוגו' : 'העלאת לוגו'}
                    </Button>
                  )}
                </FileButton>

                {profileQuery.data?.has_logo && (
                  <Button
                    variant="default"
                    color="red"
                    loading={removeLogoMutation.isPending}
                    onClick={() => removeLogoMutation.mutate()}
                  >
                    הסרה
                  </Button>
                )}
              </Group>

              <Text size="xs" c="dimmed">
                PNG, JPG או WEBP · עד 4MB · מומלץ רקע שקוף וגובה של לפחות 200 פיקסלים.
              </Text>
            </Stack>
          )}
        </Group>
      </Card>

      <Card withBorder padding="lg">
        <Title order={4} mb="sm">
          מצב רגולטורי
        </Title>
        {complianceQuery.isLoading ? (
          <Skeleton height={140} />
        ) : complianceQuery.data ? (
          <Stack gap="sm">
            <Group justify="space-between">
              <Text c="dimmed">סוג העסק</Text>
              <Text fw={600}>{complianceQuery.data.mode_hebrew}</Text>
            </Group>
            <Group justify="space-between">
              <Text c="dimmed">מסמכים מותרים</Text>
              <Text fw={600}>
                {complianceQuery.data.allowed_document_types
                  .map((type) => DOCUMENT_TYPE_LABELS[type] ?? type)
                  .join(' · ')}
              </Text>
            </Group>

            {complianceQuery.data.unresolved_gate_items.length > 0 ? (
              <Alert color="orange" variant="light" title="סעיפים פתוחים לבדיקת רואה חשבון">
                <List size="sm" spacing={4}>
                  {complianceQuery.data.unresolved_gate_items.map((item) => (
                    <List.Item key={item}>{gateLabel(item)}</List.Item>
                  ))}
                </List>
              </Alert>
            ) : (
              <Alert color="teal" variant="light">
                כל סעיפי הבדיקה הרגולטורית סומנו כמאושרים.
              </Alert>
            )}
          </Stack>
        ) : (
          <Text c="dimmed">לא ניתן לטעון את המצב הרגולטורי.</Text>
        )}
      </Card>

      {canEdit && <BackupsCard />}

      {canEdit && (
        <Card withBorder padding="lg">
          <Title order={4} mb="xs">
            הגדרות רגולטוריות
          </Title>
          <Text size="sm" c="dimmed" mb="md">
            כל ערך תקף מתאריך מסוים. ערכים קיימים אינם ניתנים לעריכה או למחיקה — שינוי מתבצע על ידי
            הוספת ערך חדש עם תאריך תחילת תוקף מאוחר יותר.
          </Text>

          {regulatoryQuery.isLoading ? (
            <Skeleton height={160} />
          ) : (
            <Table.ScrollContainer minWidth={520}>
              <Table striped withTableBorder>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>מפתח</Table.Th>
                    <Table.Th>בתוקף מ־</Table.Th>
                    <Table.Th>ערך</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {(regulatoryQuery.data?.entries ?? []).map((entry) => (
                    <Table.Tr key={`${entry.key}-${entry.effective_from}`}>
                      <Table.Td className="wrap-anywhere">{entry.key}</Table.Td>
                      <Table.Td className="numeric">{formatDate(`${entry.effective_from}T00:00:00Z`)}</Table.Td>
                      <Table.Td>
                        <Code block dir="ltr" style={{ maxWidth: 420, whiteSpace: 'pre-wrap' }}>
                          {JSON.stringify(entry.value, null, 2)}
                        </Code>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </Card>
      )}
    </Stack>
  );
}
