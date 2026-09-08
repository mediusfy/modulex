import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import test from "node:test";
import { resolveServerSpec } from "../src/server";

test("resolveServerSpec resolution order", () => {
  const modulexCheckout = fs.mkdtempSync(
    path.join(os.tmpdir(), "modulex-vscode-test-"),
  );
  fs.mkdirSync(
    path.join(modulexCheckout, "tools", "mcpserver", "cmd", "mcpserver"),
    {
      recursive: true,
    },
  );
  const plainRepo = fs.mkdtempSync(
    path.join(os.tmpdir(), "modulex-vscode-test-"),
  );

  const rows = [
    {
      name: "explicit setting wins even in a modulex checkout",
      root: modulexCheckout,
      configured: ["/usr/local/bin/mcpserver", "--flag"],
      want: {
        command: "/usr/local/bin/mcpserver",
        args: ["--flag"],
        cwd: modulexCheckout,
      },
    },
    {
      name: "modulex checkout auto-detects go run",
      root: modulexCheckout,
      configured: [],
      want: {
        command: "go",
        args: ["run", "-C", "tools/mcpserver", "./cmd/mcpserver"],
        cwd: modulexCheckout,
      },
    },
    {
      name: "plain repository with no setting resolves nothing",
      root: plainRepo,
      configured: [],
      want: undefined,
    },
  ];
  try {
    for (const row of rows) {
      assert.deepEqual(
        resolveServerSpec(row.root, row.configured),
        row.want,
        row.name,
      );
    }
  } finally {
    fs.rmSync(modulexCheckout, { recursive: true, force: true });
    fs.rmSync(plainRepo, { recursive: true, force: true });
  }
});
