import { useSyncExternalStore } from "react";

// Custom event for client-side navigation
const NAV_EVENT = "portal:navigate";

export function navigate(to: string) {
  window.history.pushState({}, "", to);
  window.dispatchEvent(new PopStateEvent(NAV_EVENT));
}

// Subscribe to both popstate (back/forward) and our custom nav event
function subscribe(callback: () => void) {
  window.addEventListener("popstate", callback);
  window.addEventListener(NAV_EVENT, callback);
  return () => {
    window.removeEventListener("popstate", callback);
    window.removeEventListener(NAV_EVENT, callback);
  };
}

function getSnapshot() {
  return window.location.pathname + window.location.search;
}

export function useLocation() {
  return useSyncExternalStore(subscribe, getSnapshot);
}
