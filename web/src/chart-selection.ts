import { isOrGroup, isOrOfAndsGroup, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";

export type LegendAction = "only" | "toggle" | "exclude" | "clear" | "show" | "hide";
export type SliceValue = string | null;
export type SliceSelection<Value> = { values: Value[]; excluded: boolean; allHidden?: boolean };
export type ChartPresentation = { selection?: SliceSelection<string>; seriesLimit?: number };
export function validChartPresentation(value: ChartPresentation): boolean {
  return (value.seriesLimit === undefined || Number.isInteger(value.seriesLimit) && value.seriesLimit >= 1 && value.seriesLimit <= 1000)
    && (value.selection === undefined || Boolean(value.selection) && typeof value.selection.excluded === "boolean" && Array.isArray(value.selection.values)
      && value.selection.values.length <= 1000 && value.selection.values.every(item => typeof item === "string") && validHiddenSelection(value.selection));
}

export function validHiddenSelection(selection: SliceSelection<unknown>) {
  return (selection.allHidden === undefined || typeof selection.allHidden === 'boolean')
    && (!selection.allHidden || !selection.excluded && !selection.values.length);
}
export const sliceSelectionActive = (selection: SliceSelection<unknown>) => Boolean(selection.allHidden || selection.values.length);
export const sliceValueVisible = <Value,>(selection: SliceSelection<Value>, value: Value) => !selection.allHidden && (!selection.values.length || selection.values.includes(value) !== selection.excluded);
export function sliceGroupCounts<Value>(selection: SliceSelection<Value>, values: Value[]) {
  const group=[...new Set(values)], visibleCount=group.filter(value=>sliceValueVisible(selection,value)).length;
  return {visibleCount,hiddenCount:group.length-visibleCount};
}
export type LegendMenuItem = {label:string;action:LegendAction;separator?:boolean};
export function legendMenuItems(visibleCount:number,hiddenCount:number,othersHidden:boolean):LegendMenuItem[] {
  const mixed=visibleCount>0&&hiddenCount>0;
  const items:LegendMenuItem[]=mixed
    ? [{label:`Show ${hiddenCount} ${hiddenCount===1?'value':'values'}`,action:'show'},{label:`Hide ${visibleCount} ${visibleCount===1?'value':'values'}`,action:'hide'}]
    : visibleCount>0 ? [{label:'Hide',action:'hide'},{label:'Hide others',action:'only'}]
      : [{label:'Show',action:'show'},{label:'Show and Hide Others',action:'only'}];
  if(mixed||hiddenCount>0||othersHidden)items.push({label:'Show All',action:'clear',separator:true});
  return items;
}

function clauseSelection(clause: WhereClause, field: string): SliceSelection<SliceValue> | null {
  if (clause.field !== field) return null;
  if (!clause.negated && (clause.op === "=" || clause.op === "is_null")) return { values: [clause.op === "is_null" ? null : clause.value], excluded: false };
  if ((!clause.negated && clause.op === "is_not_null") || (clause.negated && clause.op === "is_null")) return { values: [null], excluded: true };
  if (!clause.negated && clause.op === "!=") return { values: [clause.value], excluded: true };
  if (clause.negated && clause.op === "=") return { values: [clause.value, null], excluded: true };
  return null;
}

function termSelection(term: WhereTerm, field: string): SliceSelection<SliceValue> | null {
  // An OR of conjunctions spans several fields, so it is never one field's slice.
  if (isOrOfAndsGroup(term)) return null;
  if (!isOrGroup(term)) return clauseSelection(term, field);
  const parts = term.any.map(clause => clauseSelection(clause, field));
  if (!parts.length || parts.some(part => !part)) return null;
  if (parts.every(part => !part!.excluded)) return { values: parts.flatMap(part => part!.values), excluded: false };
  if (parts.length === 2) {
    const excluded = parts.find(part => part!.excluded), included = parts.find(part => !part!.excluded);
    if (excluded && included?.values.length === 1 && included.values[0] === null && excluded.values.some(value => value !== null)) return { values: excluded.values.filter(value => value !== null), excluded: true };
  }
  return null;
}

export function isSliceTerm(term: WhereTerm, field: string): boolean {
  return termSelection(term, field) !== null;
}

export function readSlice(where: WhereTerm[], field: string): SliceSelection<SliceValue> {
  const parts = where.map(term => termSelection(term, field)).filter((part): part is SliceSelection<SliceValue> => part !== null);
  if(parts.some(part=>!part.excluded&&part.values.length===1&&part.values[0]===null)&&parts.some(part=>part.excluded&&part.values.includes(null)))return {values:[],excluded:false,allHidden:true};
  const excluded = parts[0]?.excluded ?? false;
  return { values: [...new Set(parts.filter(part => part.excluded === excluded).flatMap(part => part.values))], excluded };
}

export function withSlice(where: WhereTerm[], field: string, selection: SliceSelection<SliceValue>): WhereTerm[] {
  const kept = where.filter(term => !isSliceTerm(term, field));
  if(selection.allHidden)return [...kept,{field,op:'is_null',value:''},{field,op:'is_not_null',value:''}];
  if (selection.excluded) {
    for (const value of selection.values) kept.push(value === null ? { field, op: "is_not_null", value: "" } : { any: [{ field, op: "!=", value }, { field, op: "is_null", value: "" }] });
  } else {
    const clauses: WhereClause[] = selection.values.map(value => value === null ? { field, op: "is_null", value: "" } : { field, op: "=", value });
    if (clauses.length === 1) kept.push(clauses[0]);
    else if (clauses.length) kept.push({ any: clauses });
  }
  return kept;
}

export function updateSlice<Value>(selection: SliceSelection<Value>, value: Value, action: LegendAction): SliceSelection<Value> {
  if(action==='show'||action==='hide'||selection.allHidden&&action==='toggle')return updateSliceValues(selection,[value],action==='toggle'?'show':action);
  if (action === "clear") return { values: [], excluded: false };
  if (action === "only") return { values: [value], excluded: false };
  if (action === "exclude") return { values: [value], excluded: true };
  const values = selection.values.includes(value) ? selection.values.filter(item => item !== value) : [...selection.values, value];
  return { values, excluded: values.length > 0 && selection.excluded };
}

// Apply a category action once, preserving selections outside the category.
// A mixed category is filled on Add; a fully visible one is removed on Remove,
// including when the selection is expressed as exclusions.
export function updateSliceValues<Value>(selection: SliceSelection<Value>, values: Value[], action: LegendAction): SliceSelection<Value> {
  if (action === "clear") return {values:[],excluded:false};
  const group = [...new Set(values)];
  if (!group.length) return selection;
  if (action === "only" || action === "exclude") return {values:group,excluded:action === "exclude"};
  if(action==='show'||action==='hide'){
    if(selection.allHidden)return action==='show'?{values:group,excluded:false}:selection;
    if(!selection.values.length)return action==='hide'?{values:group,excluded:true}:selection;
    const members=new Set(group),current=new Set(selection.values);
    const add=action==='show'?!selection.excluded:selection.excluded;
    const next=add?[...selection.values,...group.filter(value=>!current.has(value))]:selection.values.filter(value=>!members.has(value));
    if(!next.length&&!selection.excluded)return {values:[],excluded:false,allHidden:true};
    return {values:next,excluded:next.length>0&&selection.excluded};
  }
  if(selection.allHidden)return {values:group,excluded:false};
  const current = new Set(selection.values), members = new Set(group);
  const remove = selection.excluded ? group.some(value => current.has(value)) : group.every(value => current.has(value));
  const next = remove ? selection.values.filter(value => !members.has(value)) : [...selection.values,...group.filter(value => !current.has(value))];
  return {values:next,excluded:next.length > 0 && selection.excluded};
}

export function sliceGroupState<Value>(selection: SliceSelection<Value>, values: Value[]) {
  const group = [...new Set(values)], selected = new Set(selection.values);
  if(selection.allHidden)return {active:false,mixed:false,dimmed:true,soleSelection:false};
  if (!selected.size || !group.length) return {active:false,mixed:false,dimmed:false,soleSelection:false};
  const count = group.filter(value => selection.excluded ? !selected.has(value) : selected.has(value)).length;
  return {active:count === group.length,mixed:count > 0 && count < group.length,dimmed:count === 0,
    soleSelection:!selection.excluded && count === group.length && selected.size === group.length};
}
