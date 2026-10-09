import { test } from "node:test";
import assert from "node:assert/strict";
import { matches } from "../lib/findings.ts";
import type { Finding } from "../lib/api";

const f = (over: Partial<Finding>): Finding =>
  ({ title: "SQL injection", explanation: "User input reaches the query", file_path: "db/users.go", category: "security", severity: "critical", status: "open", ...over }) as Finding;

test("an empty filter matches everything", () => {
  assert.equal(matches(f({}), { q: "", status: "", severity: "" }), true);
});

test("status and severity must both agree", () => {
  assert.equal(matches(f({}), { q: "", status: "open", severity: "critical" }), true);
  assert.equal(matches(f({}), { q: "", status: "fixed", severity: "" }), false);
  assert.equal(matches(f({}), { q: "", status: "", severity: "minor" }), false);
});

test("text search is case-insensitive over title, explanation, path and category", () => {
  for (const q of ["INJECTION", "reaches", "USERS.GO", "Security"]) {
    assert.equal(matches(f({}), { q, status: "", severity: "" }), true, q);
  }
  assert.equal(matches(f({}), { q: "nothing like this", status: "", severity: "" }), false);
});
