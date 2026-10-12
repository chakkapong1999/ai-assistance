import { notFound } from "next/navigation";
import { PullRequestDetail } from "@/components/Detail";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  return { title: `Pull request ${(await params).id}` };
}

export default async function PullRequestPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<SP> }) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  return <PullRequestDetail id={id} sp={await searchParams} base={`/pull-requests/${id}`} />;
}
