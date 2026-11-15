"""Embed Pharos's existing local annotation/copy UI in offline research reports."""
import ast
import pathlib
import re

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parent.parent


def inject(doc):
    if 'id="research-annotations"' in doc:
        doc = doc[:doc.index('<div id="research-annotations">')] + doc[doc.rindex('</body>'):]
    source = REPO / 'internal/archive/assets/ui.py'
    app = ast.literal_eval(ast.parse(source.read_text()).body[0].value)
    css = app[app.index('    .feedback-toolbar{'):app.index('    .feedback-positioned{')]
    css += app[app.index('    .feedback-positioned{'):app.index('\n', app.index('    .feedback-positioned{'))]
    markup = app[app.index('<div class="feedback-highlight"'):app.index('<script src="/assets/header.js"')]
    # Screenshot capture needs the running app's image store. These files use
    # the existing local notes/copy path, with no service or network dependency.
    markup = re.sub(r'<button type="button" id="feedbackScreenshot">.*?</button>', '<button type="button" id="feedbackScreenshot" hidden></button>', markup)
    markup = markup.replace('<textarea id="feedbackNote"', '<p class="research-note">Notes stay in this browser. Use Copy feedback and paste them into our chat. Current report controls are captured with each note.</p><textarea aria-label="Annotation feedback" id="feedbackNote"')
    markup = markup.replace('<div class="feedback-toolbar-controls" data-feedback-drag>', '<div class="feedback-toolbar-controls" data-feedback-drag><button id="feedbackToggle" type="button" hidden data-feedback-ui></button>')
    symbols = []
    for name in ['target', 'close', 'grip', 'copy', 'edit', 'chevron-down']:
        symbols.append(re.search(r'<symbol id="ph-icon-' + name + r'".*?</symbol>', app).group())
    sprite = '<svg class="research-icon-sprite" aria-hidden="true"><defs>' + ''.join(symbols) + '</defs></svg>'
    js = app[app.index("const FEEDBACK_KEY="):app.index('async function api(path')]
    js = js.replace("'pharos-ui-feedback-v1'", 'storagePrefix+":notes"').replace("'pharos-ui-feedback-draft-v1'", 'storagePrefix+":draft"').replace("'pharos-ui-feedback-clear-on-copy'", 'storagePrefix+":clear"').replace("'pharos-ui-feedback-layout-v1'", 'storagePrefix+":layout"').replace("'pharos-feedback-enabled'", 'storagePrefix+":enabled"')
    js = js.replace("function feedbackView(){const view=document.querySelector('.view.active');return view?{id:view.id,title:view.querySelector('h1')?.textContent||view.id}:{id:'unknown',title:'Unknown view'}}", "function feedbackView(){return {id:document.querySelector('h1')?.closest('[id]')?.id||'report',title:document.title}}")
    js = js.replace("source:'internal/archive/assets/ui.py (APP_HTML)'", "controls:currentControls(),source:decodeURI(location.pathname)")
    js = js.replace("const element=feedbackHoverElement?.contains(event.target)?feedbackHoverElement:event.target;", "const selected=feedbackHoverElement?.contains(event.target)?feedbackHoverElement:event.target;const element=feedbackHoverIndex?selected:selected.closest('.plot,.js-plotly-plot')||selected;")
    js = js.replace("if(item.text)lines.push", "lines.push('- Page: '+document.title,'- Chart controls: '+JSON.stringify(item.controls||{}));if(item.text)lines.push")
    js = js.replace('let enabled=false;', 'let enabled=true;').replace("enabled=localStorage.getItem(FEEDBACK_ENABLED_KEY)==='true'", "enabled=localStorage.getItem(FEEDBACK_ENABLED_KEY)!=='false'")
    # Keep the toolbar visible in these standalone documents.
    js += '\nsetFeedbackEnabled(true);renderFeedback();\n'
    js += "window.pharosFeedback={annotations:()=>feedbackAnnotations.map(item=>({...item})),markdown:feedbackMarkdown,annotate:()=>setFeedbackMode(true)};\n"
    js = js.replace("pageScroller.addEventListener('scroll',renderFeedbackPins,{passive:true})", "document.addEventListener('scroll',renderFeedbackPins,{passive:true,capture:true})")
    prelude = r'''
(() => {
const $=s=>document.querySelector(s), storagePrefix='pharos-research-feedback:'+location.pathname;
const nativeStorage=(()=>{try{return window.localStorage}catch{return null}})(),memory=new Map();
const localStorage={getItem:k=>{try{return nativeStorage?.getItem(k)??memory.get(k)??null}catch{return memory.get(k)??null}},setItem:(k,v)=>{memory.set(k,v);try{if(!nativeStorage)throw Error();nativeStorage.setItem(k,v)}catch{document.querySelectorAll('.research-note').forEach(el=>el.textContent='Browser storage is unavailable. Copy your feedback before closing this page.')}},removeItem:k=>{memory.delete(k);try{nativeStorage?.removeItem(k)}catch{}}};
const pharosPrefs={get:(k,fallback)=>{try{return JSON.parse(localStorage.getItem(k))??fallback}catch{return fallback}},set:(k,v)=>localStorage.setItem(k,JSON.stringify(v))};
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n}
function icon(name){const svg=document.createElementNS('http://www.w3.org/2000/svg','svg'),use=document.createElementNS('http://www.w3.org/2000/svg','use');svg.classList.add('app-icon');svg.setAttribute('viewBox','0 0 24 24');svg.setAttribute('aria-hidden','true');use.setAttribute('href','#ph-icon-'+name);svg.append(use);return svg}
function iconLabel(name,label){return[icon(name),document.createTextNode(' '+label)]}
function currentControls(){const controls=Object.fromEntries([...document.querySelectorAll('select,input[type="search"],input[type="checkbox"],input[type="date"],input[type="datetime-local"]')].filter(el=>(el.id||el.getAttribute('aria-label'))&&!el.closest('[data-feedback-ui]')).map(el=>[(el.closest('#distribution-dashboard')?'distribution:':'')+(el.id||el.getAttribute('aria-label')),el.tagName==='SELECT'?{value:el.value,label:el.selectedOptions[0]?.textContent}:el.type==='checkbox'?el.checked:el.value]));controls.reportView=document.querySelector('[role=tab][aria-selected=true]')?.textContent;const state=window.distributionDashboardState;if(state)controls.distributionState=Object.fromEntries(['view','groupBy','normalization','minimum','period','from','to','preset'].map(k=>[k,state[k]]));return controls}
'''
    override = r'''
[data-feedback-ui]{--fs-xs:11px;--fs-sm:12px;--lamp-strong:#eab04a;--panel:#fffaf0;--bg:#eee7d8;--line:#d5c8b3;--accent:#315845;--ink:#2c2821;--muted:#756c5f;font:14px/1.5 "Avenir Next",-apple-system,sans-serif;box-sizing:border-box}
[data-feedback-ui] *,[data-feedback-ui] *::before,[data-feedback-ui] *::after{box-sizing:border-box}
[data-feedback-ui] [hidden]{display:none!important}
[data-feedback-ui] button{font:inherit;border:1px solid var(--line);border-radius:4px;padding:6px 10px;cursor:pointer;background:var(--panel);color:var(--ink)}
[data-feedback-ui] button.primary{background:var(--accent);border-color:var(--accent);color:var(--panel)}
[data-feedback-ui] button:disabled{opacity:.5;cursor:default}
[data-feedback-ui] .feedback-pin{display:grid;place-items:center;width:25px;height:25px;padding:0;border:2px solid white;border-radius:50%;background:#7c3aed;color:white;font:bold 12px/1 -apple-system,sans-serif}
[data-feedback-ui] label{display:flex;flex-direction:row;gap:6px;align-items:center;font:12px/1.5 inherit}
[data-feedback-ui] input[type=checkbox]{width:auto;height:auto;flex:none}
[data-feedback-ui] .app-icon{display:inline-block;vertical-align:middle;flex:none;width:18px;height:18px;fill:none;stroke:currentColor;stroke-width:1.5;stroke-linecap:round;stroke-linejoin:round}
.research-icon-sprite{position:absolute;width:0;height:0;overflow:hidden}
.feedback-toolbar{max-width:calc(100vw - 16px)}.feedback-toolbar-controls{flex-wrap:nowrap}
.feedback-composer h2{font-size:19px}.feedback-composer textarea{font:14px/1.5 -apple-system,sans-serif}
.feedback-attachment-options{display:grid;gap:8px}.research-note{font-size:12px;color:var(--muted);line-height:1.5}
.feedback-composer-backdrop{padding:8px}.feedback-composer{width:min(520px,100%);margin:0}
.feedback-item{font-size:13px}.feedback-highlight{box-sizing:border-box}
@media(max-width:520px){.feedback-toolbar{right:8px;bottom:8px}.feedback-composer{padding:14px}.feedback-actions{gap:7px}.feedback-actions .primary{flex-basis:100%}}
@media print{[data-feedback-ui]{display:none!important}}
'''
    bundle = '<div id="research-annotations">' + sprite + '</div><style>' + css + override + '</style>' + markup + '<script>' + prelude + js + '\n})();</script>'
    return doc.replace('</body>', bundle + '</body>')
