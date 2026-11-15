import type { MetricValueFormat } from "@pythia-software/query-table-core";

export { formatTokens, formatTokenCount } from '../../internal/archive/assets/token-count.js';

// Token measures that Query Table formats itself. Its compact notation takes
// fraction digits rather than significant figures, so this reads 1.2K, 12.3K
// and 123.4K where formatTokens reads 1.23K, 12.3K and 123K.
export const tokenMetricFormat: MetricValueFormat = { kind: "number", notation: "compact", decimals: 1 };

// Token count fields: token_count, tokens and any *_tokens field, such as total_tokens.
export const isTokenField = (name: string | undefined): boolean => /(^token_count$|^tokens$|_tokens$)/.test(name ?? "");
// One token field's sum, mean, median or extreme reads in tokens; ratios and
// other formulas do not.
export const isTokenExpression = (expression: string | undefined): boolean => {
  const field = /^(?:SUM|AVG|MEDIAN|MIN|MAX)\(\[([a-z_]+)\]\)$/i.exec(expression?.trim() ?? "")?.[1];
  return isTokenField(field);
};
