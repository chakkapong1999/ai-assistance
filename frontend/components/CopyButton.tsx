"use client";

import { useState } from "react";

export default function CopyButton({ text, label = "Copy patch" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setDone(true);
          setTimeout(() => setDone(false), 1800);
        } catch {
          // clipboard blocked (non-secure origin): leave the label as it is
        }
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}
