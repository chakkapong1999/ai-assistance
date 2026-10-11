"use client";

// API problems are shown by guard(); this catches everything else, so a bug
// in one page does not replace the whole dashboard with Next's error page.
export default function Error({ error, retry }: { error: Error & { digest?: string }; retry: () => void }) {
  return (
    <div className="empty" role="alert" style={{ marginTop: 48 }}>
      <strong>This page could not be shown</strong>
      Something went wrong on our side{error.digest ? ` (reference ${error.digest}, in the server log)` : ""}.{" "}
      <button className="quiet" onClick={() => retry()}>
        Try again
      </button>
    </div>
  );
}
