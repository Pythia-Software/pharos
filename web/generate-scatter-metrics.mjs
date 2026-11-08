import { build } from 'esbuild';
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
export async function generateScatterMetrics(check = false) {
  const bundle = await build({ absWorkingDir: root + 'web', entryPoints: ['src/scatter-metrics.ts'], bundle: true, write: false, platform: 'node', format: 'esm', logLevel: 'warning' });
  const { scatterMetrics, scatterMetricDefinitions } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
  const fields = [
    ['id', 'Point ID', 'text'], ['entity_id', 'Point', 'text'], ['point_kind', 'Point type', 'text'],
    ['title', 'Title', 'text'], ['provider', 'Provider', 'text'], ['repository_name', 'Repository', 'text'], ['model', 'Model', 'text'],
    ['first_at', 'First matching record', 'datetime'], ['last_at', 'Last matching record', 'datetime'],
  ].map(([name,label,type]) => ({name,label,type,group:'Point',bindings:{sqlite:{expr:`r."${name}"`,...type==='datetime'?{datetimeFormat:'utc-millis'}:{}}},aggregate:{groupable:true}}));
  for (const item of scatterMetrics) fields.push({name:item.id,label:item.measure.label,type:'number',group:item.group,description:item.measure.note,bindings:{sqlite:{expr:`r."${item.id}"`,expressionNumeric:true}},aggregate:{groupable:true}});
  const schema = { name:'pharos_scatter',version:1,idField:'id',defaultLimit:50,defaultSort:[{field:'entity_id',dir:'asc'}],tiebreakSort:[{field:'id',dir:'asc'}],defaultSelect:['title','point_kind','human_typed','tokens_cost'].map(field=>({field})),fields };
  for (const [path, data] of [['schemas/scatter.schema.json',schema],['schemas/scatter-metrics.json',scatterMetricDefinitions]]) {
    const next=JSON.stringify(data,null,2)+'\n';
    if(check){if(!existsSync(root+path)||readFileSync(root+path,'utf8')!==next)throw Error(`${path} is stale; run npm --prefix web run generate-schemas`);}
    else if(!existsSync(root+path)||readFileSync(root+path,'utf8')!==next)writeFileSync(root+path,next);
  }
}
