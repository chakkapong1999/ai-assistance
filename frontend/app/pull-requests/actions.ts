"use server";

import { redirect } from "next/navigation";
import { ApiError, api } from "@/lib/api";

export async function rereviewPullRequest(formData: FormData) {
  const id = Number(formData.get("id"));
  let notice = "Queued for another review.";
  try {
    await api.rereviewPullRequest(id);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    notice = e.status === 403 ? "This dashboard's token may not change anything (viewer role)." : e.message;
  }
  redirect(`/pull-requests/${id}?notice=${encodeURIComponent(notice)}`);
}
