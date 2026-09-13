/**
 * The single HTTP entry point to the API.
 *
 * Every call sends cookies and, for unsafe methods, echoes the CSRF cookie in
 * the X-CSRF-Token header (SECURITY.md 3). Errors always arrive in one
 * envelope, and the message inside it is already Hebrew, so the UI shows it
 * as-is and never invents English text.
 */

export const API_BASE = '/api/v1';

export interface ApiErrorDetails {
  [field: string]: unknown;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details?: ApiErrorDetails;
  readonly requestId?: string;

  constructor(status: number, code: string, message: string, details?: ApiErrorDetails, requestId?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.requestId = requestId;
  }

  /** Field-level validation messages, keyed by field name. */
  fieldErrors(): Record<string, string> {
    const errors: Record<string, string> = {};
    for (const [field, message] of Object.entries(this.details ?? {})) {
      if (typeof message === 'string') errors[field] = message;
    }
    return errors;
  }
}

/** The last-resort message, used only when the server sent no envelope. */
const GENERIC_ERROR = 'אירעה שגיאה בלתי צפויה. נסו שוב.';

function readCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`));
  return match?.[1] ? decodeURIComponent(match[1]) : null;
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
  /** Extra headers, such as Idempotency-Key on a critical command. */
  headers?: Record<string, string>;
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const method = options.method ?? 'GET';
  const headers: Record<string, string> = { Accept: 'application/json' };

  if (options.body !== undefined) headers['Content-Type'] = 'application/json';

  if (method !== 'GET') {
    const csrf = readCookie('csrf_token');
    if (csrf) headers['X-CSRF-Token'] = csrf;
  }

  Object.assign(headers, options.headers ?? {});

  let response: Response;
  try {
    response = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      credentials: 'same-origin',
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
    });
  } catch (cause) {
    // A network failure has no envelope, so the message is written here.
    throw new ApiError(0, 'NETWORK_ERROR', 'אין חיבור לשרת. בדקו את החיבור ונסו שוב.', undefined, undefined);
  }

  if (response.status === 204) return undefined as T;

  const isJson = response.headers.get('Content-Type')?.includes('application/json');
  const payload = isJson ? await response.json().catch(() => null) : null;

  if (!response.ok) {
    const envelope = (payload as { error?: { code?: string; message?: string; details?: ApiErrorDetails; request_id?: string } } | null)?.error;
    throw new ApiError(
      response.status,
      envelope?.code ?? 'UNKNOWN',
      envelope?.message ?? GENERIC_ERROR,
      envelope?.details,
      envelope?.request_id,
    );
  }

  return payload as T;
}

export const api = {
  get: <T>(path: string, signal?: AbortSignal) => request<T>(path, { method: 'GET', signal }),
  post: <T>(path: string, body?: unknown, headers?: Record<string, string>) =>
    request<T>(path, { method: 'POST', body, headers }),
  put: <T>(path: string, body?: unknown) => request<T>(path, { method: 'PUT', body }),
  delete: <T>(path: string) => request<T>(path, { method: 'DELETE' }),
};
