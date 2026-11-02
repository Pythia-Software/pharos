export const safetyRanks = ["recent", "calls", "evidence"] as const;
export type SafetyRank = typeof safetyRanks[number];
export const safetyRankLabels: Record<SafetyRank, string> = { recent: "Recent", calls: "Calls", evidence: "New evidence" };

type Review = { id: string; state: string; target: string; target_label: string; last_seen_at: string; calls: number; material_change: boolean };
const seen = (review: Review) => Date.parse(review.last_seen_at) || 0;

export function safetyDashboardReviews<T extends Review>(reviews: readonly T[], state: string, place: string, rank: SafetyRank): T[] {
  return reviews.filter(review => review.state === state && (!place || review.target === place)).sort((a, b) => {
    const lead = rank === "calls" ? b.calls - a.calls : rank === "evidence" ? Number(b.material_change) - Number(a.material_change) : 0;
    return lead || seen(b) - seen(a) || a.id.localeCompare(b.id);
  });
}

export function safetyDashboardPlaces(reviews: readonly Review[]) {
  const places = new Map<string, { key: string; label: string; count: number }>();
  for (const review of reviews) {
    const place = places.get(review.target) ?? { key: review.target, label: review.target_label, count: 0 };
    place.count++;
    places.set(review.target, place);
  }
  return [...places.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
}
