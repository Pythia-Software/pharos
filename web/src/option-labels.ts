import type { FieldSchema } from "@pythia-software/query-table-core";

// Each field's static filter options, as value -> label. Fields without
// labelled static options are absent.
export function optionLabels(fields: FieldSchema<Record<string, any>>["fields"]): Map<string, Map<string, string>> {
  const labels = new Map<string, Map<string, string>>();
  for (const field of fields) {
    const values = field.filter?.values;
    if (values?.source !== "static") continue;
    const options = new Map<string, string>();
    for (const option of values.options) if (typeof option !== "string") options.set(option.value, option.label);
    if (options.size) labels.set(field.name, options);
  }
  return labels;
}
