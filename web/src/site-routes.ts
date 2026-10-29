// The pages the site map (site-map.ts) offers, with the facts about them that
// no other module owns: a short description per page, the routes that are
// only aliases, where the IDs a page needs come from, and the work reader's
// parameters, which the page shell (ui.py) reads inline.
//
// The served routes are isUIPage in internal/archive/api.go; the shell's own
// lists are ROUTES, TL1_PAGES and the data-settings-route panels in ui.py.
// web/tests/unit/site-map compares every one of them with these.

// Top-level sections in browse order. Titles come from the page's own
// navigation (appSections, tl1Pages, the settings panels); these say what a
// section is for.
export const siteSections = [
  { path: "home", route: "/", description: "The home page: recent activity and suggested cards." },
  { path: "cards", route: "/cards", description: "The card library: every prebuilt question, each opening a dashboard. Filter by text, data source, topic or kind." },
  { path: "library", route: "/library", description: "Search conversations and work: text search across messages, or filter the work table." },
  { path: "analyze", route: "/analyze", description: "Dashboards per topic (tokens, tools, human words …) and query tables per dataset." },
  { path: "findings", route: "/findings", description: "Optimization findings: open, watched, won and dismissed, with evidence and prompts." },
  { path: "tl1", route: "/tl1", description: "TL1 workflow analytics: overview, configurations, follow-ups, quality and runs." },
  { path: "work", route: "/work/{work_id}", description: "One work item (a workspace) with its conversations, reader and delegation explorer." },
  { path: "settings", route: "/settings", description: "Library health and settings pages." },
] as const;

// Descriptions of pages below a section, where the page's own module has none.
export const pageDescriptions: Record<string, string> = {
  "analyze/query": "Query tables over each Analyze dataset, filtered by where terms.",
  "findings/table": "Every finding as a filterable table.",
  "findings/settings": "Which detectors run and how findings are ranked.",
};

// Served routes that only lead elsewhere. The site map never offers them.
export const legacyRoutes: Record<string, string> = {
  "/home": "/",
  "/sources": "/settings/sources",
  "/activity": "/settings",
  "/health": "/settings",
  "/mcp": "/analyze?analyze_topic=mcp",
  "/tools": "/analyze?analyze_mode=query&analyze_data=tool_calls",
  "/settings/optimization": "/findings?view=settings",
};
// Settings pages a library may lack: Libraries exists only in the managed app.
export const optionalSettingsPages: Record<string, string> = { "/settings/libraries": "Only in the Pharos app, which manages its libraries." };

// Which Pharos MCP tool yields each ID a link needs, and the field holding it.
// The test checks every tool is registered in internal/archive.
export const idSources: Record<string, Array<{ tool: string; field: string }>> = {
  work_id: [{ tool: "search_work", field: "items[].workspace_id" }, { tool: "search_conversations", field: "items[].workspace_id" }, { tool: "search_text", field: "items[].workspace_id" }],
  conversation_id: [{ tool: "search_conversations", field: "items[].conversation_id" }, { tool: "get_work_detail", field: "conversations.items[].conversation_id" }, { tool: "search_messages", field: "items[].conversation_id" }],
  message_id: [{ tool: "search_messages", field: "items[].message_id" }, { tool: "search_conversation_passages", field: "items[].message_id" }, { tool: "get_conversation_messages", field: "items[].message_id" }],
  finding_id: [{ tool: "list_findings", field: "items[].id" }],
  repository: [{ tool: "list_repositories", field: "items[].repository" }],
};

// The work reader's parameters, in the order conversationRowURL writes them.
// The shell reads them inline (ui.py: updateConversationURL, routeConversation).
// requires and conflicts name the other parameters each one depends on.
export const readerParams = [
  { name: "conversation", id: "conversation_id", description: "The conversation to open in the reader." },
  { name: "message", id: "message_id", description: "A message to scroll to and highlight; needs conversation.", requires: ["conversation"] },
  { name: "call", id: "", description: "A tool call within that message to highlight; needs message.", requires: ["message"] },
  { name: "at", id: "", description: "An ISO time to scroll to when no message is given.", requires: ["conversation"], conflicts: ["message"] },
] as const;
