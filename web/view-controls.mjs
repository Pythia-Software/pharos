// A review ledger of the actual controls, not a count of declared URL fields.
// New/changed raw controls fail until bound to a field or explicitly reviewed.
// --write records removals and direct field bindings; it never guesses the
// contract of a new raw control. Classify remaining entries in the ledger.
import ts from 'typescript';
import {readFileSync,writeFileSync,readdirSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {join,relative} from 'node:path';
const root=fileURLToPath(new URL('..',import.meta.url));
const ledgerPath=join(root,'docs/view-controls.json');
const hash=text=>createHash('sha256').update(text).digest('hex').slice(0,16);
const normalize=text=>text.replace(/\s+/g,' ').trim();
const tags=new Set(['input','select','textarea','button','summary','details','SearchSelect','ChartSelect','RangeControl','DisplayControl','PeriodControl','ScaleControl','RollingControl','SmoothingControl','ViewDisclosure','DataTable','QueryBuilder','QueryFilterRow','MetricsPanel','JsonValueView','JsonCell','TextSearchEditor','SearchFilters','SourceQueryBuilder']);
function sources(dir){return readdirSync(dir,{withFileTypes:true}).flatMap(entry=>entry.isDirectory()?sources(join(dir,entry.name)):/\.(tsx|ts)$/.test(entry.name)?[join(dir,entry.name)]:[]);}
const controls=[],bindings=[];
function scan(file,text,kind=ts.ScriptKind.TSX){
 const source=ts.createSourceFile(file,text,ts.ScriptTarget.Latest,true,kind),duplicates=new Map();
 function owner(node){for(let p=node.parent;p;p=p.parent){if(ts.isFunctionDeclaration(p)&&p.name)return p.name.text;if((ts.isArrowFunction(p)||ts.isFunctionExpression(p))&&ts.isVariableDeclaration(p.parent))return p.parent.name.getText(source);}return '<module>';}
 function add(node,tag,description,binding){const identity=[file,owner(node),tag,normalize(description)].join('|');const occurrence=duplicates.get(identity)??0;duplicates.set(identity,occurrence+1);controls.push({key:hash(identity)+':'+occurrence,file,owner:owner(node),tag,control:normalize(description),...(binding?{binding}:{})});}
 function visit(node){
  if(ts.isCallExpression(node)&&(['useViewState','useViewEntry','readViewState','writeViewState'].includes(node.expression.getText(source))||/pharosView.*(?:read|write)$/.test(node.expression.getText(source)))&&node.arguments[0]&&ts.isStringLiteral(node.arguments[0]))bindings.push({file,field:node.arguments[0].text});
  if(ts.isJsxOpeningElement(node)||ts.isJsxSelfClosingElement(node)){
   const tag=node.tagName.getText(source);const interactive=node.attributes.properties.some(attr=>ts.isJsxAttribute(attr)&&/^on(?:Click|Change|Toggle|Apply|Filter|Select|QueryChange|PointerDown|KeyDown|MouseDown)$/.test(attr.name.getText(source)));if(tags.has(tag)||interactive){
    const attributes=node.attributes.properties.filter(ts.isJsxAttribute),bound=attributes.find(attr=>attr.name.getText(source)==='data-view-field');
    const bind=bound?.initializer&&ts.isStringLiteral(bound.initializer)?bound.initializer.text:tag==='ViewDisclosure'?'disclosures':undefined;
    const description=attributes.filter(attr=>['aria-label','name','id','viewKey','data-view-field','value','checked','open'].includes(attr.name.getText(source))||/^on/.test(attr.name.getText(source))).map(attr=>attr.getText(source)).join(' ');
    add(node,tag,description,bind);
   }
  }
  if(ts.isCallExpression(node)&&['node','document.createElement'].includes(node.expression.getText(source))&&node.arguments[0]&&ts.isStringLiteral(node.arguments[0])&&tags.has(node.arguments[0].text)){
   add(node,node.arguments[0].text,node.arguments.map(argument=>argument.getText(source)).join(', '));
  }
  ts.forEachChild(node,visit);
 }visit(source);
}
for(const file of sources(join(root,'web/src')))scan(relative(root,file),readFileSync(file,'utf8'));
for(const name of ['ui.py',...readdirSync(join(root,'internal/archive/assets')).filter(name=>name.endsWith('.js')&&!name.startsWith('query-tables.')).sort()]){
 const path='internal/archive/assets/'+name,text=readFileSync(join(root,path),'utf8');
 if(name==='ui.py'){
  for(const script of text.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g))scan(path,script[1],ts.ScriptKind.JS);
  const sections=[],occurrences=new Map();
  for(const token of text.matchAll(/<(\/?)(section|input|select|textarea|button|summary|details)\b[^>]*>/g)){
    if(token[2]==='section'){if(token[1])sections.pop();else sections.push(/data-settings-route|id="settings"/.test(token[0])||sections.at(-1));continue;}
    if(token[1])continue;
    const identity=hash(token[0]),i=occurrences.get(identity)??0;occurrences.set(identity,i+1);
    controls.push({key:'html:'+identity+':'+i,file:path,owner:sections.at(-1)?'<html:settings>':'<html:shell>',tag:token[2],control:normalize(token[0])});
  }
 }else scan(path,text,ts.ScriptKind.JS);
}
// Standalone shell fragments are also review surfaces; newly added HTML or
// classic scripts must not bypass the gate merely by living outside ui.py.
for(const name of readdirSync(join(root,'internal/archive/assets')).filter(name=>name.endsWith('.html')).sort()){
 const file='internal/archive/assets/'+name,text=readFileSync(join(root,file),'utf8'),duplicates=new Map();
 for(const match of text.matchAll(/<(input|select|textarea|button|summary|details)\b[^>]*>/g)){
  const identity=hash(file+'|'+match[0]),occurrence=duplicates.get(identity)??0;duplicates.set(identity,occurrence+1);
  controls.push({key:'fragment:'+identity+':'+occurrence,file,owner:'<html:fragment>',tag:match[1],control:normalize(match[0])});
 }
}
controls.sort((a,b)=>a.file.localeCompare(b.file)||a.owner.localeCompare(b.owner)||a.key.localeCompare(b.key));
export const discoveredControls=controls;
export const discoveredBindings=bindings;
const dependencyPackages=['@pythia-software/query-table-core','@pythia-software/query-table-react','@pythia-software/query-table-ui'];
export function checkedLedger(){
 const ledger=JSON.parse(readFileSync(ledgerPath,'utf8')),known=new Map(ledger.controls.map(control=>[control.key,control]));
 const dependencies=Object.fromEntries(dependencyPackages.map(name=>{const version=JSON.parse(readFileSync(join(root,'web/node_modules',name,'package.json'),'utf8')).version;const reviewed=ledger.dependencies?.[name];return [name,{version,status:reviewed?.version===version?reviewed.status:'unclassified',reason:reviewed?.version===version?reviewed.reason:''}]}));
 const uniqueBindings=[...new Map(bindings.map(binding=>[binding.field+'|'+binding.file,binding])).values()].sort((a,b)=>a.field.localeCompare(b.field)||a.file.localeCompare(b.file));
 return {version:1,dependencies,bindings:uniqueBindings,controls:controls.map(control=>({...control,contract:control.binding?'view.'+control.binding:known.get(control.key)?.contract??'unclassified',reason:known.get(control.key)?.reason||(control.binding?'Bound directly to the registered view field.':'')}))};
}
if(process.argv[1]===fileURLToPath(import.meta.url)){
 const value=checkedLedger(),unclassified=value.controls.filter(control=>control.contract==='unclassified');
 if(process.argv.includes('--write'))writeFileSync(ledgerPath,JSON.stringify(value,null,2)+'\n');
 else if(JSON.stringify(JSON.parse(readFileSync(ledgerPath,'utf8')))!==JSON.stringify(value))throw new Error('View control audit is stale. Run npm --prefix web run generate-view-controls, then classify each new control.');
 if(Object.values(value.dependencies).some(dependency=>dependency.status!=='reviewed'||!dependency.reason))throw new Error('Query Table dependencies changed. Audit composite controls against the new published release and explicitly review docs/view-controls.json dependencies.');
 if(unclassified.length)throw new Error(`${unclassified.length} unclassified controls: ${unclassified.slice(0,12).map(control=>control.file+':'+control.owner+' '+control.control.slice(0,100)).join('\n')}`);
 console.log(`View contracts: ${controls.length} controls classified; ${new Set(bindings.map(binding=>binding.field)).size} host fields bound.`);
}
