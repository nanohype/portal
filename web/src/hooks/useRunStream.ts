import { useEffect, useRef, useCallback, useState } from "react";

interface UseRunStreamOptions {
  runId: string;
  workspaceId: string;
  enabled?: boolean;
  onData?: (data: string) => void;
}

const MAX_RETRIES = 5;

export function useRunStream({
  runId,
  workspaceId,
  enabled = true,
  onData,
}: UseRunStreamOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const retriesRef = useRef(0);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const connectRef = useRef<() => void>(() => {});
  const [isConnected, setIsConnected] = useState(false);
  const [isComplete, setIsComplete] = useState(false);

  const connect = useCallback(() => {
    if (!enabled || !runId) return;

    const token = localStorage.getItem("portal_token");
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host;
    const url = `${protocol}//${host}/api/v1/workspaces/${workspaceId}/runs/${runId}/logs/ws?token=${token}`;

    const ws = new WebSocket(url);
    wsRef.current = ws;

    ws.onopen = () => {
      setIsConnected(true);
      retriesRef.current = 0;
    };

    ws.onmessage = (event) => {
      onData?.(event.data);
    };

    ws.onclose = (event) => {
      setIsConnected(false);

      if (event.code === 1000) {
        // Normal close from server — stream is done
        setIsComplete(true);
        return;
      }

      // Unexpected close — attempt reconnect with backoff
      if (retriesRef.current < MAX_RETRIES) {
        const delay = 1000 * Math.pow(2, retriesRef.current);
        retriesRef.current++;
        reconnectTimerRef.current = setTimeout(() => connectRef.current(), delay);
      } else {
        setIsComplete(true);
      }
    };

    ws.onerror = () => {
      setIsConnected(false);
    };
  }, [runId, workspaceId, enabled, onData]);

  useEffect(() => {
    connectRef.current = connect;
    connect();
    return () => {
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current);
        reconnectTimerRef.current = null;
      }
      wsRef.current?.close();
    };
  }, [connect]);

  return { isConnected, isComplete };
}
