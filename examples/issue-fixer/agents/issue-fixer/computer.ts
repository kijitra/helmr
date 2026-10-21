import { template, image } from "@helmr/sdk"

export const issueFixerComputer = template({
  id: "issue-fixer",
  image: image("issue-fixer")
    .from("node:24.22.1-bookworm-slim@sha256:1681862882da07ee5476fee047bcf34138833f1df42633bb25bcb0f438d5bf3d")
    .run(["sh", "-ceu", "apt-get update && apt-get install -y --no-install-recommends git ripgrep ca-certificates && rm -rf /var/lib/apt/lists/*"])
    .env("ISSUE_FIXER_REPOSITORY", "/workspace/repository")
    .workdir("/workspace/repository"),
  resources: { cpu: 2, memory: "4GiB" },
  // Add preparation for your repository and stable Secret ID bindings before use.
})
