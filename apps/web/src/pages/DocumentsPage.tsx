import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import {
  Badge,
  Button,
  Card,
  Group,
  Loader,
  Pagination,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconFilePlus, IconSearch } from '@tabler/icons-react';

import { api } from '@/api/client';
import type { DocumentPage } from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';
import { useSession } from '@/hooks/useSession';
import { formatDate, formatMoney } from '@/lib/format';
import { DOCUMENT_STATE_COLORS, DOCUMENT_STATE_LABELS, DOCUMENT_TYPE_LABELS } from '@/lib/labels';

const PAGE_SIZE = 25;

export function DocumentsPage() {
  const navigate = useNavigate();
  const { can } = useSession();
  const canEdit = can('OPERATOR');

  const [query, setQuery] = useState('');
  const [state, setState] = useState<string | null>(null);
  const [documentType, setDocumentType] = useState<string | null>(null);
  const [page, setPage] = useState(1);

  const debouncedQuery = useDebounced(query, 300);
  const offset = (page - 1) * PAGE_SIZE;

  const documents = useQuery({
    queryKey: ['documents', debouncedQuery, state, documentType, offset],
    queryFn: () =>
      api.get<DocumentPage>(
        `/documents/?q=${encodeURIComponent(debouncedQuery)}` +
          `&state=${encodeURIComponent(state ?? '')}` +
          `&document_type=${encodeURIComponent(documentType ?? '')}` +
          `&limit=${PAGE_SIZE}&offset=${offset}`,
      ),
  });

  const totalPages = Math.max(1, Math.ceil((documents.data?.total ?? 0) / PAGE_SIZE));

  return (
    <Stack gap="lg">
      <Group justify="space-between" wrap="wrap">
        <Title order={2}>מסמכים</Title>
        {canEdit && (
          <Button component={Link} to="/documents/new" leftSection={<IconFilePlus size={18} />}>
            מסמך חדש
          </Button>
        )}
      </Group>

      <Card withBorder padding="md">
        <Group justify="space-between" wrap="wrap" gap="sm">
          <TextInput
            placeholder="חיפוש לפי שם לקוח או מספר מסמך"
            leftSection={<IconSearch size={18} />}
            rightSection={documents.isFetching ? <Loader size="xs" /> : null}
            value={query}
            onChange={(event) => {
              setQuery(event.currentTarget.value);
              setPage(1);
            }}
            style={{ flex: 1, minWidth: 240 }}
          />
          <Select
            placeholder="כל הסוגים"
            data={Object.entries(DOCUMENT_TYPE_LABELS).map(([value, label]) => ({ value, label }))}
            value={documentType}
            onChange={(value) => {
              setDocumentType(value);
              setPage(1);
            }}
            clearable
            w={180}
          />
          <Select
            placeholder="כל המצבים"
            data={Object.entries(DOCUMENT_STATE_LABELS).map(([value, label]) => ({ value, label }))}
            value={state}
            onChange={(value) => {
              setState(value);
              setPage(1);
            }}
            clearable
            w={150}
          />
        </Group>
      </Card>

      <Card withBorder padding={0}>
        <Table.ScrollContainer minWidth={760}>
          <Table striped highlightOnHover>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>מספר</Table.Th>
                <Table.Th>סוג</Table.Th>
                <Table.Th>לקוח</Table.Th>
                <Table.Th>תאריך</Table.Th>
                <Table.Th>סכום</Table.Th>
                <Table.Th>מצב</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {(documents.data?.documents ?? []).map((document) => (
                <Table.Tr
                  key={document.id}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/documents/${document.id}`)}
                >
                  <Table.Td className="numeric">
                    <Group gap={6} wrap="nowrap">
                      <Text fw={document.state === 'ISSUED' ? 600 : 400}>
                        {document.full_number || '—'}
                      </Text>
                      {document.test_mode && (
                        <Badge size="xs" color="orange" variant="light">
                          בדיקה
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>{DOCUMENT_TYPE_LABELS[document.document_type]}</Table.Td>
                  <Table.Td>{document.customer_name}</Table.Td>
                  <Table.Td className="numeric">{formatDate(`${document.document_date}T00:00:00Z`)}</Table.Td>
                  <Table.Td className="numeric">{formatMoney(document.total_agorot)}</Table.Td>
                  <Table.Td>
                    <Badge variant="light" color={DOCUMENT_STATE_COLORS[document.state]}>
                      {DOCUMENT_STATE_LABELS[document.state]}
                    </Badge>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>

        {!documents.isLoading && (documents.data?.documents.length ?? 0) === 0 && (
          <Text c="dimmed" ta="center" py="xl">
            {debouncedQuery || state || documentType
              ? 'לא נמצאו מסמכים התואמים לסינון.'
              : 'עדיין אין מסמכים במערכת.'}
          </Text>
        )}
      </Card>

      {totalPages > 1 && (
        <Group justify="center">
          <Pagination total={totalPages} value={page} onChange={setPage} />
        </Group>
      )}
    </Stack>
  );
}
