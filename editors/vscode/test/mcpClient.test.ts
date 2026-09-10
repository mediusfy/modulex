import assert from "node:assert/strict";
import test from "node:test";
import { McpClient, McpError, type ServerSpec } from "../src/mcpClient";

// A fake MCP server speaking newline-delimited JSON-RPC over stdio, with one
// canned behavior per tool name, so each table row below exercises one
// client path.
const FAKE_SERVER = `
const rl = require("node:readline").createInterface({ input: process.stdin });
const send = (o) => process.stdout.write(JSON.stringify(o) + "\\n");
rl.on("line", (l) => {
  const m = JSON.parse(l);
  if (m.method === "initialize") {
    send({ jsonrpc: "2.0", id: m.id, result: { protocolVersion: "2024-11-05", capabilities: {}, serverInfo: { name: "fake" } } });
  } else if (m.method === "tools/call") {
    const name = m.params.name;
    if (name === "structured") {
      send({ jsonrpc: "2.0", id: m.id, result: { content: [{ type: "text", text: "{\\"ok\\":true}" }], structuredContent: { ok: true, from: "structured" } } });
    } else if (name === "textonly") {
      send({ jsonrpc: "2.0", id: m.id, result: { content: [{ type: "text", text: "{\\"ok\\":true,\\"from\\":\\"text\\"}" }] } });
    } else if (name === "toolerror") {
      send({ jsonrpc: "2.0", id: m.id, result: { isError: true, content: [{ type: "text", text: "tool exploded" }] } });
    } else if (name === "rpcerror") {
      send({ jsonrpc: "2.0", id: m.id, error: { code: -32000, message: "rpc exploded" } });
    }
  }
});
`;

function fakeServerSpec(): ServerSpec {
  return { command: process.execPath, args: ["-e", FAKE_SERVER], cwd: "." };
}

interface Row {
  name: string;
  tool: string;
  want?: unknown;
  wantErr?: string;
}

const rows: Row[] = [
  {
    name: "prefers structuredContent",
    tool: "structured",
    want: { ok: true, from: "structured" },
  },
  {
    name: "falls back to parsing text content",
    tool: "textonly",
    want: { ok: true, from: "text" },
  },
  {
    name: "surfaces isError results as errors",
    tool: "toolerror",
    wantErr: "tool exploded",
  },
  {
    name: "surfaces JSON-RPC errors as errors",
    tool: "rpcerror",
    wantErr: "rpc exploded",
  },
];

for (const row of rows) {
  test(`callTool ${row.name}`, async () => {
    const client = await McpClient.start(fakeServerSpec(), () => {});
    try {
      if (row.wantErr !== undefined) {
        await assert.rejects(client.callTool(row.tool, {}), (err: unknown) => {
          assert.ok(err instanceof McpError);
          assert.match(err.message, new RegExp(row.wantErr as string));
          return true;
        });
      } else {
        assert.deepEqual(await client.callTool(row.tool, {}), row.want);
      }
    } finally {
      client.dispose();
    }
  });
}

test("pending calls reject when the server exits", async () => {
  const client = await McpClient.start(fakeServerSpec(), () => {});
  // "unknown" gets no response from the fake server; disposing must reject
  // the pending promise instead of leaving it hanging forever.
  const pending = client.callTool("unknown", {});
  client.dispose();
  await assert.rejects(pending, McpError);
});
