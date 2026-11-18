"use client";

import { useEffect, useRef, useState } from "react";

import { sseUrl } from "@/lib/api";
import type { Activity } from "@/lib/types";

export interface ConversationStreamStatus {
  running: boolean;
  cursor: number;
  has_more: boolean;
  at: number;
  error?: string;
}

/**
 * Live tail for one conversation. The server sends activity frames with SSE
 * `id:` set, so EventSource supplies Last-Event-ID on reconnect; the first
 * connection also replays the latest page to cover the history/SSE handoff.
 */
export function useConversationStream(
  conversationID: number | null,
  onActivity: (activity: Activity) => void,
  onStatus: (status: ConversationStreamStatus) => void,
  enabled = true,
): { connected: boolean } {
  const [connected, setConnected] = useState(false);
  const activityRef = useRef(onActivity);
  const statusRef = useRef(onStatus);
  activityRef.current = onActivity;
  statusRef.current = onStatus;

  useEffect(() => {
    if (!enabled || !conversationID || typeof window === "undefined") {
      setConnected(false);
      return;
    }
    const es = new EventSource(sseUrl(`/api/conversations/${conversationID}/events`));
    const onActivityEvent = (event: Event) => {
      try {
        activityRef.current(JSON.parse((event as MessageEvent<string>).data) as Activity);
      } catch {
        // The next frame/snapshot repairs a malformed one.
      }
    };
    const onStatusEvent = (event: Event) => {
      try {
        statusRef.current(JSON.parse((event as MessageEvent<string>).data) as ConversationStreamStatus);
      } catch {
        // Ignore malformed status; the fallback poll still runs.
      }
    };
    es.addEventListener("activity", onActivityEvent);
    es.addEventListener("status", onStatusEvent);
    es.onopen = () => setConnected(true);
    es.onerror = () => setConnected(false);
    return () => {
      es.removeEventListener("activity", onActivityEvent);
      es.removeEventListener("status", onStatusEvent);
      es.close();
      setConnected(false);
    };
  }, [conversationID, enabled]);

  return { connected };
}
