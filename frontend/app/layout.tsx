import type { ReactNode } from "react";
import Nav from "@/components/Nav";
import "./globals.css";

export const metadata = {
  title: "AI Code Review",
  description: "AI review of every commit in the organization",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <meta name="viewport" content="width=device-width, initial-scale=1" />
      </head>
      <body>
        <header className="top">
          <div className="wrap">
            <span className="brand">AI Code Review</span>
            <Nav />
          </div>
        </header>
        <main className="wrap" style={{ paddingTop: 20 }}>
          {children}
        </main>
      </body>
    </html>
  );
}
