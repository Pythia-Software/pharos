import { useEffect, useRef, useState } from "react";
import { onDatasetRefresh } from "./dataset-refresh";
import type { ResultPolicy } from "./result-cache";

// A dashboard view's refresh count, and the result-cache policy its next
// load should ask with: "fresh" once after a person's Refresh, otherwise
// "cached" (opening the page, changing a control, a focused tab).
export function useDatasetRefresh(dataset: string): [number, () => ResultPolicy] {
  const [refresh, setRefresh] = useState(0), asked = useRef<ResultPolicy>("cached");
  useEffect(() => onDatasetRefresh(dataset, policy => { if (policy === "fresh") asked.current = policy; setRefresh(value => value + 1); }), [dataset]);
  return [refresh, () => { const policy = asked.current; asked.current = "cached"; return policy; }];
}
