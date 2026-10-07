import type { ReactNode } from "react";

export const metadata = {
  title: "AI Code Review",
  description: "AI review of every commit in the organization",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body style={{ fontFamily: "system-ui, sans-serif", margin: 0, padding: "2rem 1rem" }}>
        <main style={{ maxWidth: 720, margin: "0 auto" }}>{children}</main>
      </body>
    </html>
  );
}
