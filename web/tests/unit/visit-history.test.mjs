import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_QUERY, encodeQuery } from '@pythia-software/query-table-core';
import { visitQuerySummary } from '../../src/visit-history';

test('visit history retains filters, alternatives, grouping and order in compressed links',()=>{
 const token=encodeQuery({...EMPTY_QUERY,where:[{field:'repository_name',op:'=',value:'pharos'},{any:[{field:'tool_name',op:'=',value:'Bash'},{field:'tool_name',op:'=',value:'Read',negated:true}]}],aggregations:[{id:'tokens',op:'sum',field:'total_tokens',groupBy:['model']}],orderBy:[{field:'duration_ms',dir:'desc'}]});
 const summary=visitQuerySummary(token);
 for(const text of ['repository name = pharos','Bash','not tool name = Read','by model','duration ms desc'])assert.ok(summary.includes(text),summary);
 const pairs=encodeQuery({...EMPTY_QUERY,grouping:{source:{...EMPTY_QUERY,where:[{any:[{all:[{field:'provider',op:'=',value:'claude'},{field:'model',op:'=',value:'opus'}]},{all:[{field:'provider',op:'=',value:'codex'},{field:'model',op:'=',value:'sol'}]}]}]},keys:[{field:'started_at',interval:'day',timeZone:'America/Denver'}],summaries:[{id:'n',op:'count'}],representative:null}});
 assert.equal(visitQuerySummary(pairs),'((provider = claude and model = opus) or (provider = codex and model = sol)); by started at by day');
 assert.equal(visitQuerySummary('invalid query token'),'');
});
