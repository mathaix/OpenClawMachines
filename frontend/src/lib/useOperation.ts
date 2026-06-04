import { useCallback, useRef, useState } from "react";

export interface UseOperationOptions {
  /** If set, auto-toasts on error with this title. */
  errorTitle?: string;
  /** If set, auto-toasts on success with this title. */
  successTitle?: string;
}

export interface UseOperationReturn<T> {
  /** Run an async operation. No-op if already running. Returns result or undefined if skipped/errored. */
  execute: (fn: () => Promise<T>) => Promise<T | undefined>;
  /** True while an operation is in flight. */
  loading: boolean;
  /** The error from the last failed execution, or null. Cleared on next success. */
  error: Error | null;
}

export function useOperation<T>(
  options?: UseOperationOptions,
): UseOperationReturn<T> {
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const runningRef = useRef(false);
  const optionsRef = useRef(options);
  optionsRef.current = options;

  const execute = useCallback(
    async (fn: () => Promise<T>): Promise<T | undefined> => {
      if (runningRef.current) return undefined;
      runningRef.current = true;
      setLoading(true);
      setError(null);
      try {
        const result = await fn();
        setLoading(false);
        runningRef.current = false;
        return result;
      } catch (err) {
        const error = err instanceof Error ? err : new Error(String(err));
        setError(error);
        setLoading(false);
        runningRef.current = false;
        return undefined;
      }
    },
    [],
  );

  return { execute, loading, error };
}
