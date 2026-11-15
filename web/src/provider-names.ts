// Display names for the stored keys of sources and providers. The provider
// registry (internal/archive/provider_registry.go) owns them: GET
// /api/providers serves them, testdata/providers.json records that response,
// and source-setup.test.mjs holds these maps to it. Three key spaces are
// kept apart here:
//   - a source kind (workspaces.source_kind, a [[sources]] kind): sourceName;
//   - a conversation's provider (conversations.provider): providerName;
//   - an agent app (the stored harness, "claude-code/cli") has no display
//     names yet and is not named by either.
// A key this build does not know is shown as stored.
const sources: Record<string, string> = {
  claude: "Claude Code", codex: "Codex", copilot: "Copilot CLI", opencode: "OpenCode", cline: "Cline", "vscode-copilot": "VS Code Copilot",
  cursor: "Cursor", "cursor-cli": "Cursor CLI", zed: "Zed", antigravity: "Antigravity", conductor: "Conductor", tl1: "TL1", openclaw: "OpenClaw",
  hermes: "Hermes Agent", canonical: "Canonical export", "tl1-export": "TL1 export", chatgpt: "ChatGPT", "claude-ai": "Claude.ai",
  "claude-ai-settings": "Claude Chat settings",
};
// "claude" is the provider of Claude Code sessions and of Claude.ai chats
// alike, so the provider is named for neither source.
const providers: Record<string, string> = { claude: "Claude", gemini: "Gemini" };
export const sourceName = (kind: string) => Object.hasOwn(sources, kind) ? sources[kind] : kind;
export const providerName = (provider: string) => Object.hasOwn(providers, provider) ? providers[provider] : sourceName(provider);
// A response row's provider: the display name the server sent beside the
// stored key when it did, else this build's name for the key.
export const rowProviderName = (row: { provider?: string | null; provider_name?: string | null } | null | undefined, fallback = "") =>
  row?.provider_name || (row?.provider ? providerName(row.provider) : fallback);
