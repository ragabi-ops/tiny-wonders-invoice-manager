import { useEffect, useState } from 'react';

/**
 * Returns value after it has stopped changing for `delay` milliseconds.
 * Search boxes use it so typing does not fire a request per keystroke.
 */
export function useDebounced<T>(value: T, delay = 300): T {
  const [debounced, setDebounced] = useState(value);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);

  return debounced;
}
