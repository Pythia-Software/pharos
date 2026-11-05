import type { FieldDef, FieldSchema } from "@pythia-software/query-table-core";

// These are display columns. Project only the entity identity; ribbon evidence
// is fetched when the cell is mounted, independently of the query's measures.
export function withFingerprintColumn(schema: FieldSchema, dataset: string): FieldSchema {
  const identity = dataset === "library" || dataset === "writing" ? "id"
    : schema.fields.some(field => field.name === "conversation_id") ? "conversation_id"
    : schema.fields.some(field => field.name === "workspace_id") ? "workspace_id" : undefined;
  if (!identity) return schema;
  const fingerprint = (name: string, label: string, identity: string): FieldDef => ({
    name, label, type: "text", group: "Work",
    source: { kind: "derived", dependencies: [identity], accessor: row => row[identity] },
    filter: { enabled: false }, sort: { enabled: false }, aggregate: { measure: false, groupable: false },
    render: identity === "conversation_id" ? "label_fingerprint" : "workspace_fingerprint",
    select: { default: false, width: 180 }, property: { default: false },
  });
  const fields = [fingerprint("fingerprint", "Fingerprint", identity)];
  if (identity === "conversation_id" && schema.fields.some(field => field.name === "workspace_id")) {
    fields.push(fingerprint("workspace_fingerprint", "Workspace fingerprint", "workspace_id"));
  }
  const additions = fields.filter(field => !schema.fields.some(existing => existing.name === field.name));
  return additions.length ? { ...schema, fields: [...schema.fields, ...additions] } : schema;
}
