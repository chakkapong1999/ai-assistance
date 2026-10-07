// The dashboard talks to the Go API only; it never connects to Postgres.
// API_BASE_URL is read on the server (see docker-compose.yml).
const baseUrl = process.env.API_BASE_URL ?? "http://localhost:8080";

export type Health = { ok: true } | { ok: false; reason: string };

export async function getHealth(): Promise<Health> {
  try {
    const res = await fetch(`${baseUrl}/healthz`, { cache: "no-store" });
    if (!res.ok) return { ok: false, reason: `API answered ${res.status}` };
    return { ok: true };
  } catch {
    return { ok: false, reason: "API is not reachable" };
  }
}
