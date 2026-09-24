import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  Alert,
  Badge,
  Button,
  Card,
  Code,
  Grid,
  Group,
  Modal,
  Skeleton,
  Stack,
  Table,
  Text,
  Textarea,
  Title,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  IconAlertTriangle,
  IconArrowRight,
  IconBan,
  IconBrandWhatsapp,
  IconDownload,
  IconEdit,
  IconEye,
  IconFileCheck,
  IconMail,
  IconReceipt,
  IconTrash,
} from '@tabler/icons-react';

import { API_BASE, ApiError, api } from '@/api/client';
import type {
  DeliveryAttempt,
  DeliveryStatus,
  InvoiceDocument,
  OutstandingDocument,
  WhatsAppShare,
} from '@/api/types';
import { PaymentFormModal } from '@/components/PaymentFormModal';
import { useSession } from '@/hooks/useSession';
import { formatDate, formatDateTime, formatMoney } from '@/lib/format';
import { DOCUMENT_STATE_COLORS, DOCUMENT_STATE_LABELS, DOCUMENT_TYPE_LABELS } from '@/lib/labels';
import { milliToQuantity } from '@/lib/money';

function Field({ label, value }: { label: string; value: string }) {
  return (
    <Grid.Col span={{ base: 12, sm: 6, md: 3 }}>
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text className="wrap-anywhere">{value || '—'}</Text>
    </Grid.Col>
  );
}

export function DocumentDetailPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelReason, setCancelReason] = useState('');
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [share, setShare] = useState<WhatsAppShare | null>(null);
  const [receiptOpen, setReceiptOpen] = useState(false);

  const documentQuery = useQuery({
    queryKey: ['document', id],
    queryFn: () => api.get<InvoiceDocument>(`/documents/${id}`),
    enabled: Boolean(id),
  });

  // Only issued requests and invoices are owed money. A receipt settles them;
  // it is not itself settleable.
  const settleable =
    documentQuery.data?.state === 'ISSUED' && documentQuery.data.document_type !== 'RECEIPT';

  // The same query key the payment form uses, so recording a payment there
  // refreshes the balance shown here.
  const outstanding = useQuery({
    queryKey: ['outstanding', documentQuery.data?.customer_id],
    queryFn: () =>
      api.get<{ documents: OutstandingDocument[] }>(
        `/customers/${documentQuery.data?.customer_id}/outstanding`,
      ),
    enabled: settleable,
  });
  const outstandingAgorot = settleable
    ? (outstanding.data?.documents.find((item) => item.id === id)?.outstanding_agorot ?? 0)
    : 0;

  const deliveryStatus = useQuery({
    queryKey: ['delivery-status'],
    queryFn: () => api.get<DeliveryStatus>('/delivery/status'),
    staleTime: 5 * 60_000,
  });

  const deliveries = useQuery({
    queryKey: ['deliveries', id],
    queryFn: () => api.get<{ attempts: DeliveryAttempt[] }>(`/documents/${id}/deliveries`),
    enabled: Boolean(id),
  });

  const sendMutation = useMutation({
    mutationFn: () => api.post<DeliveryAttempt>(`/documents/${id}/send`, {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['deliveries', id] });
      notifications.show({
        color: 'teal',
        message: 'השליחה נכנסה לתור. המסמך עצמו נשמר ללא תלות בהצלחת השליחה.',
      });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        autoClose: 8000,
        message: error instanceof ApiError ? error.message : 'השליחה נכשלה.',
      }),
  });

  const whatsappMutation = useMutation({
    mutationFn: () => api.post<WhatsAppShare>(`/documents/${id}/whatsapp`),
    onSuccess: (prepared) => {
      setShare(prepared);
      queryClient.invalidateQueries({ queryKey: ['deliveries', id] });
      // Opening from the click handler keeps it inside the user gesture, so
      // the popup blocker leaves it alone.
      window.open(prepared.share_url, '_blank', 'noopener');
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'הכנת השיתוף נכשלה.',
      }),
  });

  const issueMutation = useMutation({
    mutationFn: () =>
      api.post<InvoiceDocument>(`/documents/${id}/issue`, undefined, {
        // A retry after a timeout must return the original document rather
        // than issue a second one (plan.md 10).
        'Idempotency-Key': `issue-${id}`,
      }),
    onSuccess: (issued) => {
      queryClient.setQueryData(['document', id], issued);
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      notifications.show({ color: 'teal', message: `המסמך הופק: ${issued.full_number}` });
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        autoClose: 8000,
        message: error instanceof ApiError ? error.message : 'הפקת המסמך נכשלה.',
      }),
  });

  const cancelMutation = useMutation({
    mutationFn: () => api.post<InvoiceDocument>(`/documents/${id}/cancel`, { reason: cancelReason }),
    onSuccess: (cancelled) => {
      queryClient.setQueryData(['document', id], cancelled);
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      setCancelOpen(false);
      setCancelReason('');
      notifications.show({ color: 'orange', message: 'המסמך בוטל.' });
    },
    onError: (error) => {
      if (error instanceof ApiError) {
        setCancelError(error.fieldErrors().reason ?? error.message);
        return;
      }
      setCancelError('ביטול המסמך נכשל.');
    },
  });

  const deleteMutation = useMutation({
    mutationFn: () => api.delete<void>(`/documents/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['documents'] });
      notifications.show({ color: 'teal', message: 'הטיוטה נמחקה.' });
      navigate('/documents');
    },
    onError: (error) =>
      notifications.show({
        color: 'red',
        message: error instanceof ApiError ? error.message : 'מחיקת הטיוטה נכשלה.',
      }),
  });

  if (documentQuery.isLoading) return <Skeleton height={420} />;

  if (documentQuery.isError || !documentQuery.data) {
    return (
      <Stack gap="md">
        <Title order={2}>המסמך לא נמצא</Title>
        <Button component={Link} to="/documents" leftSection={<IconArrowRight size={18} />} w="fit-content">
          חזרה לרשימת המסמכים
        </Button>
      </Stack>
    );
  }

  const document = documentQuery.data;
  const isDraft = document.state === 'DRAFT';
  const isIssued = document.state === 'ISSUED';

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <Group gap="sm" wrap="wrap">
          <Button component={Link} to="/documents" variant="subtle" leftSection={<IconArrowRight size={18} />}>
            מסמכים
          </Button>
          <Title order={2}>
            {DOCUMENT_TYPE_LABELS[document.document_type]}
            {document.full_number && <span className="numeric"> {document.full_number}</span>}
          </Title>
          <Badge variant="light" color={DOCUMENT_STATE_COLORS[document.state]}>
            {DOCUMENT_STATE_LABELS[document.state]}
          </Badge>
          {document.test_mode && (
            <Badge color="orange" variant="light">
              מסמך בדיקה
            </Badge>
          )}
          {settleable && outstanding.isSuccess && (
            <Badge variant="light" color={outstandingAgorot > 0 ? 'yellow' : 'teal'}>
              {outstandingAgorot > 0 ? (
                <>
                  יתרה לתשלום <span className="numeric">{formatMoney(outstandingAgorot)}</span>
                </>
              ) : (
                'שולם במלואו'
              )}
            </Badge>
          )}
        </Group>

        <Group gap="sm">
          {/* An issued document never shows a generic edit or delete action
              (plan.md 7). */}
          {isDraft && (
            <Button
              component="a"
              href={`${API_BASE}/documents/${id}/preview`}
              target="_blank"
              rel="noopener"
              variant="default"
              leftSection={<IconEye size={18} />}
            >
              תצוגה מקדימה
            </Button>
          )}
          {canEdit && isDraft && (
            <>
              <Button
                variant="default"
                leftSection={<IconEdit size={18} />}
                onClick={() => navigate(`/documents/${id}/edit`)}
              >
                עריכה
              </Button>
              <Button
                variant="default"
                color="red"
                leftSection={<IconTrash size={18} />}
                loading={deleteMutation.isPending}
                onClick={() => deleteMutation.mutate()}
              >
                מחיקה
              </Button>
              <Button
                leftSection={<IconFileCheck size={18} />}
                loading={issueMutation.isPending}
                onClick={() => issueMutation.mutate()}
              >
                הפקת מסמך
              </Button>
            </>
          )}

          {canEdit && outstandingAgorot > 0 && (
            <Button leftSection={<IconReceipt size={18} />} onClick={() => setReceiptOpen(true)}>
              הפקת קבלה
            </Button>
          )}
          {isIssued && (
            <Button
              component="a"
              href={`${API_BASE}/documents/${id}/pdf`}
              leftSection={<IconDownload size={18} />}
            >
              הורדת PDF
            </Button>
          )}
          {canEdit && isIssued && (
            <Button
              variant="default"
              leftSection={<IconBrandWhatsapp size={18} />}
              loading={whatsappMutation.isPending}
              onClick={() => whatsappMutation.mutate()}
            >
              שיתוף בוואטסאפ
            </Button>
          )}
          {canEdit && isIssued && deliveryStatus.data?.email_available && (
            <Button
              variant="default"
              leftSection={<IconMail size={18} />}
              loading={sendMutation.isPending}
              onClick={() => sendMutation.mutate()}
            >
              שליחה בדוא״ל
            </Button>
          )}
          {canEdit && isIssued && (
            <Button
              variant="default"
              color="red"
              leftSection={<IconBan size={18} />}
              onClick={() => setCancelOpen(true)}
            >
              ביטול מסמך
            </Button>
          )}
        </Group>
      </Group>

      {isDraft && (
        <Alert color="blue" variant="light">
          זו טיוטה. אין לה מספר רשמי והיא ניתנת לעריכה. לאחר ההפקה לא ניתן יהיה לשנות או למחוק אותה —
          מומלץ לבדוק בתצוגה מקדימה לפני ההפקה.
        </Alert>
      )}

      {document.test_mode && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} title="מסמך לבדיקה">
          המסמך הופק בזמן שבדיקת ההתאמה הרגולטורית טרם הושלמה. הוא ממוספר בסדרת בדיקה נפרדת, נושא סימון
          מים, ואינו מסמך רשמי.
        </Alert>
      )}

      {document.state === 'CANCELLED' && (
        <Alert color="red" variant="light" title="מסמך מבוטל">
          <Stack gap={4}>
            <Text size="sm">בוטל ב־{formatDateTime(document.cancelled_at)}</Text>
            {document.cancellation_reason && <Text size="sm">סיבה: {document.cancellation_reason}</Text>}
            <Text size="xs" c="dimmed">
              המספר, הסכומים וקובץ ה־PDF המקוריים נשמרים כפי שהיו.
            </Text>
          </Stack>
        </Alert>
      )}

      <Card withBorder padding="lg">
        <Grid>
          <Field label="לקוח" value={document.customer_name} />
          <Field label="תאריך המסמך" value={formatDate(`${document.document_date}T00:00:00Z`)} />
          <Field
            label="לתשלום עד"
            value={document.due_date ? formatDate(`${document.due_date}T00:00:00Z`) : ''}
          />
          <Field label="פעילות" value={document.activity_name ?? ''} />
          {isIssued && <Field label="הופק ב" value={formatDateTime(document.issued_at)} />}
          {document.required_wording && <Field label="נוסח חובה" value={document.required_wording} />}
        </Grid>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={640}>
          <Table>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>תיאור</Table.Th>
                <Table.Th>יחידה</Table.Th>
                <Table.Th>כמות</Table.Th>
                <Table.Th>מחיר ליחידה</Table.Th>
                <Table.Th>סה״כ</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {document.lines.map((line) => (
                <Table.Tr key={line.id}>
                  <Table.Td>{line.description}</Table.Td>
                  <Table.Td>{line.unit || '—'}</Table.Td>
                  <Table.Td className="numeric">{milliToQuantity(line.quantity_milli)}</Table.Td>
                  <Table.Td className="numeric">{formatMoney(line.unit_price_agorot)}</Table.Td>
                  <Table.Td className="numeric">{formatMoney(line.line_total_agorot)}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        <Group justify="flex-start" p="md" gap="xl">
          <div>
            <Text size="sm" c="dimmed">
              סכום ביניים
            </Text>
            <Text className="numeric">{formatMoney(document.subtotal_agorot)}</Text>
          </div>
          {document.vat_agorot !== 0 && (
            <div>
              <Text size="sm" c="dimmed">
                מע״מ
              </Text>
              <Text className="numeric">{formatMoney(document.vat_agorot)}</Text>
            </div>
          )}
          <div>
            <Text size="sm" c="dimmed">
              סה״כ לתשלום
            </Text>
            <Text fw={700} size="lg" className="numeric">
              {formatMoney(document.total_agorot)}
            </Text>
          </div>
        </Group>
      </Card>

      {document.notes && (
        <Card withBorder padding="lg">
          <Text size="sm" c="dimmed" mb={4}>
            הערות
          </Text>
          <Text style={{ whiteSpace: 'pre-wrap' }}>{document.notes}</Text>
        </Card>
      )}

      {isIssued && document.pdf_sha256 && (
        <Card withBorder padding="lg">
          <Text size="sm" c="dimmed" mb={4}>
            חתימת הקובץ השמור (SHA-256)
          </Text>
          <Code block dir="ltr" style={{ overflowWrap: 'anywhere' }}>
            {document.pdf_sha256}
          </Code>
          <Text size="xs" c="dimmed" mt={6}>
            תבנית {document.template_version} · הקובץ נבדק מול החתימה בכל הורדה.
          </Text>
        </Card>
      )}

      {share && (
        <Card withBorder padding="lg">
          <Title order={4} mb="xs">
            הודעת וואטסאפ מוכנה
          </Title>
          <Text size="sm" c="dimmed" mb="xs">
            נפתח חלון וואטסאפ עם ההודעה. אם החלון נחסם, אפשר להעתיק את ההודעה ולצרף את הקובץ ידנית.
          </Text>
          <Code block style={{ whiteSpace: 'pre-wrap' }}>
            {share.message}
          </Code>
          <Group mt="sm">
            <Button component="a" href={share.share_url} target="_blank" rel="noopener" variant="light">
              פתיחת וואטסאפ
            </Button>
            <Button component="a" href={`${API_BASE}/documents/${id}/pdf`} variant="subtle">
              הורדת הקובץ לצירוף
            </Button>
          </Group>
        </Card>
      )}

      {isIssued && (deliveries.data?.attempts.length ?? 0) > 0 && (
        <Card withBorder padding={0}>
          <Title order={4} p="md" pb="xs">
            היסטוריית שליחה
          </Title>
          <Table>
            <Table.Tbody>
              {(deliveries.data?.attempts ?? []).map((attempt) => (
                <Table.Tr key={attempt.id}>
                  <Table.Td className="numeric">{formatDateTime(attempt.attempted_at)}</Table.Td>
                  <Table.Td>{attempt.channel === 'EMAIL' ? 'דוא״ל' : 'וואטסאפ'}</Table.Td>
                  <Table.Td className="wrap-anywhere">{attempt.recipient}</Table.Td>
                  <Table.Td>
                    <Badge
                      variant="light"
                      color={
                        attempt.state === 'SENT' ? 'teal' : attempt.state === 'FAILED' ? 'red' : 'gray'
                      }
                    >
                      {attempt.state === 'SENT'
                        ? 'נשלח'
                        : attempt.state === 'FAILED'
                          ? 'נכשל'
                          : 'בתור'}
                    </Badge>
                    {attempt.error && (
                      <Text size="xs" c="dimmed" className="wrap-anywhere">
                        {attempt.error}
                      </Text>
                    )}
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Card>
      )}

      <PaymentFormModal
        opened={receiptOpen}
        onClose={() => setReceiptOpen(false)}
        customerId={document.customer_id}
        settleDocument={{ id: document.id, activityId: document.activity_id }}
      />

      <Modal
        opened={cancelOpen}
        onClose={() => setCancelOpen(false)}
        title="ביטול מסמך"
        centered
      >
        <Stack gap="md">
          <Alert color="orange" variant="light">
            הביטול אינו מוחק את המסמך. המספר, הסכומים וקובץ ה־PDF נשמרים, והביטול נרשם ביומן הפעילות.
          </Alert>
          <Textarea
            label="סיבת הביטול"
            withAsterisk
            autosize
            minRows={2}
            value={cancelReason}
            error={cancelError}
            onChange={(event) => {
              setCancelReason(event.currentTarget.value);
              setCancelError(null);
            }}
          />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setCancelOpen(false)}>
              חזרה
            </Button>
            <Button color="red" loading={cancelMutation.isPending} onClick={() => cancelMutation.mutate()}>
              ביטול המסמך
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
