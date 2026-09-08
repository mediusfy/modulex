// Renders verification results and plans for the results webview. Kept free
// of any vscode import so it is unit-testable with plain node:test.

import type {
  PlanCheckSpec,
  VerificationResult,
  VerificationStatus,
} from "./toolSurface";

export function statusIcon(status: VerificationStatus): string {
  switch (status) {
    case "pass":
      return "✅";
    case "fail":
      return "❌";
    case "skipped":
      return "⏭️";
    case "unavailable":
      return "⚠️";
    case "approval_required":
      return "🔒";
  }
}

export interface Summary {
  pass: number;
  fail: number;
  other: number;
}

export function summarize(results: VerificationResult[]): Summary {
  const summary: Summary = { pass: 0, fail: 0, other: 0 };
  for (const r of results) {
    if (r.status === "pass") {
      summary.pass++;
    } else if (r.status === "fail") {
      summary.fail++;
    } else {
      summary.other++;
    }
  }
  return summary;
}

export function summaryLine(
  title: string,
  results: VerificationResult[],
): string {
  const s = summarize(results);
  const verdict = s.fail > 0 ? "FAIL" : "PASS";
  return `${title}: ${verdict} — ${s.pass} passed, ${s.fail} failed, ${s.other} skipped/unavailable/approval-required`;
}

function escapeHtml(s: string): string {
  return s
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

// resultsHtml renders a full webview document for a set of verification
// results. Styling leans on VSCode's theme variables so it follows the
// editor's light/dark theme.
export function resultsHtml(
  title: string,
  results: VerificationResult[],
): string {
  const rows = results
    .map((r) => {
      const detail = r.message ?? r.reason ?? "";
      return `<tr data-testid="result-row-${escapeHtml(r.name)}">
  <td class="status">${statusIcon(r.status)} ${escapeHtml(r.status)}</td>
  <td class="name">${escapeHtml(r.name)}</td>
  <td class="category">${escapeHtml(r.category)}</td>
  <td class="detail"><pre>${escapeHtml(detail)}</pre></td>
</tr>`;
    })
    .join("\n");
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<style>
  body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); }
  h1 { font-size: 1.2em; }
  table { border-collapse: collapse; width: 100%; }
  th, td { text-align: left; vertical-align: top; padding: 4px 10px; border-bottom: 1px solid var(--vscode-editorWidget-border); }
  td.status { white-space: nowrap; }
  pre { margin: 0; white-space: pre-wrap; word-break: break-word; font-family: var(--vscode-editor-font-family); }
</style>
</head>
<body>
<h1 data-testid="results-title">${escapeHtml(summaryLine(title, results))}</h1>
<table data-testid="results-table">
<thead><tr><th>Status</th><th>Check</th><th>Category</th><th>Detail</th></tr></thead>
<tbody>
${rows}
</tbody>
</table>
</body>
</html>`;
}

export function planText(title: string, checks: PlanCheckSpec[]): string {
  const lines = checks.map((c) => {
    const extras = [
      c.RequiredTool ? `requires ${c.RequiredTool}` : "",
      c.Networked ? "networked" : "",
      c.Reason ?? "",
    ]
      .filter(Boolean)
      .join("; ");
    return `- ${c.Name} [${c.Category}]: ${c.Command}${extras ? `  (${extras})` : ""}`;
  });
  return `${title} (${checks.length} checks)\n${lines.join("\n")}`;
}
