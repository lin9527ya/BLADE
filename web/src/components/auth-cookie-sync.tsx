"use client";

import * as React from "react";

import { auth } from "@/lib/auth";

// Keeps the blade_token cookie aligned with localStorage on every page mount.
// API calls carry Authorization and work without the cookie, but EventSource
// cannot set headers — a missing/stale cookie makes the live stream 401 silently,
// which looks like "the page only updates after a refresh".
export function AuthCookieSync() {
  React.useEffect(() => {
    auth.ensureCookie();
  }, []);
  return null;
}
