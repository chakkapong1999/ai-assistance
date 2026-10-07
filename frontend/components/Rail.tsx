"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

// Lucide icon shapes (layout-dashboard, git-commit, git-pull-request, folder, users), inlined.
const items = [
  {
    href: "/",
    label: "Overview",
    icon: (
      <>
        <rect x="3" y="3" width="7" height="9" rx="1" />
        <rect x="14" y="3" width="7" height="5" rx="1" />
        <rect x="14" y="12" width="7" height="9" rx="1" />
        <rect x="3" y="16" width="7" height="5" rx="1" />
      </>
    ),
  },
  {
    href: "/commits",
    label: "Commits",
    icon: (
      <>
        <circle cx="12" cy="12" r="3" />
        <path d="M3 12h6M15 12h6" />
      </>
    ),
  },
  {
    href: "/pull-requests",
    label: "Pull requests",
    icon: (
      <>
        <circle cx="18" cy="18" r="3" />
        <circle cx="6" cy="6" r="3" />
        <path d="M13 6h3a2 2 0 0 1 2 2v7M6 9v12" />
      </>
    ),
  },
  {
    href: "/repositories",
    label: "Repositories",
    icon: <path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z" />,
  },
  {
    href: "/users",
    label: "People",
    icon: (
      <>
        <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M22 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75" />
        <circle cx="9" cy="7" r="4" />
      </>
    ),
  },
];

export default function Rail({ role }: { role: "viewer" | "admin" | null }) {
  const path = usePathname();
  return (
    <aside className="rail">
      <Link href="/" className="brand" aria-label="AI code review, overview">
        <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true">
          <rect x="2" y="5" width="12" height="4" rx="1" style={{ fill: "var(--del)" }} />
          <rect x="2" y="13" width="18" height="4" rx="1" style={{ fill: "var(--add)" }} />
        </svg>
        <span>Code review</span>
      </Link>
      <nav aria-label="Main">
        {items.map((i) => {
          const active = i.href === "/" ? path === "/" : path.startsWith(i.href);
          return (
            <Link key={i.href} href={i.href} aria-current={active ? "page" : undefined}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                {i.icon}
              </svg>
              {i.label}
            </Link>
          );
        })}
      </nav>
      <div className={`role${role ? "" : " off"}`}>
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
