import Link from "next/link";

export default function NotFound() {
  return (
    <div className="empty" style={{ marginTop: 48 }}>
      <strong>This page does not exist</strong>
      The link may be old, or the item was removed. <Link href="/">Go to the overview</Link>
    </div>
  );
}
