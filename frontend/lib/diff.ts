// Splits a whole git-style unified diff into one entry per file, so each file
// can be shown under its own header. `text` starts at the first "@@" and is
// what DiffView renders; it is "" for binary files and pure renames.

export type FileChange = {
  path: string;
  status: "added" | "deleted" | "renamed" | "modified";
  binary: boolean;
  text: string;
  additions: number;
  deletions: number;
};

const strip = (p: string) => (p.startsWith("a/") || p.startsWith("b/") ? p.slice(2) : p);

export function splitDiff(diff: string): FileChange[] {
  const out: FileChange[] = [];
  let cur: FileChange | null = null;
  let body: string[] = [];
  const flush = () => {
    if (cur) out.push({ ...cur, text: body.join("\n") });
    body = [];
  };
  for (const ln of diff.replace(/\r\n/g, "\n").split("\n")) {
    if (ln.startsWith("diff --git ")) {
      flush();
      const rest = ln.slice("diff --git ".length);
      const j = rest.lastIndexOf(" b/");
      cur = { path: j >= 0 ? rest.slice(j + 3) : rest, status: "modified", binary: false, text: "", additions: 0, deletions: 0 };
      continue;
    }
    if (!cur) continue;
    if (body.length > 0 || ln.startsWith("@@")) {
      // Inside the hunks: "--- " and "+++ " are removed/added lines, not headers.
      body.push(ln);
      if (ln.startsWith("+")) cur.additions++;
      else if (ln.startsWith("-")) cur.deletions++;
    } else if (ln.startsWith("new file mode")) cur.status = "added";
    else if (ln.startsWith("deleted file mode")) cur.status = "deleted";
    else if (ln.startsWith("rename to ")) {
      cur.status = "renamed";
      cur.path = ln.slice("rename to ".length);
    } else if (ln.startsWith("Binary files ") || ln.startsWith("GIT binary patch")) cur.binary = true;
    else if (ln.startsWith("+++ ") && ln !== "+++ /dev/null") cur.path = strip(ln.slice(4));
  }
  flush();
  return out;
}
