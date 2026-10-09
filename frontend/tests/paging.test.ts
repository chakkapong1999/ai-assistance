import { test } from "node:test";
import assert from "node:assert/strict";
import { DEFAULT_SIZE, paging, window } from "../lib/paging.ts";

test("paging falls back to page 1 and 10 rows on odd input", () => {
  for (const [p, s] of [["", ""], ["0", "7"], ["-3", "abc"], ["1.5", "10.5"], ["999999", "1000"], ["x", "0"]]) {
    assert.deepEqual(paging(p, s), { page: 1, size: DEFAULT_SIZE }, `${p}/${s}`);
  }
});

test("paging keeps valid values", () => {
  assert.deepEqual(paging("7", "50"), { page: 7, size: 50 });
  assert.deepEqual(paging("100000", "100"), { page: 100000, size: 100 });
});

test("window shows the ends and two pages each side, with gaps", () => {
  assert.deepEqual(window(1, 1), [1]);
  assert.deepEqual(window(1, 5), [1, 2, 3, null, 5]);
  assert.deepEqual(window(6, 12), [1, null, 4, 5, 6, 7, 8, null, 12]);
  assert.deepEqual(window(12, 12), [1, null, 10, 11, 12]);
});

test("window has no gap marker when pages are adjacent", () => {
  assert.deepEqual(window(3, 6), [1, 2, 3, 4, 5, 6]);
});
