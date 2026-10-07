"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const items = [
  { href: "/", label: "Overview" },
  { href: "/commits", label: "Commits" },
  { href: "/repositories", label: "Repositories" },
  { href: "/users", label: "People" },
];

export default function Nav() {
  const path = usePathname();
  return (
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
  );
}
