import { useEffect, useRef, useState, useCallback } from "react";
import { getMachineToken } from "../lib/api";

interface MachineTokenState {
  token: string | null;
  hostname: string | null;
  loading: boolean;
  error: string | null;
}

const MAX_RETRIES = 5;

export function useMachineToken(accountId: number | undefined, machineId: string | undefined) {
  const [state, setState] = useState<MachineTokenState>({ token: null, hostname: null, loading: true, error: null });
  const tokenRef = useRef<string | null>(null);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retriesRef = useRef(0);
  const cancelledRef = useRef(false);

  const fetchToken = useCallback(async () => {
    if (!accountId || !machineId || cancelledRef.current) return;
    try {
      const res = await getMachineToken(accountId, machineId);
      if (cancelledRef.current) return;
      tokenRef.current = res.token;
      retriesRef.current = 0; // Reset on success
      setState({ token: res.token, hostname: res.hostname, loading: false, error: null });

      // Schedule refresh 60s before expiry
      const expiresAt = new Date(res.expires_at).getTime();
      const refreshIn = Math.max(expiresAt - Date.now() - 60000, 10000);
      timerRef.current = setTimeout(fetchToken, refreshIn);
    } catch (err) {
      if (cancelledRef.current) return;
      retriesRef.current += 1;
      const errorMsg = err instanceof Error ? err.message : "Token fetch failed";

      if (retriesRef.current >= MAX_RETRIES) {
        setState(prev => ({ ...prev, loading: false, error: `${errorMsg} (gave up after ${MAX_RETRIES} retries)` }));
        return; // Stop retrying
      }

      setState(prev => ({ ...prev, loading: false, error: errorMsg }));
      // Retry with exponential backoff: 5s, 10s, 20s, 40s
      const backoff = Math.min(5000 * Math.pow(2, retriesRef.current - 1), 60000);
      timerRef.current = setTimeout(fetchToken, backoff);
    }
  }, [accountId, machineId]);

  useEffect(() => {
    cancelledRef.current = false;
    retriesRef.current = 0;
    fetchToken();
    return () => {
      cancelledRef.current = true;
      if (timerRef.current) clearTimeout(timerRef.current);
    };
  }, [fetchToken]);

  return state;
}
