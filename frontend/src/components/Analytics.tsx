import { useEffect, useRef } from "react";
import { useLocation } from "react-router-dom";

declare global {
  interface Window {
    dataLayer: any[];
    gtag?: (...args: any[]) => void;
  }
}

const GA_ID = import.meta.env.VITE_GA_MEASUREMENT_ID as string | undefined;

function loadGa(id: string) {
  if (document.getElementById("ga4-script")) return;
  const script = document.createElement("script");
  script.id = "ga4-script";
  script.async = true;
  script.src = `https://www.googletagmanager.com/gtag/js?id=${id}`;
  document.head.appendChild(script);

  // Initialize gtag
  window.dataLayer = window.dataLayer || [];
  function gtag(...args: any[]) {
    window.dataLayer.push(args);
  }
  // @ts-ignore
  window.gtag = gtag;
  gtag("js", new Date());
  gtag("config", id, { send_page_view: false });
}

export function Analytics() {
  const location = useLocation();
  const initialized = useRef(false);

  useEffect(() => {
    if (!import.meta.env.PROD) return;
    if (!GA_ID) return;
    if (!initialized.current) {
      loadGa(GA_ID);
      initialized.current = true;
    }
    if (typeof window.gtag === "function") {
      window.gtag("event", "page_view", {
        page_path: location.pathname + location.search,
      });
    }
  }, [location]);

  return null;
}
