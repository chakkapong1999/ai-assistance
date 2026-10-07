import { getHealth } from "@/lib/api";

export const dynamic = "force-dynamic";

export default async function Home() {
  const health = await getHealth();

  return (
    <>
      <h1>AI Code Review</h1>
      <p>The commit list and review results arrive in a later milestone (M6).</p>
      <p>
        API status: <strong>{health.ok ? "connected" : health.reason}</strong>
      </p>
    </>
  );
}
