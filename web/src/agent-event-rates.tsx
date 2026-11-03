import { agentEventGroups } from "./agent-events-analysis-catalog";
import { perThousand, type RatePeriod } from "./agent-event-rates-model";
import { chartSeriesStyle } from "./chart-colors";
import { ChartLegend } from "./chart-legend";
import "./agent-event-examples.css";

// Events per 1,000 agent messages read, stacked by kind group, one column per
// period. Hovering a segment says the rate, the events and the messages read.
const color = (group: string) => chartSeriesStyle("family_group", group).color;
const dateLabel = (start: string) => new Date(`${start}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" });

export function AgentEventRates({ periods, period, loading, error }: { periods: RatePeriod[]; period: "day" | "week"; loading: boolean; error: string }) {
  const groups = agentEventGroups.filter(group => periods.some(entry => entry.events[group]));
  const highest = Math.max(1, ...periods.map(entry => perThousand(entry.total, entry.read)));
  const unit = period === "week" ? "week" : "day";
  return <section className="agent-event-rates" aria-busy={loading}>
    <header>
      <h3>Events per 1,000 agent messages, by {unit}</h3>
      <p className="muted">Counts follow how much agents worked; this rate divides each {unit}'s events by the agent messages read that {unit}, under the same filters.</p>
    </header>
    {error ? <p className="query-table-error" role="alert">{error}</p> : null}
    {!error && !loading && !periods.length ? <p className="muted">No agent messages were read in this range.</p> : null}
    {periods.length ? <>
      <ChartLegend items={groups.map(group => ({ key: group, label: group, color: color(group) }))} />
      <div className="agent-event-rate-plot" role="img" aria-label={`Events per 1,000 agent messages by ${unit}, up to ${Math.round(highest)}`}>
        <span className="agent-event-rate-axis">{Math.round(highest).toLocaleString()}</span>
        <div className="agent-event-rate-columns">
          {periods.map(entry => {
            const rate = perThousand(entry.total, entry.read);
            return <div key={entry.start} className="agent-event-rate-column" style={{ height: `${100 * rate / highest}%` }}>
              {groups.filter(group => entry.events[group]).map(group => {
                const value = perThousand(entry.events[group], entry.read);
                return <i key={group} className="qt-metric-mark" style={{ flexGrow: entry.events[group], background: color(group) }}
                  data-chart-colors={JSON.stringify([color(group)])}
                  data-chart-tooltip={`${period === "week" ? "Week of " : ""}${dateLabel(entry.start)}\n${group}: ${value.toFixed(1)} per 1,000 messages\n${entry.events[group].toLocaleString()} events in ${entry.read.toLocaleString()} messages read\nAll kinds: ${rate.toFixed(1)}`} />;
              })}
            </div>;
          })}
        </div>
        <div className="agent-event-rate-dates muted">
          <span>{dateLabel(periods[0].start)}</span>
          {periods.length > 2 ? <span>{dateLabel(periods[Math.floor(periods.length / 2)].start)}</span> : null}
          {periods.length > 1 ? <span>{dateLabel(periods[periods.length - 1].start)}</span> : null}
        </div>
      </div>
    </> : null}
  </section>;
}
