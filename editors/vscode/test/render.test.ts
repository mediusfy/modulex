import assert from "node:assert/strict";
import test from "node:test";
import { resultsHtml, statusIcon, summaryLine } from "../src/render";
import {
  planCheckToCheckSpecIn,
  type PlanCheckSpec,
  type VerificationResult,
  type VerificationStatus,
} from "../src/toolSurface";

test("statusIcon covers every status", () => {
  const rows: Array<{ status: VerificationStatus; want: string }> = [
    { status: "pass", want: "✅" },
    { status: "fail", want: "❌" },
    { status: "skipped", want: "⏭️" },
    { status: "unavailable", want: "⚠️" },
    { status: "approval_required", want: "🔒" },
  ];
  for (const row of rows) {
    assert.equal(statusIcon(row.status), row.want, row.status);
  }
});

test("summaryLine buckets statuses and picks the verdict", () => {
  const rows: Array<{
    name: string;
    results: VerificationResult[];
    want: string;
  }> = [
    {
      name: "all pass",
      results: [
        { name: "build", category: "full", status: "pass" },
        { name: "lint", category: "full", status: "skipped" },
      ],
      want: "gates: PASS — 1 passed, 0 failed, 1 skipped/unavailable/approval-required",
    },
    {
      name: "any fail flips the verdict",
      results: [
        { name: "build", category: "full", status: "pass" },
        { name: "test", category: "full", status: "fail" },
      ],
      want: "gates: FAIL — 1 passed, 1 failed, 0 skipped/unavailable/approval-required",
    },
  ];
  for (const row of rows) {
    assert.equal(summaryLine("gates", row.results), row.want, row.name);
  }
});

test("planCheckToCheckSpecIn maps PascalCase plan checks to snake_case input", () => {
  const rows: Array<{ name: string; in: PlanCheckSpec; want: object }> = [
    {
      name: "full mapping",
      in: {
        Name: "lint",
        Command: "make lint",
        Category: "full",
        Reason: "required",
        RequiredTool: "golangci-lint",
        Networked: true,
      },
      want: {
        name: "lint",
        command: "make lint",
        category: "full",
        reason: "required",
        required_tool: "golangci-lint",
        networked: true,
      },
    },
    {
      name: "optional fields omitted, not sent as empty",
      in: { Name: "build", Command: "make build", Category: "full" },
      want: { name: "build", command: "make build", category: "full" },
    },
  ];
  for (const row of rows) {
    assert.deepEqual(planCheckToCheckSpecIn(row.in), row.want, row.name);
  }
});

test("resultsHtml escapes untrusted check output", () => {
  const html = resultsHtml("review", [
    {
      name: "secret-scan",
      category: "secret_scan",
      status: "fail",
      message: '<script>alert("x")</script>',
    },
  ]);
  assert.ok(!html.includes('<script>alert("x")</script>'));
  assert.ok(html.includes("&lt;script&gt;"));
});
