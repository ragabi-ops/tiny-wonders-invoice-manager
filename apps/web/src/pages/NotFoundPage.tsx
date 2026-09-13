import { Button, Center, Stack, Text, Title } from '@mantine/core';
import { Link } from 'react-router-dom';

export function NotFoundPage() {
  return (
    <Center mih="60vh">
      <Stack align="center" gap="sm">
        <Title order={2}>הדף לא נמצא</Title>
        <Text c="dimmed">ייתכן שהכתובת שגויה או שהדף עדיין לא זמין.</Text>
        <Button component={Link} to="/">
          חזרה לדף הראשי
        </Button>
      </Stack>
    </Center>
  );
}
