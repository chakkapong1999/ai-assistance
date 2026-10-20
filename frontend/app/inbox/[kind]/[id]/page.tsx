import { notFound } from "next/navigation";
import { CommitDetail, PullRequestDetail } from "@/components/Detail";

type SP = Record<string, string | string[] | undefined>;
type Params = Promise<{ kind: string; id: string }>;

export async function generateMetadata({ params }: { params: Params }) {
  const { kind, id } = await params;
  return { title: `${kind === "commits" ? "Commit" : "Pull request"} ${id} · Inbox` };
}

export default async function InboxItem({ params, searchParams }: { params: Params; searchParams: Promise<SP> }) {
  const { kind, id: raw } = await params;
  const id = Number(raw);
  if (!Number.isInteger(id) || id < 1) notFound();
  const sp = await searchParams;
  const base = `/inbox/${kind}/${id}`;
  if (kind === "commits") return <CommitDetail id={id} sp={sp} base={base} crumbs={false} />;
  if (kind === "pull-requests") return <PullRequestDetail id={id} sp={sp} base={base} crumbs={false} />;
  notFound();
}
