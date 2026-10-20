import { agent, template, image } from "@helmr/sdk"
import { readFile, writeFile } from "node:fs/promises"
import { randomUUID } from "node:crypto"
import { request as httpRequest } from "node:http"

const machine = template({
  id: "mac-test-computer",
  image: image("mac-test-image")
    .from("docker.io/library/debian@sha256:a2ca65377c251208de42e1a2722cb949211236c161b634f6f3663754e2d06850")
    .workdir("/sandbox"),
  resources: { cpu: 1, memory: "1GiB", disk: "32GiB" },
})

// Use a disposable connection and clear the deadline before completing the
// Turn. A retained HTTP pool or AbortSignal.timeout would block RAM capture.
async function callLocalModel(
  options: { url: string; headers: Readonly<{ Authorization: string }> },
  turnId: string,
  prompt: string,
) {
  let deadline: ReturnType<typeof setTimeout> | undefined
  const raw = await new Promise<string>((resolve, reject) => {
    let complete = false
    let body = ""
    const request = httpRequest(options.url, {
      method: "POST", agent: false,
      headers: {
        ...options.headers,
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
        "MCP-Protocol-Version": "2025-11-25",
        "Connection": "close",
      },
    }, response => {
      if (response.statusCode !== 200) {
        request.destroy(new Error(`Local model MCP HTTP ${response.statusCode}`))
        return
      }
      const chunks: Buffer[] = []
      let size = 0
      response.on("data", (chunk: Buffer) => {
        size += chunk.length
        if (size > 128 * 1024) request.destroy(new Error("Local model response exceeds limit"))
        else chunks.push(chunk)
      })
      response.once("error", error => request.destroy(error))
      response.once("end", () => {
        body = Buffer.concat(chunks).toString("utf8")
        complete = true
      })
    })
    request.once("error", reject)
    request.once("close", () => complete ? resolve(body) : reject(new Error("Local model response was truncated")))
    deadline = setTimeout(() => request.destroy(new Error("Local model deadline exceeded")), 44000)
    request.end(JSON.stringify({
      jsonrpc: "2.0", id: turnId, method: "tools/call",
      params: { name: "infer_local_model", arguments: { turnId, prompt } },
    }))
  }).finally(() => { if (deadline !== undefined) clearTimeout(deadline) })
  const rpc = JSON.parse(raw) as { error?: unknown; result?: { isError?: boolean; content?: { text?: string }[] } }
  if (rpc.error || rpc.result?.isError || !rpc.result?.content?.[0]?.text) throw new Error("Local model MCP call failed")
  const generated = JSON.parse(rpc.result.content[0].text) as { text: string; model: string }
  if (typeof generated.text !== "string" || !generated.text || generated.model !== "qwen3.8:27b") throw new Error("Invalid local model response")
  return generated
}

export const macTest = agent({
  id: "mac-test", template: machine, maxTurnDuration: "10m", closeAfterIdle: "30m",
  async init(context) {
    let count = 0
    try { count = Number(await readFile("/sandbox/init-count.txt", "utf8")) }
    catch (error) { if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error }
    if (!Number.isInteger(count) || count < 0) throw new Error("Invalid init counter")
    await writeFile("/sandbox/init-count.txt", String(++count))
    return { nonce: randomUUID(), pid: process.pid, initCount: count, turns: 0, recovery: context.recovery.kind }
  },
  async turn(turn, context) {
    const state = context.initResult
    state.turns++
    const input = turn.input.map(part => part.text).join("")
    await turn.output.write(`Mac runtime turn ${state.turns}: ${input}`)
    let answer: unknown = null
    if (input === "ask") {
      const response = await turn.ask({ type: "text", prompt: "Mac上のHelmrからの確認です。続行する合言葉を入力してください。" })
      answer = response.answer
    }
    const options = await context.mcp.getConnectionOptions()
    const generated = await callLocalModel(options, turn.id, input === "ask"
      ? `ユーザーからの回答は ${JSON.stringify(answer)} です。日本語で短く返答してください。` : input)
    const proof = { ...state, model: generated.model, response: generated.text, input, answer,
      architecture: process.arch, session: context.session.id, computer: context.computer.id, turn: turn.id }
    await writeFile("/sandbox/proof.json", JSON.stringify(proof) + "\n")
    await turn.respond(generated.text)
    return proof
  },
})
