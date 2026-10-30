// Agent documentation follows the installed public API, including every nested
// metric/display/property type. No handwritten copy of the dependency schema.
import ts from 'typescript';
import {readFileSync} from 'node:fs';
export function publishedQueryTypes() {
  const base=new URL('./node_modules/@pythia-software/query-table-core/',import.meta.url);
  const version=JSON.parse(readFileSync(new URL('package.json',base),'utf8')).version;
  const source=ts.createSourceFile('index.d.ts',readFileSync(new URL('dist/index.d.ts',base),'utf8'),ts.ScriptTarget.Latest,true);
  const declarations=new Map(source.statements.filter(node=>ts.isInterfaceDeclaration(node)||ts.isTypeAliasDeclaration(node)).map(node=>[node.name.text,node]));
  const types={},queue=['QueryState'];
  while(queue.length){
    const name=queue.shift();if(types[name]||!declarations.has(name))continue;
    const node=declarations.get(name),uses=new Set();
    const visit=child=>{const reference=ts.isTypeReferenceNode(child)?child.typeName:ts.isExpressionWithTypeArguments(child)?child.expression:undefined;if(reference&&ts.isIdentifier(reference)&&declarations.has(reference.text))uses.add(reference.text);ts.forEachChild(child,visit);};
    visit(node);uses.delete(name);
    types[name]={definition:node.getFullText(source).trim(),uses:[...uses].sort()};queue.push(...uses);
  }
  return {source:'@pythia-software/query-table-core public TypeScript declarations',version,types:Object.fromEntries(Object.entries(types).sort(([a],[b])=>a.localeCompare(b)))};
}
