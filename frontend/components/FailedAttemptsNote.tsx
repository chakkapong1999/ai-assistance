import type { FailedAttempts } from "@/lib/api";
import { ago, compact, num, usd } from "@/lib/format";

// One line under the header: reviews that were thrown away still cost money.
export default function FailedAttemptsNote({ attempts: a }: { attempts: FailedAttempts }) {
  if (a.count === 0) return null;
  const cost = a.measured > 0 ? `${usd(a.cost_usd)} and ${compact(a.tokens_in)} / ${compact(a.tokens_out)} tokens spent` : "cost not measured";
  return (
    <p className="banner" role="status">
      <strong>
        {num(a.count)} failed review {a.count === 1 ? "attempt" : "attempts"}
      </strong>
      : {cost}, not included in the review below{a.last_at ? `. Last ${ago(a.last_at)}` : ""}
      {a.last_error ? `: ${a.last_error}` : ""}
    </p>
  );
}
