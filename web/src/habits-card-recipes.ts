import type { QueryRecipe } from "./curiosity-data";
import { habitsCardDescription, habitsCardPresentation, habitsCardWindow, readHabitsCardView, type HabitsCardTarget } from "./habits-card";

// Card Library entries for the Habits page's sections. Each is the page at an
// address (its habits_* view fields), drawn and opened as the Habits dashboard
// card the page's own "Add to Home" makes; the rhythm query is never run.

const kinds: Record<HabitsCardTarget, string> = {
  "habits-streaks": "Summary", "habits-pace": "Summary", "habits-calendar": "Calendar",
  "habits-punchcard": "Distribution", "habits-days": "Timeline", "habits-automations": "Ranking",
};

function habits(id: string, title: string, target: HabitsCardTarget, view: Record<string, string> = {}): QueryRecipe {
  const read = readHabitsCardView(new URLSearchParams(view)), { category, loading } = habitsCardPresentation(target, read);
  return {
    id, title, description: habitsCardDescription(target, read), category, idea: category === "human" ? "rhythm-01" : "rhythm-03",
    dataset: "rhythm", field: "started_at", unit: "hours", measures: [], period: "current", action: "Explore Habits",
    habits: { target, view, window: habitsCardWindow(target, read), kind: kinds[target], loading },
  };
}

export const habitsCardRecipes: QueryRecipe[] = [
  habits("habits-streak", "Your streak", "habits-streaks"),
  habits("habits-weekday-streak", "Your weekday streak", "habits-streaks", { habits_streak: "weekdays" }),
  habits("habits-calendar-you", "Your active days", "habits-calendar"),
  habits("habits-calendar-agents", "Days with agents you started", "habits-calendar", { habits_calendar: "agents" }),
  habits("habits-punchcard-you", "When you work", "habits-punchcard"),
  habits("habits-punchcard-away", "When agents work while you're away", "habits-punchcard", { habits_punchcard: "unattended" }),
  habits("habits-days", "Your days", "habits-days"),
  habits("habits-pace", "Your pace", "habits-pace"),
  habits("habits-automations", "Automatic agent runs", "habits-automations", { habits_punchcard_days: "30" }),
];
