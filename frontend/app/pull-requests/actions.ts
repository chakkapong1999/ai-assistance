"use server";

import { redirect } from "next/navigation";
import { revalidatePath } from "next/cache";
import { ApiError, api } from "@/lib/api";
import { safePath } from "../signin/actions";

export async function rereviewPullRequest(formData: FormData) {
  const id = Number(formData.get("id"));
  let notice = "Queued for another review.";
  try {
    await api.rereviewPullRequest(id);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    notice = e.status === 403 ? "This dashboard's token may not change anything (viewer role)." : e.message;
  }
  revalidatePath("/", "layout");
  // The inbox sends its own address; the commit page sends none.
  const back = formData.get("back") ? await safePath(formData.get("back")) : `/pull-requests/${id}`;
  redirect(`${back}?notice=${encodeURIComponent(notice)}`);
}
