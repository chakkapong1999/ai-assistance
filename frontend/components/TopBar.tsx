"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { Me } from "@/lib/api";
import { signOut } from "@/app/signin/actions";

const items = [
  ["/", "Overview"],
  ["/commits", "Commits"],
  ["/pull-requests", "Pull requests"],
  ["/repositories", "Repositories"],
  ["/users", "People"],
] as const;

const roles = {
  admin: [
    "Admin",
    "You can turn review on or off, review again, and close reviews.",
  ],
  lead: ["Lead", "You can send findings back, dismiss them and close reviews."],
  senior: [
    "Senior",
    "You can send findings back, dismiss them and close reviews.",
  ],
  author: ["Author", "You can mark your own findings as fixed."],
  viewer: ["Read only", "You can look but not change anything."],
  none: [
    "API not connected",
    "The API did not answer. Check API_BASE_URL and API_TOKEN.",
  ],
} as const;

export default function TopBar({ me }: { me: Me | null }) {
  const path = usePathname();
  const [label, hint] = roles[me?.role ?? "none"];
  const role = me?.role ?? null;
  return (
    <header className="top">
      <Link href="/" className="brand" aria-label="Code review, overview">
        <svg width="20" height="20" viewBox="0 0 20 20" aria-hidden="true">
          <rect
            x="0"
            y="3"
            width="11"
            height="5"
            rx="1"
            style={{ fill: "var(--del)" }}
          />
          <rect
            x="0"
            y="12"
            width="20"
            height="5"
            rx="1"
            style={{ fill: "var(--add)" }}
          />
        </svg>
        <span>Code review</span>
      </Link>
      <nav aria-label="Main">
        {items.map(([href, text]) => {
          const active = href === "/" ? path === "/" : path.startsWith(href);
          return (
            <Link
              key={href}
              href={href}
              aria-current={active ? "page" : undefined}
            >
              {text}
            </Link>
          );
        })}
      </nav>
      <form className="find" action="/search" role="search">
        <input type="search" name="q" placeholder="Search everything" aria-label="Search commits, pull requests, repositories and people" maxLength={200} required />
      </form>
      <div className="who">
        <p className={`role${role ? "" : " off"}`} title={hint}>
          {me?.user ? <b>{me.user.name}</b> : null}
          {label}
          <span className="sr">. {hint}</span>
        </p>
        {me?.user ? (
          <form action={signOut}>
            <button className="quiet">Sign out</button>
          </form>
        ) : (
          <Link href="/signin" className="signin">
            Sign in
          </Link>
        )}
      </div>
    </header>
  );
}
