import type { ReactNode } from "react";
import { notFound } from "next/navigation";
import { ApiError } from "./api";

// Server components cannot show an error's message in production, so API
// problems are turned into a visible box here instead of an error boundary.
export async function guard<T>(p: Promise<T>): Promise<[T, null] | [null, ReactNode]> {
  try {
    return [await p, null];
  } catch (e) {
    if (e instanceof ApiError) {
      if (e.status === 404) notFound();
      const hint = e.code === "unauthorized" ? " Check that API_TOKEN matches one of the API's API_TOKENS." : "";
      return [null, <div key="problem" className="banner" role="alert"><strong>Cannot load data.</strong> {e.message}{hint}</div>];
    }
    throw e;
  }
}
