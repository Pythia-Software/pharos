import { isWhereConjunction, isWhereGroup, type WhereBranch, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";

// mapTermPredicates rewrites every predicate of a term in place, keeping its
// OR groups and AND branches. It returns undefined when the rewrite declines
// any predicate, since dropping one would change what the term matches.
export function mapTermPredicates(term: WhereTerm, rewrite: (clause: WhereClause) => WhereClause | undefined): WhereTerm | undefined {
  if (!isWhereGroup(term)) return rewrite(term);
  const branch = (member: WhereBranch): WhereBranch | undefined => {
    if (!isWhereConjunction(member)) return rewrite(member);
    const all = member.all.map(rewrite);
    return all.every((clause): clause is WhereClause => Boolean(clause)) ? { all } : undefined;
  };
  const any = term.any.map(branch);
  return any.every((member): member is WhereBranch => Boolean(member)) ? { ...term, any } : undefined;
}
