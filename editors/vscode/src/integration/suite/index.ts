// Runs inside the VSCode extension host (no test framework needed: the
// harness calls run() and a rejected promise fails the run). Verifies the
// extension activates, registers its full command surface, and can drive
// the real MCP server end to end via the Discover Repository command.
import * as assert from "node:assert/strict";
import * as vscode from "vscode";

const EXPECTED_COMMANDS = [
  "modulex.reviewDiff",
  "modulex.runFocusedVerification",
  "modulex.runFullGates",
  "modulex.discoverRepository",
  "modulex.readContract",
  "modulex.createHandoff",
  "modulex.restartServer",
];

export async function run(): Promise<void> {
  const extension = vscode.extensions.getExtension("mediusfy.modulex");
  assert.ok(extension, "extension mediusfy.modulex not found in the host");
  await extension.activate();
  assert.ok(extension.isActive, "extension did not activate");

  const commands = await vscode.commands.getCommands(true);
  for (const cmd of EXPECTED_COMMANDS) {
    assert.ok(commands.includes(cmd), `command ${cmd} not registered`);
  }

  // Full round-trip through the real server (auto-detected go run against
  // the opened modulex checkout): the command opens the discovery report
  // as a JSON document beside the active editor.
  await vscode.commands.executeCommand("modulex.discoverRepository");
  const opened = await waitFor(
    () =>
      vscode.window.visibleTextEditors.find(
        (e) => e.document.languageId === "json" && e.document.getText().length > 2,
      ),
    120_000,
  );
  assert.ok(opened, "discover_repository did not open a JSON document");
  const report = JSON.parse(opened.document.getText()) as { modules?: unknown[] };
  assert.ok(Array.isArray(report.modules), "discovery report has no modules list");
}

async function waitFor<T>(probe: () => T | undefined, timeoutMs: number): Promise<T | undefined> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = probe();
    if (value) {
      return value;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  return undefined;
}
