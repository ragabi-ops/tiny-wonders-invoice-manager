import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Group,
  Loader,
  Pagination,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
  Tooltip,
} from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconEdit, IconSearch, IconUserPlus } from '@tabler/icons-react';

import { api } from '@/api/client';
import type { Customer, CustomerPage } from '@/api/types';
import { CustomerFormModal } from '@/components/CustomerFormModal';
import { useDebounced } from '@/hooks/useDebounced';
import { useSession } from '@/hooks/useSession';
import { CUSTOMER_TYPE_LABELS, DELIVERY_LABELS } from '@/lib/labels';

const PAGE_SIZE = 25;

export function CustomersPage() {
  const navigate = useNavigate();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [query, setQuery] = useState('');
  const [page, setPage] = useState(1);
  const [includeArchived, setIncludeArchived] = useState(false);
  const [editing, setEditing] = useState<Customer | undefined>();
  const [formOpen, setFormOpen] = useState(false);

  const debouncedQuery = useDebounced(query, 300);
  const offset = (page - 1) * PAGE_SIZE;

  const customers = useQuery({
    queryKey: ['customers', debouncedQuery, includeArchived, offset],
    queryFn: () =>
      api.get<CustomerPage>(
        `/customers/?q=${encodeURIComponent(debouncedQuery)}` +
          `&include_archived=${includeArchived}&limit=${PAGE_SIZE}&offset=${offset}`,
      ),
  });

  const totalPages = Math.max(1, Math.ceil((customers.data?.total ?? 0) / PAGE_SIZE));

  const openCreate = () => {
    setEditing(undefined);
    setFormOpen(true);
  };

  const openEdit = (customer: Customer) => {
    setEditing(customer);
    setFormOpen(true);
  };

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <Title order={2}>לקוחות</Title>
        {canEdit && (
          <Button leftSection={<IconUserPlus size={18} />} onClick={openCreate}>
            לקוח חדש
          </Button>
        )}
      </Group>

      <Card withBorder padding="md">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <TextInput
            placeholder="חיפוש לפי שם, טלפון, דוא״ל או מספר עוסק"
            leftSection={<IconSearch size={18} />}
            rightSection={customers.isFetching ? <Loader size="xs" /> : null}
            value={query}
            onChange={(event) => {
              setQuery(event.currentTarget.value);
              setPage(1);
            }}
            style={{ flex: 1, minWidth: 260 }}
          />
          <Switch
            label="הצג גם ארכיון"
            checked={includeArchived}
            onChange={(event) => {
              setIncludeArchived(event.currentTarget.checked);
              setPage(1);
            }}
          />
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={720}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>שם</Table.Th>
                <Table.Th>סוג</Table.Th>
                <Table.Th>טלפון</Table.Th>
                <Table.Th>דוא״ל</Table.Th>
                <Table.Th>משלוח</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(customers.data?.customers ?? []).map((customer) => (
                <Table.Tr
                  key={customer.id}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/customers/${customer.id}`)}
                >
                  <Table.Td>
                    <Group gap="xs">
                      <Text>{customer.display_name}</Text>
                      {!customer.active && (
                        <Badge size="xs" color="gray" variant="light">
                          ארכיון
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>{CUSTOMER_TYPE_LABELS[customer.customer_type]}</Table.Td>
                  <Table.Td className="numeric">{customer.phone || '—'}</Table.Td>
                  <Table.Td className="wrap-anywhere" dir="ltr">
                    {customer.email || '—'}
                  </Table.Td>
                  <Table.Td>{DELIVERY_LABELS[customer.preferred_delivery]}</Table.Td>
                  <Table.Td>
                    {canEdit && (
                      <Tooltip label="עריכה">
                        <ActionIcon
                          variant="subtle"
                          aria-label="עריכת לקוח"
                          onClick={(event) => {
                            event.stopPropagation();
                            openEdit(customer);
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

        {!customers.isLoading && (customers.data?.customers.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            {debouncedQuery ? 'לא נמצאו לקוחות התואמים לחיפוש.' : 'עדיין אין לקוחות במערכת.'}
          </Text>
        )}
      </Card>

      {totalPages > 1 && (
        <Group justify="center">
          <Pagination total={totalPages} value={page} onChange={setPage} />
        </Group>
      )}

      <CustomerFormModal opened={formOpen} onClose={() => setFormOpen(false)} customer={editing} />
    </Stack>
  );
}
