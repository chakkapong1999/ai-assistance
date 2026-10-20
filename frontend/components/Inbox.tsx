"use client";

import Link from "next/link";
import { useEffect, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";

const typing = (t: EventTarget | null) => t instanceof HTMLElement && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName));

// The inbox's two panes. j and k move through the list in its order on the page.
export function InboxShell({ list, children }: { list: ReactNode; children: ReactNode }) {
  const path = usePathname();
  const router = useRouter();
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.key !== "j" && e.key !== "k") || e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
      const rows = [...document.querySelectorAll<HTMLAnchorElement>(".inbox a.row")];
      const at = rows.findIndex((a) => a.getAttribute("href") === path);
      const next = rows[at === -1 ? 0 : e.key === "j" ? Math.min(at + 1, rows.length - 1) : Math.max(at - 1, 0)];
      if (!next) return;
      e.preventDefault();
      next.scrollIntoView({ block: "nearest" });
      router.push(next.getAttribute("href")!);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [path, router]);
  const open = path !== "/inbox";
  return (
    <div className={`inbox${open ? " open" : ""}`}>
      <nav className="list" aria-label="Inbox">
        {list}
      </nav>
      <section className="pane">
        {open ? (
          <Link href="/inbox" className="backlink">
            Back to the inbox
          </Link>
        ) : null}
        {children}
      </section>
    </div>
  );
}

export function Row({ href, children }: { href: string; children: ReactNode }) {
  const current = usePathname() === href;
  return (
    <Link href={href} className="row" aria-current={current ? "page" : undefined} scroll={false}>
      {children}
    </Link>
  );
}
