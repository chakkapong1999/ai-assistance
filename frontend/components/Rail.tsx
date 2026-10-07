"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const items = [
  { href: "/", label: "Overview" },
  { href: "/commits", label: "Commits" },
  { href: "/pull-requests", label: "Pull requests" },
  { href: "/repositories", label: "Repositories" },
  { href: "/users", label: "People" },
];

export default function Rail({ role }: { role: "viewer" | "admin" | null }) {
  const path = usePathname();
  return (
    <aside className="rail">
      <Link href="/" className="brand" aria-label="AI code review, overview">
        <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true">
          <rect x="2" y="5" width="12" height="4" rx="1" fill="#ff8b82" />
          <rect x="2" y="13" width="18" height="4" rx="1" fill="#5fd194" />
        </svg>
        <span>Code review</span>
      </Link>
      <nav aria-label="Main">
        {items.map((i) => {
          const active = i.href === "/" ? path === "/" : path.startsWith(i.href);
          return (
            <Link key={i.href} href={i.href} aria-current={active ? "page" : undefined}>
              {i.label}
            </Link>
          );
        })}
      </nav>
      <div className="role">
        {role === "admin" ? (
          <>
            <strong>Admin</strong>You can turn review on or off and review again.
          </>
        ) : role === "viewer" ? (
          <>
            <strong>Read only</strong>You can look but not change anything.
          </>
        ) : (
          <>
            <strong>Not connected</strong>The API did not answer.
          </>
        )}
      </div>
    </aside>
  );
}
