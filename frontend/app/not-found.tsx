import Link from "next/link";

export default function NotFound() {
  return (
    <div className="empty">
      <p>Not found.</p>
      <Link href="/">Back to overview</Link>
    </div>
  );
}
