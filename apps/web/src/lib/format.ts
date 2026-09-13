/**
 * Hebrew formatting helpers. Money arrives from the API as integer agorot and
 * becomes a string only here, at the edge, so no arithmetic ever happens on a
 * formatted value.
 */

const TIMEZONE = 'Asia/Jerusalem';

const currencyFormatter = new Intl.NumberFormat('he-IL', {
  style: 'currency',
  currency: 'ILS',
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

const dateFormatter = new Intl.DateTimeFormat('he-IL', {
  timeZone: TIMEZONE,
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
});

const dateTimeFormatter = new Intl.DateTimeFormat('he-IL', {
  timeZone: TIMEZONE,
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
});

/** Formats integer agorot as ₪1,234.56. */
export function formatMoney(agorot: number): string {
  return currencyFormatter.format(agorot / 100);
}

/** Formats an RFC3339 timestamp as a date in Asia/Jerusalem. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return '—';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? '—' : dateFormatter.format(date);
}

/** Formats an RFC3339 timestamp as date and time in Asia/Jerusalem. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '—';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? '—' : dateTimeFormatter.format(date);
}

/** Percentage of an annual turnover ceiling, clamped to 0-100 for display. */
export function percentOf(amountAgorot: number, ceilingAgorot: number): number {
  if (ceilingAgorot <= 0) return 0;
  return Math.min(100, Math.max(0, (amountAgorot / ceilingAgorot) * 100));
}
