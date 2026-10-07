"use server";

import { redirect } from "next/navigation";
import { ApiError, api } from "@/lib/api";

export async function rereview(formData: FormData) {
  const id = Number(formData.get("id"));
  let notice = "Queued for another review.";
  try {
    await api.rereview(id);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    notice = e.status === 403 ? "This dashboard's token may not change anything (viewer role)." : e.message;
  }
  redirect(`/commits/${id}?notice=${encodeURIComponent(notice)}`);
}
