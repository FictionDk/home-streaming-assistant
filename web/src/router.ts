import { useEffect, useState } from "react";

const NAV_EVENT = "hsa:navigate";

export function navigate(to: string): void {
  window.history.pushState(null, "", to);
  window.dispatchEvent(new Event(NAV_EVENT));
}

export function replaceRoute(to: string): void {
  window.history.replaceState(null, "", to);
  window.dispatchEvent(new Event(NAV_EVENT));
}

export function usePathname(): string {
  const [path, setPath] = useState(() => window.location.pathname);
  useEffect(() => {
    const update = () => {
      setPath(window.location.pathname);
    };
    window.addEventListener("popstate", update);
    window.addEventListener(NAV_EVENT, update);
    return () => {
      window.removeEventListener("popstate", update);
      window.removeEventListener(NAV_EVENT, update);
    };
  }, []);
  return path;
}
