import { createTheme, type MantineThemeOverride } from '@mantine/core';

/**
 * Mobile-first Hebrew theme. Touch targets are large by default (plan.md 7),
 * and the font stack prefers faces that carry a complete Hebrew glyph set.
 */
export const theme: MantineThemeOverride = createTheme({
  fontFamily: '"Assistant", "Heebo", "Segoe UI", system-ui, -apple-system, sans-serif',
  headings: {
    fontFamily: '"Assistant", "Heebo", "Segoe UI", system-ui, sans-serif',
    fontWeight: '600',
  },
  primaryColor: 'teal',
  defaultRadius: 'md',
  components: {
    Button: { defaultProps: { size: 'md' } },
    TextInput: { defaultProps: { size: 'md' } },
    PasswordInput: { defaultProps: { size: 'md' } },
    Textarea: { defaultProps: { size: 'md' } },
    Select: { defaultProps: { size: 'md' } },
  },
});
