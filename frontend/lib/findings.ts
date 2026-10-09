import type { Finding } from "./api";

export type FindingFilter = { q: string; status: string; severity: string };

export const matches = (f: Finding, x: FindingFilter) =>
  (!x.status || f.status === x.status) &&
  (!x.severity || f.severity === x.severity) &&
  (!x.q || [f.title, f.explanation, f.file_path, f.category].some((t) => t.toLowerCase().includes(x.q.toLowerCase())));
