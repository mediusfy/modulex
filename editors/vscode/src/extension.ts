// Modulex VSCode extension: a client of the local read-only modulex MCP
// server (tools/mcpserver), per ADR-0035 plan step 5 (Jira MOD-81). Every
// command runs against the local checkout over stdio — no hosted backend,
// fully offline with allow_network=false (the default).

import { execFile } from "node:child_process";
import { promisify } from "node:util";
import * as vscode from "vscode";
import { McpClient } from "./mcpClient";
import { NO_SERVER_GUIDANCE, resolveServerSpec } from "./server";
import { planText, resultsHtml, summaryLine } from "./render";
import {
  TOOL,
  planCheckToCheckSpecIn,
  type CreateHandoffResult,
  type DiscoverRepositoryResult,
  type PlanCheckSpec,
  type ReadContractResult,
  type RecommendVerificationResult,
  type ReviewDiffResult,
  type RunVerificationResult,
  type VerificationResult,
} from "./toolSurface";

const execFileAsync = promisify(execFile);

// clientPromise (not a resolved client) is the shared state: two commands
// racing during server startup must share one in-flight start, or each
// spawns its own `go run` server and the loser leaks as an orphan.
let clientPromise: Promise<McpClient> | undefined;
let output: vscode.OutputChannel;
// lastResults feeds create_handoff's verification field, so a handoff made
// after a review/verification run records what actually ran.
let lastResults: VerificationResult[] = [];

function workspaceRoot(): string | undefined {
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
}

async function getClient(): Promise<McpClient> {
  if (clientPromise) {
    try {
      const existing = await clientPromise;
      if (existing.alive) {
        return existing;
      }
      existing.dispose();
    } catch {
      // The previous start failed; fall through and start fresh.
    }
    clientPromise = undefined;
  }
  const root = workspaceRoot();
  if (!root) {
    throw new Error("Modulex needs an open workspace folder.");
  }
  const configured = vscode.workspace
    .getConfiguration("modulex")
    .get<string[]>("server.command", []);
  const spec = resolveServerSpec(root, configured);
  if (!spec) {
    throw new Error(NO_SERVER_GUIDANCE);
  }
  output.appendLine(
    `starting MCP server: ${spec.command} ${spec.args.join(" ")} (cwd ${spec.cwd})`,
  );
  const starting = McpClient.start(spec, (line) => output.appendLine(line));
  clientPromise = starting;
  try {
    return await starting;
  } catch (err) {
    if (clientPromise === starting) {
      clientPromise = undefined;
    }
    throw err;
  }
}

function disposeClient(): void {
  const pending = clientPromise;
  clientPromise = undefined;
  void pending?.then((c) => c.dispose()).catch(() => {});
}

async function withProgress<T>(
  title: string,
  task: () => Promise<T>,
): Promise<T> {
  return vscode.window.withProgress(
    {
      location: vscode.ProgressLocation.Notification,
      title,
      cancellable: false,
    },
    task,
  );
}

function showResults(title: string, results: VerificationResult[]): void {
  lastResults = results;
  output.appendLine(summaryLine(title, results));
  output.appendLine(JSON.stringify(results, null, 2));
  const panel = vscode.window.createWebviewPanel(
    "modulexResults",
    title,
    vscode.ViewColumn.Beside,
    {},
  );
  panel.webview.html = resultsHtml(title, results);
  const summary = summaryLine(title, results);
  if (results.some((r) => r.status === "fail")) {
    void vscode.window.showWarningMessage(summary);
  } else {
    void vscode.window.showInformationMessage(summary);
  }
}

async function showJsonDocument(value: unknown): Promise<void> {
  const doc = await vscode.workspace.openTextDocument({
    language: "json",
    content: JSON.stringify(value, null, 2),
  });
  await vscode.window.showTextDocument(doc, vscode.ViewColumn.Beside);
}

function allowNetwork(): boolean {
  return vscode.workspace
    .getConfiguration("modulex")
    .get<boolean>("allowNetwork", false);
}

async function changedFiles(root: string): Promise<string[]> {
  const { stdout } = await execFileAsync("git", ["status", "--porcelain"], {
    cwd: root,
  });
  return stdout
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => line.slice(3).trim())
    .map((p) => {
      const renameArrow = p.indexOf(" -> ");
      return renameArrow === -1 ? p : p.slice(renameArrow + 4);
    });
}

async function reviewDiff(): Promise<void> {
  const defaultBase = vscode.workspace
    .getConfiguration("modulex")
    .get<string>("review.baseRef", "origin/main");
  const baseRef = await vscode.window.showInputBox({
    prompt: "Base ref to review the current checkout against",
    value: defaultBase,
  });
  if (!baseRef) {
    return;
  }
  const c = await getClient();
  const result = await withProgress(
    `Modulex: reviewing diff against ${baseRef}`,
    () =>
      c.callTool<ReviewDiffResult>(TOOL.reviewDiff, {
        root: workspaceRoot(),
        base_ref: baseRef,
        head_ref: "HEAD",
        allow_network: allowNetwork(),
      }),
  );
  showResults(`Review vs ${baseRef}`, result.results);
}

async function runChecks(
  title: string,
  checks: PlanCheckSpec[],
): Promise<void> {
  if (checks.length === 0) {
    void vscode.window.showInformationMessage(`${title}: nothing to run.`);
    return;
  }
  const c = await getClient();
  output.appendLine(planText(title, checks));
  const result = await withProgress(`Modulex: ${title}`, () =>
    c.callTool<RunVerificationResult>(TOOL.runVerification, {
      root: workspaceRoot(),
      checks: checks.map(planCheckToCheckSpecIn),
      allow_network: allowNetwork(),
    }),
  );
  showResults(title, result.results);
}

async function recommendPlan(
  files: string[],
): Promise<RecommendVerificationResult> {
  const c = await getClient();
  return c.callTool<RecommendVerificationResult>(TOOL.recommendVerification, {
    changed_files: files,
  });
}

async function runFocusedVerification(): Promise<void> {
  const root = workspaceRoot();
  if (!root) {
    throw new Error("Modulex needs an open workspace folder.");
  }
  const files = await changedFiles(root);
  if (files.length === 0) {
    void vscode.window.showInformationMessage(
      "Modulex: working tree is clean — no changed files to focus on.",
    );
    return;
  }
  const { plan } = await recommendPlan(files);
  await runChecks("Focused verification", plan.FocusedChecks);
}

async function runFullGates(): Promise<void> {
  const { plan } = await recommendPlan([]);
  await runChecks("Full gates", plan.FullGates);
}

async function discoverRepository(): Promise<void> {
  const c = await getClient();
  const result = await withProgress("Modulex: discovering repository", () =>
    c.callTool<DiscoverRepositoryResult>(TOOL.discoverRepository, {
      root: workspaceRoot(),
    }),
  );
  await showJsonDocument(result.repository);
}

async function readContract(): Promise<void> {
  const c = await getClient();
  const result = await c.callTool<ReadContractResult>(TOOL.readContract, {
    root: workspaceRoot(),
  });
  if (!result.present) {
    void vscode.window.showInformationMessage(
      "Modulex: no modulex.agent.yaml in this repository.",
    );
    return;
  }
  if (result.validation_errors?.length) {
    void vscode.window.showWarningMessage(
      `Modulex: contract has ${result.validation_errors.length} validation error(s) — see the opened document.`,
    );
  }
  await showJsonDocument(result);
}

async function createHandoff(): Promise<void> {
  const agentName = await vscode.window.showInputBox({
    prompt: "Agent name recorded in the handoff envelope",
    value: "vscode",
  });
  if (!agentName) {
    return;
  }
  const c = await getClient();
  const result = await withProgress("Modulex: creating handoff envelope", () =>
    c.callTool<CreateHandoffResult>(TOOL.createHandoff, {
      root: workspaceRoot(),
      agent_name: agentName,
      verification: lastResults,
    }),
  );
  await showJsonDocument(result.envelope);
}

function restartServer(): void {
  disposeClient();
  void vscode.window.showInformationMessage(
    "Modulex: MCP server stopped; it restarts on the next command.",
  );
}

function register(
  context: vscode.ExtensionContext,
  command: string,
  handler: () => Promise<void> | void,
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand(command, async () => {
      try {
        await handler();
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        output.appendLine(`error: ${message}`);
        void vscode.window.showErrorMessage(`Modulex: ${message}`);
      }
    }),
  );
}

export function activate(context: vscode.ExtensionContext): void {
  output = vscode.window.createOutputChannel("Modulex");
  context.subscriptions.push(output);
  register(context, "modulex.reviewDiff", reviewDiff);
  register(context, "modulex.runFocusedVerification", runFocusedVerification);
  register(context, "modulex.runFullGates", runFullGates);
  register(context, "modulex.discoverRepository", discoverRepository);
  register(context, "modulex.readContract", readContract);
  register(context, "modulex.createHandoff", createHandoff);
  register(context, "modulex.restartServer", restartServer);
}

export function deactivate(): void {
  disposeClient();
}
