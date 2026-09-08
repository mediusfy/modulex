import { join } from "node:path";

// opencode adapter for the modulex agent pointcuts. Thin by design: every
// decision lives in the tool-agnostic scripts under scripts/agenthooks/,
// shared with the Claude Code (.claude/settings.json) and Antigravity
// (.antigravity/hooks/hooks.json) adapters. See
// docs/planning/agent-pointcuts-guide.md.
//
//   pre    plugin init            -> pre.sh   (repository self-report)
//   guard  tool.execute.before    -> guard.sh (throw blocks the tool call)
//   while  file.edited (debounced)-> while.sh (focused checks on the edit)
//   post   session.idle           -> post.sh  (verify plan for the diff)
export const ModulexPointcuts = async ({ $, directory }) => {
  const hook = (name) => join(directory, "scripts", "agenthooks", name);
  const run = (name, payload) =>
    $({
      cwd: directory,
      env: {
        ...process.env,
        MODULEX_HOOK_PAYLOAD: payload ? JSON.stringify(payload) : "",
      },
    })`bash ${hook(name)}`
      .quiet()
      .nothrow();

  const pre = await run("pre.sh");
  if (pre.exitCode === 0) console.log(pre.stdout.toString());

  let posting = false;
  let debounce;

  return {
    "tool.execute.before": async (input, output) => {
      const res = await run("guard.sh", {
        tool_name: input.tool,
        tool_input: output.args ?? {},
      });
      if (res.exitCode === 2) {
        throw new Error(
          res.stderr.toString() || "blocked by modulex guard pointcut",
        );
      }
    },

    event: async ({ event }) => {
      if (event.type === "file.edited") {
        const file = event.properties?.file;
        clearTimeout(debounce);
        debounce = setTimeout(async () => {
          const res = await run("while.sh", {
            tool_input: { file_path: file },
          });
          if (res.exitCode === 2) console.error(res.stderr.toString());
        }, 1500);
      } else if (event.type === "session.idle" && !posting) {
        posting = true;
        try {
          const res = await run("post.sh", {});
          const text =
            res.exitCode === 2 ? res.stderr.toString() : res.stdout.toString();
          if (text.trim())
            (res.exitCode === 2 ? console.error : console.log)(text);
        } finally {
          posting = false;
        }
      }
    },
  };
};
