import { notFound } from "next/navigation";
import { CommitDetail } from "@/components/Detail";

type SP = Record<string, string | string[] | undefined>;

export async function generateMetadata({ params }: { params: Promise<{ id: string }> }) {
  return { title: `Commit ${(await params).id}` };
}

export default async function CommitPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: Promise<SP> }) {
  const id = Number((await params).id);
  if (!Number.isInteger(id) || id < 1) notFound();
  return <CommitDetail id={id} sp={await searchParams} base={`/commits/${id}`} />;
}
