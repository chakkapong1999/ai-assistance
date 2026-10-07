"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const items = [
  ["/", "Overview"],
  ["/commits", "Commits"],
  ["/pull-requests", "Pull requests"],
  ["/repositories", "Repositories"],
  ["/users", "People"],
] as const;

const roles = {
  admin: ["Admin", "You can turn review on or off and review again."],
  viewer: ["Read only", "You can look but not change anything."],
  none: ["API not connected", "The API did not answer. Check API_BASE_URL and API_TOKEN."],
} as const;

export default function TopBar({ role }: { role: "viewer" | "admin" | null }) {
  const path = usePathname();
  const [label, hint] = roles[role ?? "none"];
  return (
    <header className="top">
      <Link href="/" className="brand" aria-label="Code review, overview">
        <svg width="20" height="20" viewBox="0 0 20 20" aria-hidden="true">
          <rect x="0" y="3" width="11" height="5" rx="1" style={{ fill: "var(--del)" }} />
          <rect x="0" y="12" width="20" height="5" rx="1" style={{ fill: "var(--add)" }} />
        </svg>
        <span>Code review</span>
      </Link>
      <nav aria-label="Main">
        {items.map(([href, text]) => {
          const active = href === "/" ? path === "/" : path.startsWith(href);
          return (
            <Link key={href} href={href} aria-current={active ? "page" : undefined}>
              {text}
            </Link>
          );
        })}
      </nav>
      <p className={`role${role ? "" : " off"}`} title={hint}>
        {label}
        <span className="sr">. {hint}</span>
      </p>
    </header>
  );
}
