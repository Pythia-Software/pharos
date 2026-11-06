// Which agent events share one mark: the rule the conversation reader
// (reader-model.js) and the timeline and fingerprint ribbons (web/src
// event-merge.ts) both use. The page loads this as a classic script before
// reader-model.js; the frontend bundle imports it.
//
// Two events of one message are near-identical when one sentence contains
// the other, or they share at least EVENT_MERGE_OVERLAP of the text the two
// cover together. Touching spans share nothing. Grouping is transitive.
const EVENT_MERGE_OVERLAP=.8;
function nearSameSpan(a,b){
  const lo=Math.max(a[0],b[0]),hi=Math.min(a[1],b[1]);if(hi<=lo)return false;
  if(a[0]<=b[0]&&a[1]>=b[1]||b[0]<=a[0]&&b[1]>=a[1])return true;
  return (hi-lo)/(Math.max(a[1],b[1])-Math.min(a[0],b[0]))>=EVENT_MERGE_OVERLAP;
}
// Groups of near-identical spans: each a list of indexes into spans, in
// order, groups ordered by their first member. A missing span is a group of
// its own.
function eventSpanGroups(spans){
  const parent=spans.map((_,index)=>index),find=index=>parent[index]===index?index:(parent[index]=find(parent[index]));
  for(let i=0;i<spans.length;i++)for(let j=i+1;j<spans.length;j++)if(spans[i]&&spans[j]&&nearSameSpan(spans[i],spans[j])){const a=find(i),b=find(j);if(a!==b)parent[Math.max(a,b)]=Math.min(a,b)}
  const groups=new Map();spans.forEach((_,index)=>{const root=find(index);if(!groups.has(root))groups.set(root,[]);groups.get(root).push(index)});
  return [...groups.values()];
}
if(typeof module==='object'&&module&&module.exports)module.exports={EVENT_MERGE_OVERLAP,nearSameSpan,eventSpanGroups};
