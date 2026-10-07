export default function Diff({ text }: { text: string }) {
  const lines = text.replace(/\n$/, "").split("\n");
  return (
    <pre className="diff">
      {lines.map((l, i) => {
        const cls = l.startsWith("@@") ? "hunk" : l.startsWith("+") && !l.startsWith("+++") ? "add" : l.startsWith("-") && !l.startsWith("---") ? "del" : "";
        return (
          <span key={i} className={cls}>
            {l || " "}
          </span>
        );
      })}
    </pre>
  );
}
