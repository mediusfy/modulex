import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as path from "node:path";
import test from "node:test";
import { McpClient } from "../src/mcpClient";
import { resolveServerSpec } from "../src/server";

// Opt-in end-to-end test against the real Go MCP server (mirrors the
// IntelliJ RealServerE2ETest): spawns tools/mcpserver via go run from the
// repository root and drives it with the extension's own client. Run with:
//
//   MODULEX_E2E=1 npm test
//
// Skipped otherwise, because it builds and runs the Go server.
// __dirname is out/test at run time (tests run compiled), so the repo root
// is four levels up: out/test -> vscode -> editors -> repo.
const repoRoot = path.resolve(__dirname, "..", "..", "..", "..");
const enabled =
  process.env.MODULEX_E2E === "1" &&
  fs.existsSync(path.join(repoRoot, "tools", "mcpserver"));

test("real server round-trip", { skip: !enabled }, async () => {
  const spec = resolveServerSpec(repoRoot, []);
  assert.ok(spec, "modulex checkout not auto-detected");
  const client = await McpClient.start(spec, () => {});
  try {
    const disc = await client.callTool<{ repository: object }>("discover_repository", {
      root: repoRoot,
    });
    assert.ok(disc.repository, "discover_repository returned no repository");

    const rec = await client.callTool<{ plan: { FullGates: unknown[] } }>(
      "recommend_verification",
      { changed_files: [] },
    );
    assert.ok(rec.plan.FullGates.length > 0, "expected full gates in the plan");

    const review = await client.callTool<{
      results: Array<{ name: string; status: string }>;
    }>("review_diff", { root: repoRoot, base_ref: "HEAD", head_ref: "HEAD" });
    assert.ok(review.results.length > 0, "expected review results");
    const known = new Set(["pass", "fail", "skipped", "unavailable", "approval_required"]);
    for (const r of review.results) {
      assert.ok(known.has(r.status), `check ${r.name} has unknown status ${r.status}`);
    }
  } finally {
    client.dispose();
  }
});
