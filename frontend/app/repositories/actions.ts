"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { ApiError, api } from "@/lib/api";

export async function toggleReview(formData: FormData) {
  const id = Number(formData.get("id"));
  const enable = formData.get("enable") === "true";
  let notice = "";
  try {
    await api.setReviewEnabled(id, enable);
  } catch (e) {
    if (!(e instanceof ApiError)) throw e;
    notice = e.status === 403 ? "This dashboard's token may not change anything (viewer role)." : e.message;
  }
  revalidatePath("/repositories");
  if (notice) redirect(`/repositories?notice=${encodeURIComponent(notice)}`);
}
