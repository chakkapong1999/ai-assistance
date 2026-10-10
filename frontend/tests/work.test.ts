import { test } from "node:test";
import assert from "node:assert/strict";
import { findingHref, itemHref, itemTitle, sentBackCount } from "../lib/work.ts";
import type { WorkItem } from "../lib/api";

const item = (over: Partial<WorkItem>): WorkItem =>
  ({ kind: "commit", id: 5, title: "Fix login", number: null, repository: "acme/api", author: null, reviewed_at: "2026-10-10T00:00:00Z", findings: 2, open_findings: [], ...over }) as WorkItem;

test("links go to the commit or pull request, with the finding in view", () => {
  assert.equal(itemHref(item({})), "/commits/5");
  assert.equal(findingHref(item({}), { id: 9 }), "/commits/5#f-9");
  assert.equal(findingHref(item({ kind: "pull_request", id: 3 }), { id: 9 }), "/pull-requests/3#f-9");
});

test("a pull request title carries its number", () => {
  assert.equal(itemTitle(item({})), "Fix login");
  assert.equal(itemTitle(item({ kind: "pull_request", number: 12, title: "Add cache" })), "#12 Add cache");
});

test("sent back findings are counted across items", () => {
  const f = (back: boolean) => ({ id: 1, sent_back: back ? { by: null, note: null, at: "x" } : null }) as WorkItem["open_findings"][number];
  assert.equal(sentBackCount([item({ open_findings: [f(true), f(false)] }), item({ open_findings: [f(true)] })]), 2);
  assert.equal(sentBackCount([]), 0);
});
