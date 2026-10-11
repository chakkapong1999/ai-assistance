"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// Re-reads the page from the server every few seconds, keeping scroll and
// form state. Pages render it only while a review is waiting or running, so
// it stops by itself once the work is done. Hidden tabs are not refreshed.
export default function AutoRefresh({ seconds = 5 }: { seconds?: number }) {
  const router = useRouter();
  useEffect(() => {
    const t = setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, seconds * 1000);
    return () => clearInterval(t);
  }, [router, seconds]);
  return null;
}
