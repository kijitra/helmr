import { fixtureValue } from "../../support/runtime-mcp"
import { agent, template, image } from "@helmr/sdk"
import { readFile, writeFile } from "node:fs/promises"

export const verificationComputer = template({
  id: "verification",
  image: image("verification").from("node:24.22.1-bookworm-slim@sha256:1681862882da07ee5476fee047bcf34138833f1df42633bb25bcb0f438d5bf3d").workdir("/sandbox"),
  resources: { cpu: 1, memory: "1GiB" },
})

export const verificationAgent = agent({
  id: "verification-agent",
  template: verificationComputer,
  maxTurnDuration: "2m",
  turn: async (turn, ctx) => {
    const input = fixtureValue(turn.input)
    if (input === null || typeof input !== "object" || Array.isArray(input) ||
        !("marker" in input) || typeof input["marker"] !== "string" || input["marker"].length === 0) {
      throw new Error("A nonempty marker is required")
    }
    await writeFile("verification-marker.txt", input["marker"])
    const observed = await readFile("verification-marker.txt", "utf8")
    if (observed !== input["marker"]) throw new Error("guest filesystem round trip failed")
    return { marker: observed, turnId: turn.id, sessionId: ctx.session.id, computerId: ctx.computer.id }
  },
})
