import type { ReactNode } from "react";
import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import Rail from "@/components/Rail";
import { api } from "@/lib/api";
import "./globals.css";

export const metadata = {
  title: { default: "Code review", template: "%s · Code review" },
  description: "AI review of every commit and pull request in the organization",
};

export const dynamic = "force-dynamic";

export default async function RootLayout({ children }: { children: ReactNode }) {
  const role = await api.me().then((m) => m.role).catch(() => null);
  return (
    <html lang="en">
      <head>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
      </head>
      <body>
        <div className="shell">
          <Rail role={role} />
          <main className="main">{children}</main>
        </div>
      </body>
    </html>
  );
}
