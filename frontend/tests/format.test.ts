import { test } from "node:test";
import assert from "node:assert/strict";
import { ago, intParam, one, score, tone, usd } from "../lib/format.ts";

test("usd keeps four decimals under a dollar and two above", () => {
  assert.equal(usd(0.0789), "$0.0789");
  assert.equal(usd(12.5), "$12.50");
  assert.equal(usd(null), "-");
});

test("ago rounds down to the largest unit", () => {
  const now = Date.parse("2026-01-02T12:00:00Z");
  assert.equal(ago("2026-01-02T11:59:50Z", now), "just now");
  assert.equal(ago("2026-01-02T11:15:00Z", now), "45 min ago");
  assert.equal(ago("2026-01-02T09:00:00Z", now), "3 h ago");
  assert.equal(ago("2025-12-30T12:00:00Z", now), "3 d ago");
  assert.equal(ago(null, now), "-");
});

test("intParam and one read query values defensively", () => {
  assert.equal(intParam("30", 7, 1, 90), 30);
  assert.equal(intParam("500", 7, 1, 90), 7);
  assert.equal(intParam(["14", "3"], 7, 1, 90), 14);
  assert.equal(intParam(undefined, 7, 1, 90), 7);
  assert.equal(one(["a", "b"]), "a");
  assert.equal(one(undefined), "");
});

test("tone and score", () => {
  assert.equal(tone(90), "good");
  assert.equal(tone(89.9), "warn");
  assert.equal(tone(69), "bad");
  assert.equal(tone(null), "none");
  assert.equal(score(87.25), "87.3");
  assert.equal(score(undefined), "-");
});
