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
export function discoverSourceControls(file,text,kind=ts.ScriptKind.TSX){
 const controls=[],bindings=[];
 const source=ts.createSourceFile(file,text,ts.ScriptTarget.Latest,true,kind),duplicates=new Map();
 function ownerNode(node){for(let p=node.parent;p;p=p.parent)if(ts.isFunctionLike(p))return p;return source;}
 function owner(node){for(let p=node.parent;p;p=p.parent){if(ts.isFunctionDeclaration(p)&&p.name)return p.name.text;if((ts.isArrowFunction(p)||ts.isFunctionExpression(p))&&ts.isVariableDeclaration(p.parent))return p.parent.name.getText(source);}return '<module>';}
 const scopeBindings=new WeakMap();
 function disclosureBindings(node){
  const scope=ownerNode(node);if(scopeBindings.has(scope))return scopeBindings.get(scope);
  const names=new Set(),panels=new Set();let hook=false;
  function find(child){
   if(child!==scope&&ts.isFunctionLike(child))return;
   if(ts.isCallExpression(child)&&child.expression.getText(source)==='useViewEntry'&&child.arguments[0]&&ts.isStringLiteral(child.arguments[0])&&child.arguments[0].text==='disclosures'&&child.arguments[1])hook=true;
   if(ts.isBinaryExpression(child)&&child.operatorToken.kind===ts.SyntaxKind.EqualsToken&&ts.isPropertyAccessExpression(child.left)&&child.left.name.text==='viewKey'&&ts.isPropertyAccessExpression(child.left.expression)&&child.left.expression.name.text==='dataset'&&(!ts.isStringLiteral(child.right)||child.right.text.trim()))names.add(child.left.expression.expression.getText(source));
   if(ts.isCallExpression(child)&&ts.isPropertyAccessExpression(child.expression)&&child.expression.name.text==='setAttribute'&&child.arguments[0]&&ts.isStringLiteral(child.arguments[0])&&child.arguments[0].text==='aria-controls'&&child.arguments[1]&&(!ts.isStringLiteral(child.arguments[1])||child.arguments[1].text.trim()))panels.add(child.expression.expression.getText(source));
   ts.forEachChild(child,find);
  }find(scope);const value={names,panels,hook};scopeBindings.set(scope,value);return value;
 }
 const primitive=node=>file==='web/src/view-disclosure.tsx'&&owner(node)==='ViewDisclosure';
 function add(node,tag,description,binding,disclosure=false,sharedDisclosure=false){const identity=[file,owner(node),tag,normalize(description)].join('|');const occurrence=duplicates.get(identity)??0;duplicates.set(identity,occurrence+1);controls.push({key:hash(identity)+':'+occurrence,file,owner:owner(node),tag,control:normalize(description),...(binding?{binding}:{}),...(disclosure?{disclosure:true}:{}),...(sharedDisclosure?{sharedDisclosure:true}:{})});}
 function visit(node){
  if(ts.isCallExpression(node)&&(['useViewState','useViewEntry','readViewState','writeViewState'].includes(node.expression.getText(source))||/pharosView.*(?:read|write)$/.test(node.expression.getText(source)))&&node.arguments[0]&&ts.isStringLiteral(node.arguments[0]))bindings.push({file,field:node.arguments[0].text});
  if(ts.isJsxOpeningElement(node)||ts.isJsxSelfClosingElement(node)){
   const tag=node.tagName.getText(source);const interactive=node.attributes.properties.some(attr=>ts.isJsxAttribute(attr)&&(attr.name.getText(source)==='aria-expanded'||/^on(?:Click|Change|Toggle|Apply|Filter|Select|QueryChange|PointerDown|KeyDown|MouseDown)$/.test(attr.name.getText(source))));if(tags.has(tag)||interactive){
    const attributes=node.attributes.properties.filter(ts.isJsxAttribute),bound=attributes.find(attr=>attr.name.getText(source)==='data-view-field');
    const bind=bound?.initializer&&ts.isStringLiteral(bound.initializer)?bound.initializer.text:tag==='ViewDisclosure'||primitive(node)?'disclosures':undefined;
    const description=attributes.filter(attr=>['aria-label','name','id','viewKey','data-view-field','value','checked','open','defaultOpen'].includes(attr.name.getText(source))||/^on/.test(attr.name.getText(source))).map(attr=>attr.getText(source)).join(' ');
    const validKey=attributes.some(attr=>attr.name.getText(source)==='viewKey'&&attr.initializer&&(!ts.isStringLiteral(attr.initializer)||attr.initializer.text.trim()));
    const sharedButton=!['details','summary','ViewDisclosure'].includes(tag)&&bind==='disclosures'&&disclosureBindings(node).hook;
    add(node,tag,description,bind,attributes.some(attr=>attr.name.getText(source)==='aria-expanded'),primitive(node)||tag==='ViewDisclosure'&&validKey||sharedButton);
   }
  }
  if(ts.isCallExpression(node)&&['node','document.createElement'].includes(node.expression.getText(source))&&node.arguments[0]&&ts.isStringLiteral(node.arguments[0])&&tags.has(node.arguments[0].text)){
   const tag=node.arguments[0].text,writers=disclosureBindings(node).names,name=ts.isVariableDeclaration(node.parent)?node.parent.name.getText(source):undefined;
   add(node,tag,node.arguments.map(argument=>argument.getText(source)).join(', '),undefined,false,tag==='summary'?writers.size>0:Boolean(name&&writers.has(name)));
  }
  if(ts.isCallExpression(node)&&ts.isPropertyAccessExpression(node.expression)&&node.expression.name.text==='setAttribute'&&node.arguments[0]&&ts.isStringLiteral(node.arguments[0])&&node.arguments[0].text==='aria-expanded'){
   const name=node.expression.expression.getText(source);
   const writers=disclosureBindings(node);
   add(node,'aria-expanded',name,undefined,true,writers.names.has(name)&&writers.panels.has(name)||file==='web/src/view-state.ts'&&owner(node)==='apply');
  }
  ts.forEachChild(node,visit);
 }visit(source);return {controls,bindings};
}
function scan(file,text,kind){const found=discoverSourceControls(file,text,kind);controls.push(...found.controls);bindings.push(...found.bindings);}
export function validateDisclosureControls(controls){
 for(const control of controls){
  const disclosure=['details','summary','ViewDisclosure'].includes(control.tag)||control.disclosure;
  if(disclosure&&['settings','legacy'].includes(control.contract))throw new Error(`Accordion openness is view state: ${control.file} ${control.owner}. Use ViewDisclosure (React) or data-view-key (classic HTML), or explicitly review a draft/menu exception.`);
  if(control.tag==='ViewDisclosure'&&!control.sharedDisclosure)throw new Error(`ViewDisclosure requires an explicit, nonempty viewKey: ${control.file} ${control.owner}`);
  if(disclosure&&control.contract==='view.disclosures'&&!control.sharedDisclosure)throw new Error(`Missing shared accordion binding: ${control.file} ${control.owner}. Use ViewDisclosure (React) or data-view-key (classic HTML); a ledger classification or data-view-field alone does not bind accordion state.`);
 }
}
function htmlAttribute(markup,name){const match=new RegExp(`(?:^|\\s)${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`).exec(markup);return match?.slice(1).find(value=>value!==undefined);}
function htmlDisclosureBinding(tag,markup){return Boolean((tag==='details'||tag==='button'&&htmlAttribute(markup,'aria-controls')?.trim())&&htmlAttribute(markup,'data-view-key')?.trim());}
export function discoverHTMLControls(file,text,{fragment=false}={}){
 const controls=[],sections=[],details=[],occurrences=new Map();
 for(const token of text.matchAll(/<(\/?)(section|input|select|textarea|button|summary|details)\b[^>]*>/g)){
  const [,closing,tag]=token,markup=token[0];
  if(tag==='section'){if(closing)sections.pop();else sections.push(/data-settings-route|id="settings"/.test(markup)||sections.at(-1));continue;}
  if(closing){if(tag==='details')details.pop();continue;}
  const identity=hash(fragment?file+'|'+markup:markup),i=occurrences.get(identity)??0;occurrences.set(identity,i+1);
  const binding=htmlAttribute(markup,'data-view-field'),shared=tag==='summary'?details.at(-1):htmlDisclosureBinding(tag,markup);
  controls.push({key:(fragment?'fragment:':'html:')+identity+':'+i,file,owner:fragment?'<html:fragment>':sections.at(-1)?'<html:settings>':'<html:shell>',tag,control:normalize(markup),...(binding?{binding}:{}),...(/aria-expanded\s*=/.test(markup)?{disclosure:true}:{}),...(shared?{sharedDisclosure:true}:{})});
  if(tag==='details')details.push(shared);
 }
 return controls;
}
for(const file of sources(join(root,'web/src')))scan(relative(root,file),readFileSync(file,'utf8'));
for(const name of ['ui.py',...readdirSync(join(root,'internal/archive/assets')).filter(name=>name.endsWith('.js')&&!name.startsWith('query-tables.')).sort()]){
 const path='internal/archive/assets/'+name,text=readFileSync(join(root,path),'utf8');
 if(name==='ui.py'){
  for(const script of text.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/g))scan(path,script[1],ts.ScriptKind.JS);
  controls.push(...discoverHTMLControls(path,text));
 }else scan(path,text,ts.ScriptKind.JS);
}
// Standalone shell fragments are also review surfaces; newly added HTML or
// classic scripts must not bypass the gate merely by living outside ui.py.
for(const name of readdirSync(join(root,'internal/archive/assets')).filter(name=>name.endsWith('.html')).sort()){
 const file='internal/archive/assets/'+name,text=readFileSync(join(root,file),'utf8');
 controls.push(...discoverHTMLControls(file,text,{fragment:true}));
}
controls.sort((a,b)=>a.file.localeCompare(b.file)||a.owner.localeCompare(b.owner)||a.key.localeCompare(b.key));
export const discoveredControls=controls;
export const discoveredBindings=bindings;
const dependencyPackages=['@pythia-software/query-table-core','@pythia-software/query-table-react','@pythia-software/query-table-ui','@pythia-software/query-table-codegen'];
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
 validateDisclosureControls(value.controls);
 if(unclassified.length)throw new Error(`${unclassified.length} unclassified controls: ${unclassified.slice(0,12).map(control=>control.file+':'+control.owner+' '+control.control.slice(0,100)).join('\n')}`);
 console.log(`View contracts: ${controls.length} controls classified; ${new Set(bindings.map(binding=>binding.field)).size} host fields bound.`);
}
