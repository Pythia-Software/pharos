// Runtime timestamp validation and the generated agent/Go contract share these schemas.
import libraryDocument from "../../schemas/library.schema.json";
import usageDocument from "../../schemas/usage.schema.json";
import writingDocument from "../../schemas/writing.schema.json";
import conversationActivityDocument from "../../schemas/conversation_activity.schema.json";
import writingMessagesDocument from "../../schemas/writing_messages.schema.json";
import mcpCallsDocument from "../../schemas/mcp_calls.schema.json";
import toolsDocument from "../../schemas/tools.schema.json";
import toolCallsDocument from "../../schemas/tool_calls.schema.json";
import skillUsagesDocument from "../../schemas/skill_usages.schema.json";
import tl1AttemptsDocument from "../../schemas/tl1_attempts.schema.json";
import parallelismDocument from "../../schemas/parallelism.schema.json";
import findingsDocument from "../../schemas/findings.schema.json";

export const schemaDocuments = {
  library: libraryDocument, usage: usageDocument, writing: writingDocument, conversation_activity: conversationActivityDocument,
  writing_messages: writingMessagesDocument, mcp_calls: mcpCallsDocument, tools: toolsDocument, tool_calls: toolCallsDocument,
  skill_usages: skillUsagesDocument, tl1_attempts: tl1AttemptsDocument, parallelism: parallelismDocument, findings: findingsDocument,
};
