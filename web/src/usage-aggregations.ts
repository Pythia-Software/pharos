import { EMPTY_QUERY, applyAggregations, isOrGroup, matchesClause, predicatesOf, type AggregationRequest, type FieldSchema, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";

type Row = Record<string, any>;
const dailyID = (row: Row) => `${row.agent_session_id}|${row.day}|${row.model}`;

// Keep each shared-file metric at its own granularity, just as the service does.
export function applyUsageAggregations(daily: Row[], hourly: Row[], request: AggregationRequest, schema: FieldSchema<Row>) {
  const hasHour = (term: WhereTerm) => predicatesOf(term).some(clause => clause.field === "hour");
  const usesHour = (aggregation: AggregationRequest["aggregations"][number]) => aggregation.field === "hour" || aggregation.groupBy.includes("hour");
  const hourTerm = (term: WhereTerm) => hasHour(term) || predicatesOf(term).every(clause => ["first_usage_at", "last_usage_at", "hour"].includes(clause.field));
  const datetimeFields = new Set(schema.fields.filter(field => field.type === "datetime").map(field => field.name));
  const matchesPredicate = (row: Row, clause: WhereClause) => {
    // Core coerces datetime filter values to milliseconds; exported dates are strings.
    const value = row[clause.field];
    const datetime = datetimeFields.has(clause.field);
    const comparable = datetime && typeof value === "string" ? { ...row, [clause.field]: Date.parse(value) } : row;
    return matchesClause(comparable, clause, schema);
  };
  const matches = (row: Row, term: WhereTerm) => isOrGroup(term) ? term.any.some(clause => matchesPredicate(row, clause)) : matchesPredicate(row, term);
  const rowWhere = request.where.filter(term => !hourTerm(term)), hourWhere = request.where.filter(hourTerm);
  const ids = new Set(daily.filter(row => rowWhere.every(term => matches(row, term))).map(row => String(row.id)));
  const hours = hourly.filter(row => ids.has(dailyID(row)) && hourWhere.every(term => matches(row, term)));
  const filtersHours = request.where.some(hasHour);
  const selectedIDs = new Set(hours.map(dailyID));
  const days = filtersHours ? daily.filter(row => selectedIDs.has(String(row.id))) : daily.filter(row => request.where.every(term => matches(row, term)));
  return { metrics: request.aggregations.flatMap(aggregation => {
    const hourlyMetric = usesHour(aggregation);
    return applyAggregations(hourlyMetric ? hours : days, {
      ...EMPTY_QUERY,
      where: [],
      aggregations: [aggregation],
    }, schema).metrics;
  }) };
}
