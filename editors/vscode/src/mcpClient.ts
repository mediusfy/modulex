// Minimal MCP client over the stdio transport: newline-delimited JSON-RPC
// 2.0, matching the Go SDK's mcp.StdioTransport that tools/mcpserver runs.
// Hand-rolled instead of depending on an SDK so the extension has zero
// runtime dependencies and works fully offline.

import { spawn, type ChildProcess } from "node:child_process";
import * as readline from "node:readline";

export interface ServerSpec {
  command: string;
  args: string[];
  cwd: string;
}

interface JsonRpcResponse {
  jsonrpc: "2.0";
  id?: number;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
  method?: string;
}

interface ToolCallResult {
  isError?: boolean;
  structuredContent?: unknown;
  content?: Array<{ type: string; text?: string }>;
}

const PROTOCOL_VERSION = "2024-11-05";

export class McpError extends Error {}

export class McpClient {
  private nextId = 1;
  private readonly pending = new Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >();
  private stderrTail: string[] = [];
  private exited = false;

  private constructor(
    private readonly proc: ChildProcess,
    private readonly log: (line: string) => void,
  ) {}

  static async start(
    spec: ServerSpec,
    log: (line: string) => void,
  ): Promise<McpClient> {
    const proc = spawn(spec.command, spec.args, {
      cwd: spec.cwd,
      stdio: ["pipe", "pipe", "pipe"],
    });
    const client = new McpClient(proc, log);
    client.wire();
    await client.request("initialize", {
      protocolVersion: PROTOCOL_VERSION,
      capabilities: {},
      clientInfo: { name: "modulex-vscode", version: "0.1.0" },
    });
    client.notify("notifications/initialized", {});
    return client;
  }

  private wire(): void {
    const stdout = this.proc.stdout;
    if (stdout) {
      readline.createInterface({ input: stdout }).on("line", (line) => {
        this.onLine(line);
      });
    }
    const stderr = this.proc.stderr;
    if (stderr) {
      readline.createInterface({ input: stderr }).on("line", (line) => {
        this.stderrTail.push(line);
        if (this.stderrTail.length > 50) {
          this.stderrTail.shift();
        }
        this.log(`[server] ${line}`);
      });
    }
    this.proc.on("error", (err) => {
      this.failAll(
        new McpError(`modulex MCP server failed to start: ${err.message}`),
      );
    });
    this.proc.on("exit", (code) => {
      this.exited = true;
      const detail = this.stderrTail.length
        ? `; server said: ${this.stderrTail.slice(-5).join(" | ")}`
        : "";
      this.failAll(
        new McpError(
          `modulex MCP server exited with code ${code ?? "unknown"}${detail}`,
        ),
      );
    });
  }

  private onLine(line: string): void {
    const trimmed = line.trim();
    if (trimmed === "") {
      return;
    }
    let msg: JsonRpcResponse;
    try {
      msg = JSON.parse(trimmed) as JsonRpcResponse;
    } catch {
      this.log(`[server stdout, not JSON-RPC] ${trimmed}`);
      return;
    }
    if (typeof msg.id !== "number") {
      return; // notification from the server; nothing to route
    }
    const waiter = this.pending.get(msg.id);
    if (!waiter) {
      return;
    }
    this.pending.delete(msg.id);
    if (msg.error) {
      waiter.reject(new McpError(msg.error.message));
    } else {
      waiter.resolve(msg.result);
    }
  }

  private failAll(err: Error): void {
    for (const waiter of this.pending.values()) {
      waiter.reject(err);
    }
    this.pending.clear();
  }

  private write(msg: object): void {
    if (this.exited || !this.proc.stdin || !this.proc.stdin.writable) {
      throw new McpError("modulex MCP server is not running");
    }
    this.proc.stdin.write(`${JSON.stringify(msg)}\n`);
  }

  private request(method: string, params: object): Promise<unknown> {
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      try {
        this.write({ jsonrpc: "2.0", id, method, params });
      } catch (err) {
        this.pending.delete(id);
        reject(err instanceof Error ? err : new McpError(String(err)));
      }
    });
  }

  private notify(method: string, params: object): void {
    this.write({ jsonrpc: "2.0", method, params });
  }

  get alive(): boolean {
    return !this.exited;
  }

  async callTool<T>(name: string, args: object): Promise<T> {
    const raw = (await this.request("tools/call", {
      name,
      arguments: args,
    })) as ToolCallResult;
    const text = (raw.content ?? [])
      .filter((c) => c.type === "text" && typeof c.text === "string")
      .map((c) => c.text)
      .join("\n");
    if (raw.isError) {
      throw new McpError(text || `tool ${name} failed`);
    }
    if (raw.structuredContent !== undefined) {
      return raw.structuredContent as T;
    }
    try {
      return JSON.parse(text) as T;
    } catch {
      throw new McpError(
        `tool ${name} returned no structured content and non-JSON text: ${text}`,
      );
    }
  }

  dispose(): void {
    this.exited = true;
    this.failAll(new McpError("modulex MCP client disposed"));
    this.proc.kill();
  }
}
