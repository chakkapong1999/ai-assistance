import { redirect } from "next/navigation";
import { api } from "@/lib/api";
import { one } from "@/lib/format";
import { signIn } from "./actions";

export const metadata = { title: "Sign in" };

export default async function SignInPage({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const sp = await searchParams;
  const error = one(sp.error);
  const next = one(sp.next) ?? "/";
  const me = await api.me().catch(() => null);
  if (me?.user && !error) redirect(next.startsWith("/") && !next.startsWith("//") ? next : "/");

  return (
    <div className="signin-page">
      <h1>Sign in</h1>
      <p className="muted">
        Use your personal token to mark findings as fixed, or, as a senior, lead or admin, to review the fixes and close reviews. An admin
        creates it for you. Without it you can still look around.
      </p>
      {error ? (
        <p className="banner bad" role="alert" style={{ marginTop: 16 }}>
          {error}
        </p>
      ) : null}
      <form action={signIn} className="stack">
        <input type="hidden" name="next" value={next} />
        <label>
          Personal token
          <input name="token" type="password" autoComplete="off" required spellCheck={false} />
        </label>
        <button className="primary">Sign in</button>
      </form>
    </div>
  );
}
