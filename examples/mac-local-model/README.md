# Mac-local model Agent

This development example calls the M3 Mac's existing Ollama model through Helmr's
Session-bound MCP transport. It does not connect the Agent VM directly to the
Mac's private IP, expose Ollama on the LAN, or use a paid model API.

The development control plane must explicitly enable `infer_local_model` for one
Environment, using `HELMR_LOCAL_MODEL_ENVIRONMENT_ID` and a literal loopback
`HELMR_LOCAL_MODEL_ENDPOINT`, for example `http://127.0.0.1:11434`. The model is
fixed to `qwen3.8:27b`; install neither a replacement model nor another Ollama
server as part of this example. A loopback SSH reverse forward connects the Linux
control plane to the existing Mac service. The production entrypoint leaves the
gateway disabled.

Use the [Lima setup and qualification notes](../../docs/implementation/macos-lima-runtime.md)
to prepare the ARM64 Worker, local services and verified runtime. A canonical
ARM64 bundle builder is also required. The public CLI installation is not a
substitute for the development release's ARM64 builder configuration.

Build and deploy using an API key scoped to the dedicated local test Environment:

```sh
helmr build . -o .helmr/deployment-bundle
helmr deploy --bundle .helmr/deployment-bundle
helmr session create mac-test --text '日本語で短く自己紹介してください。' --json
helmr session enqueue SESSION_ID --text ask --json
```

Credentials belong in private local configuration, never in the Agent source or
shell arguments. In the M3 acceptance run, the SDK and protocol sources were
copied from this checkout into a private build workspace and resolved with Bun
workspaces. The example manifest follows the standalone examples' registry
installation convention; using a registry SDK is not the same source-provenance
claim as that acceptance build.

The `ask` input creates a text question. Answer it in the console or with
`helmr session ask answer` (see `--help` for the Session/Turn/Ask identifiers).
The Agent sends the supplied answer to the same local model and responds with its
generated text. Other inputs go directly to that model as individual prompts;
this example does not implement a multi-message model conversation or an
autonomous tool-execution loop.

`/sandbox/proof.json` records the model, generated response, Turn count and the
initial process's random marker, PID and initialization count. Comparing those
fields across Turns after hibernation distinguishes RAM continuation from rerun
initialization. A new Session on an existing Computer intentionally increments
the disk initialization counter.

The MCP request uses a disposable HTTP connection, a bounded response and an
explicitly cleared deadline. This allows the completed Turn to hibernate without
retaining an HTTP pool or timer. Inference itself has one admission slot, a
40-second deadline and periodic execution-authority checks in the control plane.
HTTP cancellation is not a guarantee of immediate GPU cessation in Ollama.
