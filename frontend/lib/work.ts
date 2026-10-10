import type { WorkFinding, WorkItem } from "./api";

/** The page of a commit or pull request. */
export const itemHref = (i: Pick<WorkItem, "kind" | "id">) => (i.kind === "commit" ? `/commits/${i.id}` : `/pull-requests/${i.id}`);

/** The finding itself, in view on that page. */
export const findingHref = (i: Pick<WorkItem, "kind" | "id">, f: Pick<WorkFinding, "id">) => `${itemHref(i)}#f-${f.id}`;

/** "#12 Title" for a pull request, the first line of the message for a commit. */
export const itemTitle = (i: Pick<WorkItem, "kind" | "title" | "number">) => (i.kind === "pull_request" && i.number ? `#${i.number} ${i.title}` : i.title);

/** How many findings were sent back by a reviewer. */
export const sentBackCount = (items: WorkItem[]) => items.reduce((n, i) => n + i.open_findings.filter((f) => f.sent_back).length, 0);
