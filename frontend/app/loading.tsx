// Shown while a page waits for the API. Same head and list sheet as the real pages, so nothing jumps when data lands.
export default function Loading() {
  return (
    <div className="loading" role="status" aria-label="Loading">
      <div className="head">
        <i className="bar" style={{ width: 200, height: 30 }} />
      </div>
      <ul className="rows" aria-hidden="true">
        {[64, 52, 70, 46, 58, 40].map((w) => (
          <li key={w} className="item cols-recent">
            <span className="score" />
            <div>
              <i className="bar" style={{ width: `${w}%` }} />
              <i className="bar" />
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}
