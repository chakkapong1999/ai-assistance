"use server";

import { redirect } from "next/navigation";
import { ApiError, api } from "@/lib/api";
import { safePath } from "../signin/actions";

type Run = (id: number, note: string) => Promise<unknown>;

// Runs one workflow step and goes back to the page with a short notice. The
// API makes every decision (who may do what); this only reports its answer.
async function step(formData: FormData, run: Run, done: string) {
  const id = Number(formData.get("id"));
  const note = String(formData.get("note") ?? "");
  const back = await safePath(formData.get("back"));
  const anchor = formData.get("anchor") ? `#${String(formData.get("anchor")).replace(/[^\w-]/g, "")}` : "";
  let notice = done;
  try {
    await run(id, note);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    notice =
      e.status === 401 || e.code === "config"
        ? "Sign in with your personal token first."
        : e.status === 403
          ? e.message.startsWith("this action needs")
            ? "Your role cannot do this."
            : e.message
          : e.message;
  }
  const u = new URL(back, "http://local");
  u.searchParams.set("notice", notice);
  redirect(`${u.pathname}${u.search}${anchor}`);
}

export async function markFixed(f: FormData) {
  return step(f, api.findingFixed, "Marked as fixed. A senior, lead or admin will look at it.");
}
export async function sendBack(f: FormData) {
  return step(f, api.findingReopen, "Sent back to the author.");
}
export async function dismiss(f: FormData) {
  return step(f, api.findingDismiss, "Dismissed.");
}
export async function closeReview(f: FormData) {
  return step(f, api.closeReview, "Review closed.");
}
