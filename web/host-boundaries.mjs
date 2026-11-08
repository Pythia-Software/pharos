// Source-only host boundary check. No bundles, browser, service or native calls.
import ts from 'typescript';
import {readFileSync, readdirSync} from 'node:fs';
import {basename, join} from 'node:path';
import {fileURLToPath} from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));
const sourceRoots = ['web/src', 'internal/archive/assets', 'internal/uifeedback/assets'];
// These exact outputs contain dependency code; scan their authored inputs.
const generated = new Set(['internal/archive/assets/query-tables.js', 'internal/archive/assets/data-guide.js', 'internal/uifeedback/assets/inbox.js']);
const adapters = new Map([['internal/archive/assets/host-services.js', 'Owns native transport, browser clipboard and copy fallback.']]);
const nativeModule = name => name === 'electron' || name.startsWith('@tauri-apps/') || /^(?:@wailsio\/|\.?\.?\/.*wailsjs\/|wailsjs\/)/.test(name);

// HTML/Python shells contain executable JS, not a JS source file as a whole.
// Keep line offsets so failures point at the authored shell, not a virtual file.
export function shellScripts(file, text) {
  const entries = [], scriptRanges = [], lines = offset => text.slice(0, offset).split('\n').length - 1;
  const attribute = (tag, name) => new RegExp(`\\b${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, 'i').exec(tag)?.slice(1).find(value => value !== undefined);
  let index = 0;
  for (const match of text.matchAll(/<!--[\s\S]*?(?:-->|$)|<script\b([^>]*)>([\s\S]*?)(?:<\/script\s*>|$)/gi)) {
    scriptRanges.push([match.index, match.index + match[0].length]);
    if (match[0].startsWith('<!--')) continue;
    const type = attribute(match[1], 'type')?.toLowerCase().split(';')[0].trim();
    if (attribute(match[1], 'src') !== undefined || type && !['module', 'text/javascript', 'application/javascript', 'text/ecmascript', 'application/ecmascript'].includes(type)) continue;
    if (!/<\/script\s*>$/i.test(match[0])) throw Error(`${file}:${lines(match.index) + 1}: unclosed executable script`);
    const start = match.index + match[0].indexOf('>') + 1;
    entries.push({file, name: `${file}.script-${index++}.js`, text: match[2], lineOffset: lines(start)});
  }
  // Event attributes are executable too. Decode the HTML quoting commonly
  // used around computed property names before parsing the handler body.
  const decode = text => text.replace(/&(?:quot|apos|amp|lt|gt|#\d+|#x[\da-f]+);/gi, entity => {
    const value = entity.slice(1, -1).toLowerCase();
    if (value.startsWith('#')) return String.fromCodePoint(value[1] === 'x' ? parseInt(value.slice(2), 16) : Number(value.slice(1)));
    return {quot: '"', apos: "'", amp: '&', lt: '<', gt: '>'}[value];
  });
  for (const tag of text.matchAll(/<[a-z][\w:-]*\b(?:[^<>"']|"[^"]*"|'[^']*')*>/gi)) {
    if (scriptRanges.some(([start, end]) => tag.index >= start && tag.index < end)) continue;
    const start = /^<[\w:-]+/.exec(tag[0])[0].length;
    for (const match of tag[0].slice(start, -1).matchAll(/([\w:-]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/gi)) {
      if (!/^on[a-z]+$/i.test(match[1])) continue;
      const body = match[2] ?? match[3] ?? match[4] ?? '';
      entries.push({file, name: `${file}.event-${index++}.js`, text: `function __event(event){${decode(body)}\n}`, lineOffset: lines(tag.index + start + match.index)});
    }
  }
  return entries;
}

export function discoverHostSources(directory = root) {
  const entries = [];
  function walk(dir) {
    for (const entry of readdirSync(join(directory, dir), {withFileTypes: true}).sort((a, b) => a.name.localeCompare(b.name))) {
      const file = `${dir}/${entry.name}`;
      if (entry.isDirectory()) { walk(file); continue; }
      if (!entry.isFile() || generated.has(file) || /\.d\.ts$/.test(file)) continue;
      if (!/\.(?:[cm]?js|jsx|tsx?|html|py)$/.test(file)) continue;
      const text = readFileSync(join(directory, file), 'utf8');
      if (/\.(?:html|py)$/.test(file)) entries.push(...shellScripts(file, text));
      else entries.push({file, name: file, text, lineOffset: 0});
    }
  }
  for (const dir of sourceRoots) walk(dir);
  return entries;
}

export function findHostBypasses(entries, approvals = adapters) {
  const files = new Map(entries.map(entry => [entry.name, entry]));
  const options = {allowJs: true, noResolve: true, noLib: true, target: ts.ScriptTarget.Latest, jsx: ts.JsxEmit.Preserve};
  const host = ts.createCompilerHost(options);
  host.getSourceFile = name => {
    const entry = files.get(name);
    // Isolate lexical bindings between pages/scripts. The synthetic export
    // changes no authored offsets and is never executed or written to disk.
    return entry && ts.createSourceFile(name, entry.text + '\nexport {};', options.target, true);
  };
  host.fileExists = name => files.has(name);
  host.readFile = name => files.get(name)?.text;
  host.getCurrentDirectory = () => '';
  const program = ts.createProgram([...files.keys()], options, host);
  const checker = program.getTypeChecker(); // lexical aliases, without resolving dependencies or typechecking
  const findings = [], seen = new Set();
  for (const entry of entries) {
    const source = program.getSourceFile(entry.name);
    if (!source) throw Error(`Could not parse host boundary source: ${entry.name}`);
    const add = (node, reason) => {
      const line = source.getLineAndCharacterOfPosition(node.getStart(source)).line + 1 + (entry.lineOffset ?? 0);
      const key = `${entry.name}:${node.pos}:${reason}`;
      if (!seen.has(key)) findings.push({file: entry.file, line, reason});
      seen.add(key);
    };
    // Syntax errors must not silently hide a newly added native access.
    for (const diagnostic of source.parseDiagnostics) {
      const line = source.getLineAndCharacterOfPosition(diagnostic.start ?? 0).line + 1 + (entry.lineOffset ?? 0);
      findings.push({file: entry.file, line, reason: ts.flattenDiagnosticMessageText(diagnostic.messageText, ' ')});
    }
    if (approvals.has(entry.file)) {
      if (!approvals.get(entry.file)?.trim()) add(source, 'Host adapter approval needs a reason.');
      continue;
    }
    const unwrap = node => {
      while (node && (ts.isParenthesizedExpression(node) || ts.isAsExpression(node) || ts.isNonNullExpression(node) || ts.isTypeAssertionExpression(node) || ts.isSatisfiesExpression(node))) node = node.expression;
      return node;
    };
    const declaration = node => checker.getSymbolAtLocation(node)?.valueDeclaration;
    const constant = (input, seen = new Set()) => {
      const node = unwrap(input);
      if (!node || seen.has(node)) return undefined;
      seen.add(node);
      if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text;
      if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.PlusToken) {
        const left = constant(node.left, new Set(seen)), right = constant(node.right, new Set(seen));
        return left !== undefined && right !== undefined ? left + right : undefined;
      }
      if (ts.isIdentifier(node)) {
        const decl = declaration(node);
        if (decl && ts.isVariableDeclaration(decl)) return constant(decl.initializer, seen);
      }
      return undefined;
    };
    const property = node => ts.isPropertyAccessExpression(node) ? node.name.text
      : ts.isElementAccessExpression(node) ? constant(node.argumentExpression) : undefined;
    const bindingPath = (element, seen) => {
      const parent = element.parent.parent;
      const base = ts.isVariableDeclaration(parent) ? pathOf(parent.initializer, seen) : ts.isBindingElement(parent) ? bindingPath(parent, seen) : [];
      const key = element.propertyName ?? element.name;
      const name = ts.isComputedPropertyName(key) ? constant(key.expression) : ts.isIdentifier(key) || ts.isStringLiteral(key) ? key.text : undefined;
      return [...base, name ?? '?'];
    };
    const pathOf = (input, seen = new Set()) => {
      const node = unwrap(input);
      if (!node || seen.has(node)) return [];
      seen.add(node);
      if (ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node)) return [...pathOf(node.expression, seen), property(node) ?? '?'];
      if (ts.isIdentifier(node)) {
        const decl = declaration(node);
        if (decl && ts.isVariableDeclaration(decl) && decl.initializer) return pathOf(decl.initializer, seen);
        if (decl && ts.isBindingElement(decl)) {
          if (ts.isObjectBindingPattern(decl.parent)) return bindingPath(decl, seen);
        }
        return [node.text];
      }
      return [];
    };
    const prohibited = parts => {
      if (parts.includes('webkit') || parts.includes('messageHandlers')) return 'Native WebKit transport must use host-services.';
      if (parts.some((name, i) => name === 'webview' && parts[i - 1] === 'chrome') || parts.includes('external') && parts.includes('invoke')) return 'Native host transport must use host-services.';
      if (parts.some((name, i) => name === 'clipboard' && parts[i - 1] === 'navigator')) return 'Clipboard access must use the shared host clipboard owner.';
      return undefined;
    };
    const visit = node => {
      if (ts.isPropertyAccessExpression(node) || ts.isElementAccessExpression(node) || ts.isBindingElement(node)) {
        const parts = ts.isBindingElement(node) && ts.isObjectBindingPattern(node.parent) ? bindingPath(node, new Set()) : pathOf(node);
        const reason = prohibited(parts);
        if (reason) add(node, reason);
        // Dynamic access inside a known native transport is never a reason
        // to evade the boundary merely because the final key is unknown.
        if (parts.includes('?') && parts.some(part => ['webkit', 'messageHandlers', 'webview'].includes(part))) add(node, 'Computed native transport access must use host-services.');
      }
      if (ts.isCallExpression(node)) {
        const parts = pathOf(node.expression);
        if (parts.at(-1) === 'execCommand' && parts.includes('document') && ['copy', 'cut'].includes(constant(node.arguments[0]))) add(node, 'Clipboard fallback must use the shared host clipboard owner.');
        if ((node.expression.kind === ts.SyntaxKind.ImportKeyword || ts.isIdentifier(node.expression) && node.expression.text === 'require') && nativeModule(constant(node.arguments[0]) ?? '')) add(node, 'Native bridge imports belong in an approved host adapter.');
      }
      if ((ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) && node.moduleSpecifier && nativeModule(constant(node.moduleSpecifier) ?? '')) add(node, 'Native bridge imports belong in an approved host adapter.');
      ts.forEachChild(node, visit);
    };
    visit(source);
  }
  return findings;
}

if (process.argv[1] === fileURLToPath(import.meta.url) && basename(process.argv[1]) === 'host-boundaries.mjs') {
  const entries = discoverHostSources(), findings = findHostBypasses(entries);
  for (const finding of findings) console.error(`${finding.file}:${finding.line}: ${finding.reason}`);
  if (findings.length) process.exitCode = 1;
  else console.log(`Host boundaries: ${entries.length} authored scripts checked.`);
}
