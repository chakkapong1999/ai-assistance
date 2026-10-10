import { test } from "node:test";
import assert from "node:assert/strict";
import { describe, actorName, groups } from "../lib/audit.ts";
import type { AuditEntry } from "../lib/api";

const e = (over: Partial<AuditEntry>): AuditEntry =>
  ({
    id: 1,
    at: "2026-10-10T00:00:00Z",
    actor: { id: 7, name: "Somchai", role: "senior" },
    action: "finding.fixed",
    subject: { type: "finding", id: 42 },
    detail: {},
    ...over,
  }) as AuditEntry;

test("a finding links to its commit with the finding in view", () => {
  const l = describe(e({ detail: { commit_id: 5 } }));
  assert.equal(l.text, "marked a finding as fixed");
  assert.equal(l.href, "/commits/5#f-42");
});

test("a finding of a pull request links to the pull request", () => {
  assert.equal(describe(e({ detail: { pull_request_id: 9 } })).href, "/pull-requests/9#f-42");
});

test("a finding with no known page has no link", () => {
  assert.equal(describe(e({})).href, null);
  assert.equal(describe(e({ detail: { commit_id: "5" } })).href, null);
});

test("reviews, commits, pull requests and repositories link to their pages", () => {
  assert.equal(describe(e({ action: "review.closed", subject: { type: "review", id: 3 }, detail: { commit_id: 5 } })).href, "/commits/5");
  assert.equal(describe(e({ action: "commit.rereview", subject: { type: "commit", id: 5 } })).href, "/commits/5");
  assert.equal(describe(e({ action: "pull_request.rereview", subject: { type: "pull_request", id: 9 } })).href, "/pull-requests/9");
  const r = describe(e({ action: "repository.review_enabled", subject: { type: "repository", id: 2 }, detail: { repository: "ws/app" } }));
  assert.equal(r.text, "switched review on for ws/app");
  assert.equal(r.href, "/commits?repo_id=2");
});

test("an unknown action is shown as it is", () => {
  assert.equal(describe(e({ action: "something.new", subject: { type: "commit", id: 1 } })).text, "something.new");
});

test("actorName falls back to the user id, then the role", () => {
  assert.equal(actorName(e({})), "Somchai");
  assert.equal(actorName(e({ actor: { id: 7, name: null, role: "senior" } as AuditEntry["actor"] })), "User 7");
  assert.equal(actorName(e({ actor: { id: null, name: null, role: "admin" } as AuditEntry["actor"] })), "admin token");
});

test("the filter groups start with everything", () => {
  assert.deepEqual(groups[0], ["", "Everything"]);
});
