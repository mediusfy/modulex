// Extension-host integration test launcher: downloads a real VSCode build
// and runs suite/index.ts inside it, with the modulex repository root as
// the opened workspace (so server auto-detection finds tools/mcpserver).
// Run with: npm run test:integration
import * as path from "node:path";
import { runTests } from "@vscode/test-electron";

async function main(): Promise<void> {
  const extensionDevelopmentPath = path.resolve(__dirname, "..", "..", "..");
  const extensionTestsPath = path.resolve(__dirname, "suite", "index");
  const workspace = path.resolve(extensionDevelopmentPath, "..", "..");
  await runTests({
    extensionDevelopmentPath,
    extensionTestsPath,
    launchArgs: [workspace, "--disable-extensions"],
  });
}

main().catch((err) => {
  console.error("integration tests failed:", err);
  process.exit(1);
});
