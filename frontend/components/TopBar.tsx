"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect } from "react";
import type { Me } from "@/lib/api";
import { signOut } from "@/app/signin/actions";
import ThemeToggle from "./ThemeToggle";

const items = [
  ["/", "Overview"],
  ["/commits", "Commits"],
  ["/pull-requests", "Pull requests"],
  ["/repositories", "Repositories"],
  ["/users", "People"],
  ["/insights", "Insights"],
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

export default function TopBar({ me, todo, theme }: { me: Me | null; todo: number; theme?: "light" | "dark" }) {
  const path = usePathname();
  const [label, hint] = roles[me?.role ?? "none"];
  const role = me?.role ?? null;
  // "/" jumps to search from anywhere but a text field.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey || t?.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t?.tagName ?? "")) return;
      e.preventDefault();
      document.querySelector<HTMLInputElement>(".find input")?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <header className="top">
      <Link href="/" className="brand" aria-label="Code review, overview">
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="var(--accent)" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M3 13h5l1.5 3h5L16 13h5" />
          <path d="M5 5h14l2 8v6H3v-6z" />
        </svg>
        <span>Code review</span>
      </Link>
      <nav aria-label="Main">
        {[items[0], ...(me?.user ? ([["/inbox", "Inbox"]] as const) : []), ...items.slice(1), ...(role === "admin" ? ([["/audit", "Audit"]] as const) : [])].map(([href, text]) => {
          const active = href === "/" ? path === "/" : path.startsWith(href);
          return (
            <Link
              key={href}
              href={href}
              aria-current={active ? "page" : undefined}
            >
              {text}
              {href === "/inbox" && todo > 0 ? <span className="count"><span className="sr">, </span>{todo}<span className="sr"> to do</span></span> : null}
            </Link>
          );
        })}
      </nav>
      <form className="find" action="/search" role="search">
        <input type="search" name="q" placeholder="Search everything  /" aria-label="Search commits, pull requests, repositories and people" maxLength={200} required />
      </form>
      <div className="who">
        <ThemeToggle initial={theme} />
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
