import type { ReactNode } from "react";
import "@fontsource-variable/schibsted-grotesk";
import "@fontsource-variable/geist-mono";
import TopBar from "@/components/TopBar";
import { api } from "@/lib/api";
import "./globals.css";

export const metadata = {
  title: { default: "Code review", template: "%s · Code review" },
  description: "AI review of every commit and pull request in the organization",
};

export const dynamic = "force-dynamic";

export default async function RootLayout({ children }: { children: ReactNode }) {
  const me = await api.me().catch(() => null);
  // ponytail: reads the whole work list for one number on every page; add a count endpoint if /me/work gets slow
  const work = me?.user ? await api.myWork().catch(() => null) : null;
  const todo = work ? work.to_fix.total_findings + work.to_review.length : 0;
  return (
    <html lang="en">
      <head>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
      </head>
      <body>
        <TopBar me={me} todo={todo} />
        <main className="main">{children}</main>
      </body>
    </html>
  );
}
