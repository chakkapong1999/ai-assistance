import { test } from "node:test";
import assert from "node:assert/strict";
import { splitDiff } from "../lib/diff.ts";

const diff = `diff --git a/app/main.go b/app/main.go
index 1111111..2222222 100644
--- a/app/main.go
+++ b/app/main.go
@@ -1,3 +1,3 @@
 package main
--- old comment
+++ new comment
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1,1 +0,0 @@
-gone
diff --git a/logo.png b/logo.png
new file mode 100644
Binary files /dev/null and b/logo.png differ
`;

test("one entry per file, headers kept out of the hunk text", () => {
  const files = splitDiff(diff);
  assert.deepEqual(
    files.map((f) => [f.path, f.status, f.binary, f.additions, f.deletions]),
    [
      ["app/main.go", "modified", false, 1, 1],
      ["old.txt", "deleted", false, 0, 1],
      ["logo.png", "added", true, 0, 0],
    ],
  );
  assert.ok(files[0].text.startsWith("@@ -1,3 +1,3 @@"));
  assert.ok(files[0].text.includes("--- old comment"), "a removed line starting with --- stays in the hunk");
  assert.equal(files[2].text, "");
});

test("an empty diff has no files", () => {
  assert.deepEqual(splitDiff(""), []);
});
