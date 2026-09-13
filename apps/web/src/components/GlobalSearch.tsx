import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Badge, Group, Loader, Modal, Stack, Text, TextInput, UnstyledButton } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { IconSearch } from '@tabler/icons-react';

import { api } from '@/api/client';
import type { SearchHit, SearchResults } from '@/api/types';
import { useDebounced } from '@/hooks/useDebounced';

const MIN_QUERY_LENGTH = 2;

const KIND_LABELS: Record<SearchHit['kind'], string> = {
  CUSTOMER: 'לקוח',
  SERVICE: 'שירות',
  ACTIVITY: 'פעילות',
};

const KIND_PATHS: Record<SearchHit['kind'], string> = {
  CUSTOMER: '/customers',
  SERVICE: '/services',
  ACTIVITY: '/activities',
};

/** One search box over customers, services and activities (plan.md 7). */
export function GlobalSearch({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const [query, setQuery] = useState('');
  const debounced = useDebounced(query, 250);
  const navigate = useNavigate();

  // Start each visit with an empty box rather than a stale query.
  useEffect(() => {
    if (opened) setQuery('');
  }, [opened]);

  const results = useQuery({
    queryKey: ['search', debounced],
    queryFn: () => api.get<SearchResults>(`/search/?q=${encodeURIComponent(debounced)}`),
    enabled: debounced.trim().length >= MIN_QUERY_LENGTH,
  });

  const hits: SearchHit[] = results.data
    ? [...results.data.customers, ...results.data.services, ...results.data.activities]
    : [];

  const open = (hit: SearchHit) => {
    onClose();
    navigate(hit.kind === 'CUSTOMER' ? `/customers/${hit.id}` : KIND_PATHS[hit.kind]);
  };

  return (
    <Modal opened={opened} onClose={onClose} title="חיפוש" size="lg" centered>
      <Stack gap="sm">
        <TextInput
          data-autofocus
          placeholder="שם לקוח, טלפון, דוא״ל, שירות או פעילות"
          leftSection={<IconSearch size={18} />}
          rightSection={results.isFetching ? <Loader size="xs" /> : null}
          value={query}
          onChange={(event) => setQuery(event.currentTarget.value)}
        />

        {debounced.trim().length > 0 && debounced.trim().length < MIN_QUERY_LENGTH && (
          <Text size="sm" c="dimmed">
            יש להקליד לפחות {MIN_QUERY_LENGTH} תווים.
          </Text>
        )}

        {debounced.trim().length >= MIN_QUERY_LENGTH && !results.isFetching && hits.length === 0 && (
          <Text size="sm" c="dimmed">
            לא נמצאו תוצאות.
          </Text>
        )}

        <Stack gap={4}>
          {hits.map((hit) => (
            <UnstyledButton
              key={`${hit.kind}-${hit.id}`}
              onClick={() => open(hit)}
              p="xs"
              style={{ borderRadius: 8 }}
            >
              <Group justify="space-between" wrap="nowrap">
                <div style={{ minWidth: 0 }}>
                  <Text truncate>{hit.title}</Text>
                  {hit.subtitle && (
                    <Text size="xs" c="dimmed" truncate>
                      {hit.subtitle}
                    </Text>
                  )}
                </div>
                <Badge variant="light" size="sm">
                  {KIND_LABELS[hit.kind]}
                </Badge>
              </Group>
            </UnstyledButton>
          ))}
        </Stack>
      </Stack>
    </Modal>
  );
}
