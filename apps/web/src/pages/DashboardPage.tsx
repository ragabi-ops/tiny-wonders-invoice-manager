import { Link } from 'react-router-dom';
import {
  Alert,
  Badge,
  Card,
  Grid,
  List,
  Progress,
  Skeleton,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconAlertTriangle, IconInfoCircle } from '@tabler/icons-react';

import { api } from '@/api/client';
import type { BusinessProfile, ComplianceStatus, Dashboard } from '@/api/types';
import { formatDate, formatMoney, percentOf } from '@/lib/format';
import {
  DOCUMENT_STATE_COLORS,
  DOCUMENT_STATE_LABELS,
  DOCUMENT_TYPE_LABELS,
  PAYMENT_METHOD_LABELS,
  gateLabel,
} from '@/lib/labels';
import { useSession } from '@/hooks/useSession';

/** One headline figure. */
function Stat({ label, value, hint, color }: { label: string; value: string; hint?: string; color?: string }) {
  return (
    <Card withBorder padding="md" h="100%">
      <Text size="sm" c="dimmed">
        {label}
      </Text>
      <Text fw={700} size="xl" className="numeric" c={color}>
        {value}
      </Text>
      {hint && (
        <Text size="xs" c="dimmed">
          {hint}
        </Text>
      )}
    </Card>
  );
}

/**
 * The home screen (plan.md 7). Every figure here is computed from real
 * documents, payments and expenses — there are no placeholder tiles.
 */
export function DashboardPage() {
  const { user } = useSession();

  const dashboard = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => api.get<Dashboard>('/reports/dashboard'),
  });

  const compliance = useQuery({
    queryKey: ['compliance'],
    queryFn: () => api.get<ComplianceStatus>('/compliance/'),
  });

  const profile = useQuery({
    queryKey: ['business-profile'],
    queryFn: () => api.get<BusinessProfile>('/settings/business-profile'),
  });

  const businessIncomplete = profile.data && profile.data.legal_name.trim() === '';
  const data = dashboard.data;

  return (
    <Stack gap="lg">
      <div>
        <Title order={2}>שלום {user?.display_name}</Title>
        <Text c="dimmed">
          {data ? `נכון ל־${formatDate(`${data.business_date}T00:00:00Z`)}` : 'ברוכים הבאים למערכת.'}
        </Text>
      </div>

      {compliance.data && !compliance.data.real_issuance_enabled && (
        <Alert color="orange" variant="light" icon={<IconAlertTriangle size={18} />} title="המערכת במצב בדיקה">
          <Stack gap="xs">
            <Text size="sm">
              הפקת מסמכים רשמיים חסומה עד להשלמת בדיקת ההתאמה הרגולטורית מול רואה חשבון. מסמכים שמופקים
              כעת ממוספרים בסדרת בדיקה נפרדת.
            </Text>
            {compliance.data.unresolved_gate_items.length > 0 && (
              <List size="sm" spacing={4}>
                {compliance.data.unresolved_gate_items.map((item) => (
                  <List.Item key={item}>{gateLabel(item)}</List.Item>
                ))}
              </List>
            )}
          </Stack>
        </Alert>
      )}

      {businessIncomplete && (
        <Alert color="blue" variant="light" icon={<IconInfoCircle size={18} />} title="פרטי העסק חסרים">
          יש להשלים את פרטי העסק במסך ההגדרות. הפרטים מוטבעים בכל מסמך שמופק.
        </Alert>
      )}

      {(data?.failed_deliveries ?? 0) > 0 && (
        <Alert color="red" variant="light" icon={<IconAlertTriangle size={18} />} title="שליחות שנכשלו">
          {data?.failed_deliveries} שליחות נכשלו ולא הגיעו ללקוח. המסמכים עצמם תקינים ונשמרו — אפשר לשלוח
          שוב או לשתף בוואטסאפ.
        </Alert>
      )}

      {dashboard.isLoading ? (
        <Skeleton height={120} />
      ) : data ? (
        <Grid>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Stat label="הכנסות היום" value={formatMoney(data.revenue_today_agorot)} />
          </Grid.Col>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Stat label="הכנסות החודש" value={formatMoney(data.revenue_month_agorot)} />
          </Grid.Col>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Stat label="הכנסות השנה" value={formatMoney(data.revenue_year_agorot)} />
          </Grid.Col>
          <Grid.Col span={{ base: 6, md: 3 }}>
            <Stat label="הוצאות החודש" value={formatMoney(data.expenses_month_agorot)} />
          </Grid.Col>

          <Grid.Col span={{ base: 12, md: 6 }}>
            <Stat
              label="ממתין לתשלום"
              value={formatMoney(data.unpaid_amount_agorot)}
              hint={
                data.overdue_count > 0
                  ? `${data.unpaid_count} מסמכים, מהם ${data.overdue_count} באיחור`
                  : `${data.unpaid_count} מסמכים`
              }
              color={data.overdue_count > 0 ? 'red' : undefined}
            />
          </Grid.Col>
          <Grid.Col span={{ base: 12, md: 6 }}>
            <Card withBorder padding="md" h="100%">
              <Text size="sm" c="dimmed">
                תקרת מחזור שנתית
              </Text>
              {data.turnover.threshold_known ? (
                <Stack gap={6}>
                  <Text fw={700} size="xl" className="numeric">
                    {formatMoney(data.turnover.revenue_agorot)}
                  </Text>
                  <Progress
                    value={percentOf(data.turnover.revenue_agorot, data.turnover.threshold_agorot)}
                    color={data.turnover.percent_used > 85 ? 'red' : 'teal'}
                    aria-label="התקדמות מחזור שנתי"
                  />
                  <Text size="xs" c="dimmed">
                    מתוך {formatMoney(data.turnover.threshold_agorot)} · נותרו{' '}
                    {formatMoney(data.turnover.remaining_agorot)}
                  </Text>
                </Stack>
              ) : (
                <Text c="dimmed" size="sm">
                  התקרה לשנה זו אינה מוגדרת במערכת.
                </Text>
              )}
            </Card>
          </Grid.Col>
        </Grid>
      ) : (
        <Text c="dimmed">לא ניתן לטעון את נתוני המסך הראשי.</Text>
      )}

      <Grid>
        <Grid.Col span={{ base: 12, md: 6 }}>
          <Card withBorder padding={0} h="100%">
            <Title order={4} p="md" pb="xs">
              מסמכים אחרונים
            </Title>
            {(data?.recent_documents.length ?? 0) === 0 ? (
              <Text c="dimmed" ta="center" py="lg">
                עדיין אין מסמכים.
              </Text>
            ) : (
              <Table>
                <Table.Tbody>
                  {(data?.recent_documents ?? []).map((document) => (
                    <Table.Tr key={document.id}>
                      <Table.Td>
                        <Link to={`/documents/${document.id}`}>
                          {DOCUMENT_TYPE_LABELS[document.document_type]}
                        </Link>
                        <Text size="xs" c="dimmed" className="numeric">
                          {document.full_number || 'טיוטה'} · {document.customer_name}
                        </Text>
                      </Table.Td>
                      <Table.Td className="numeric">{formatMoney(document.total_agorot)}</Table.Td>
                      <Table.Td>
                        <Badge size="sm" variant="light" color={DOCUMENT_STATE_COLORS[document.state]}>
                          {DOCUMENT_STATE_LABELS[document.state]}
                        </Badge>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Card>
        </Grid.Col>

        <Grid.Col span={{ base: 12, md: 6 }}>
          <Card withBorder padding={0} h="100%">
            <Title order={4} p="md" pb="xs">
              תשלומים אחרונים
            </Title>
            {(data?.recent_payments.length ?? 0) === 0 ? (
              <Text c="dimmed" ta="center" py="lg">
                עדיין אין תשלומים.
              </Text>
            ) : (
              <Table>
                <Table.Tbody>
                  {(data?.recent_payments ?? []).map((payment) => (
                    <Table.Tr key={payment.id}>
                      <Table.Td>
                        <Text>{payment.customer_name}</Text>
                        <Text size="xs" c="dimmed">
                          {PAYMENT_METHOD_LABELS[payment.method]} ·{' '}
                          <span className="numeric">
                            {formatDate(`${payment.received_at}T00:00:00Z`)}
                          </span>
                        </Text>
                      </Table.Td>
                      <Table.Td className="numeric">{formatMoney(payment.amount_agorot)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Card>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
