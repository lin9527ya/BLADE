"use client";

import { useEffect, useRef, useState } from "react";

/**
 * useSSE subscribes to one named EventSource event and returns connection state.
 * The handler is kept in a ref so callers do not need useCallback; a changed
 * handler does not reconnect the stream.
 *
 * The stream sends full snapshots, so a missed frame is repaired by the next
 * one. EventSource performs its own reconnect/backoff; callers should keep a
 * low-frequency polling fallback while `connected` is false.
 */
export function useSSE<T>(
  url: string | null,
  eventName: string,
  onEvent: (data: T) => void,
  enabled = true,
): { connected: boolean } {
  const [connected, setConnected] = useState(false);
  const handlerRef = useRef(onEvent);
  handlerRef.current = onEvent;

  useEffect(() => {
    if (!enabled || !url || typeof window === "undefined") {
      setConnected(false);
      return;
    }
    const es = new EventSource(url);
    const listener = (event: Event) => {
      const message = event as MessageEvent<string>;
      try {
        handlerRef.current(JSON.parse(message.data) as T);
      } catch {
        // A malformed frame must not tear down the stream; the next snapshot wins.
      }
    };
    es.addEventListener(eventName, listener);
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    return () => {
      es.removeEventListener(eventName, listener);
      es.close();
      setConnected(false);
    };
  }, [url, eventName, enabled]);

  return { connected };
}
