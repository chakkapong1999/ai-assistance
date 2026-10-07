"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { ApiError, TOKEN_COOKIE, api } from "@/lib/api";

// Only paths on this site may be returned to.
export async function safePath(p: FormDataEntryValue | null): Promise<string> {
  const s = typeof p === "string" ? p : "/";
  return s.startsWith("/") && !s.startsWith("//") && !s.includes("\\") ? s : "/";
}

export async function signIn(formData: FormData) {
  const token = String(formData.get("token") ?? "").trim();
  const next = await safePath(formData.get("next"));
  const fail = (m: string) => redirect(`/signin?error=${encodeURIComponent(m)}&next=${encodeURIComponent(next)}`);
  if (!token) fail("Paste your personal token.");
  let name = "";
  try {
    const me = await api.meWith(token);
    if (!me.user) fail("This token is not linked to a person, so it cannot take part in reviews. Ask an admin for a personal token.");
    name = me.user?.name ?? "";
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    fail(e.status === 401 ? "The API does not know this token. Check it and try again." : e.message);
  }
  (await cookies()).set(TOKEN_COOKIE, token, {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.COOKIE_SECURE === "true",
    path: "/",
    maxAge: 60 * 60 * 24 * 14,
  });
  redirect(next);
}

export async function signOut() {
  (await cookies()).delete(TOKEN_COOKIE);
  redirect("/");
}
