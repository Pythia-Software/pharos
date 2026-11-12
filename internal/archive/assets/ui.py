APP_HTML = r'''<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Pharos</title>
  <style>
    :root{color-scheme:light dark;--fs-3xs:9px;--fs-2xs:10px;--fs-xs:11px;--fs-sm:12px;--fs-md:13px;--fs-base:14px;--fs-lg:16px;--fs-xl:18px;--fs-2xl:22px;--fs-3xl:28px;--bg:#eee7d8;--bg-top:var(--bg);--panel:#fffaf0;--ink:#2c2821;--muted:#756c5f;--accent:#315845;--accent-deep:#203f31;--sea:#315845;--gold:#ad823b;--attention-mark:#fff176;--attention-current:#ff9f43;--attention-ring:#e0a800;--attention-ink:#1d1a0a;--focus-ring:var(--accent);--line:#d5c8b3;--warn:#98601d;--bad:#9c3d36;--heat-0:#13808a;--heat-1:#4f5aac;--heat-2:#a43687;--heat-3:#cc3f2f;--serif:Charter,"Iowan Old Style","Palatino Linotype",Palatino,Georgia,serif;--sans:"Avenir Next",Avenir,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
    @media(prefers-color-scheme:dark){:root:not([data-theme]){--bg:#10241b;--bg-top:#163226;--panel:#183529;--ink:#f3ebdc;--muted:#b5ab93;--accent:#d8ae5e;--accent-deep:#f1d690;--sea:#8cc6a0;--gold:#d4ad66;--attention-mark:#ffe94d;--attention-current:#ffa24a;--attention-ring:#ffe94d;--attention-ink:#1d1a0a;--focus-ring:var(--sea);--line:#2d4f40;--warn:#f0955a;--bad:#ef8c84;--heat-0:#6ac9ce;--heat-1:#95a4f6;--heat-2:#e882c8;--heat-3:#ff8068;}}
    :root[data-theme="light"]{color-scheme:light}
    :root[data-theme="dark"]{color-scheme:dark;--bg:#10241b;--bg-top:#163226;--panel:#183529;--ink:#f3ebdc;--muted:#b5ab93;--accent:#d8ae5e;--accent-deep:#f1d690;--sea:#8cc6a0;--gold:#d4ad66;--attention-mark:#ffe94d;--attention-current:#ffa24a;--attention-ring:#ffe94d;--attention-ink:#1d1a0a;--focus-ring:var(--sea);--line:#2d4f40;--warn:#f0955a;--bad:#ef8c84;--heat-0:#6ac9ce;--heat-1:#95a4f6;--heat-2:#e882c8;--heat-3:#ff8068;}
    *{box-sizing:border-box}html,body{height:100%}body{margin:0;display:flex;flex-direction:column;overflow:hidden;background:radial-gradient(circle at 18% -10%,color-mix(in srgb,var(--gold) 9%,transparent),transparent 34%),linear-gradient(var(--bg-top),var(--bg) 520px) no-repeat,var(--bg);color:var(--ink);font:var(--fs-base)/1.5 var(--sans)}
    header{position:sticky;top:0;z-index:2;background:color-mix(in srgb,var(--bg) 91%,transparent);backdrop-filter:blur(16px);border-bottom:1px solid var(--line);box-shadow:0 1px 0 color-mix(in srgb,var(--gold) 15%,transparent)}
    .bar{max-width:1180px;margin:auto;padding:13px 22px;display:flex;gap:var(--header-bar-gap,18px);align-items:center}.brand{display:flex;align-items:center;gap:11px}.brand-mark{display:block;flex:none;width:37px;height:42px}.brand-mark .tower{fill:#eadcbc}.brand-mark .brass{fill:var(--mast-brass)}.brand-mark .door{fill:var(--mast-bottom)}.brand-name{font:700 var(--fs-2xl)/.95 var(--serif);letter-spacing:-.025em}.brand-tagline{display:block;margin-top:4px;color:#dcb466;font:600 var(--fs-3xs)/1 var(--sans);letter-spacing:.13em;text-transform:uppercase}.tabs{display:flex;gap:4px;margin-left:auto}.header-icon{display:grid;place-items:center;width:32px;height:32px;min-width:32px;padding:0;border:1px solid var(--line);background:var(--panel);color:var(--ink);font-size:var(--fs-xl);line-height:1}.header-icons{display:flex;gap:var(--header-control-gap,6px);margin-left:8px;flex-wrap:wrap;min-width:0;justify-content:flex-end}.header-history{display:inline-flex}.header-icons>[hidden]{display:none}.header-icon:hover{border-color:var(--accent);color:var(--accent)}.header-icon[aria-pressed="true"],.header-icon.active{background:var(--accent);color:var(--panel)}body:not(.feedback-enabled) .feedback-toolbar,body:not(.feedback-enabled) .feedback-panel,body:not(.feedback-enabled) #feedbackPins{display:none}
    @media(min-width:761px){.bar{--header-bar-gap:14px}.tabs button{padding-inline:10px}.tabs,.header-icons{flex:none}}
    @media(max-width:1120px){.brand-tagline{display:none}}
    .header-icon.upgrade-toggle{display:inline-flex;width:auto;min-width:0;padding:0 10px;white-space:nowrap;font-size:var(--fs-sm);font-weight:600;line-height:1;flex:none}
    @media(min-width:761px) and (max-width:1180px){.bar{flex-wrap:wrap;row-gap:8px}.brand{order:1}.header-icons{order:2}.tabs{order:3;min-width:62%;margin:0}}
    button,input,select{font:inherit;font-size:var(--fs-md)}select:not([multiple]){appearance:none;box-shadow:none;border-radius:5px;padding-right:28px}td button,th button{font-size:inherit}button{border:0;border-radius:4px;padding:5px 12px;background:transparent;color:var(--muted);cursor:pointer}.tabs button.active,.primary{background:var(--accent);color:var(--panel)}
    .header-icon svg{width:18px;height:18px;stroke:currentColor;stroke-width:2;fill:none;stroke-linecap:round;stroke-linejoin:round}.header-icons .header-icon[hidden]{display:none}.navigator-toggle[aria-expanded="true"]{background:var(--accent);color:var(--panel)}.navigator{display:none;gap:8px;align-items:center;padding:8px max(22px,calc((100% - 1180px)/2 + 22px));border-top:1px solid var(--line);background:color-mix(in srgb,var(--bg) 70%,var(--panel))}.navigator.open{display:flex}.navigator-uri{position:relative;flex:1;min-width:0;display:flex}.navigator input{flex:1;min-width:0;height:32px;padding:5px 10px;border:1px solid var(--line);border-radius:4px;background:var(--panel);color:var(--ink);font:var(--fs-sm) ui-monospace,SFMono-Regular,monospace}.navigator input:has(+.navigator-go:not([hidden])){padding-right:34px}.navigator .navigator-go{position:absolute;top:3px;right:3px;display:grid;place-items:center;width:26px;height:26px;min-height:26px;padding:0;border:0;border-radius:3px}.navigator .navigator-go[hidden]{display:none}.navigator-go svg{width:16px;height:16px;stroke:currentColor;stroke-width:2;fill:none;stroke-linecap:round;stroke-linejoin:round}.navigator button{height:32px;min-height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink)}.navigator .navigator-error{color:var(--bad);font-size:var(--fs-sm)}.navigator .navigator-error:empty{display:none}.navigator .header-icon{width:32px;min-width:32px;padding:0}.navigator .header-icon:disabled{opacity:.4;cursor:default}.navigator #bookmarkStar{margin-left:6px}.bookmark-star.bookmarked{color:var(--mast-brass,var(--accent))}.bookmark-star.bookmarked svg{fill:currentColor}.search-dialog{width:min(560px,calc(100% - 32px));margin:12vh auto auto;padding:0;border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--ink);box-shadow:0 24px 70px #0007}.search-dialog::backdrop{background:#0006}.search-dialog form{display:grid;gap:14px;padding:16px 18px}.search-dialog-heading{display:flex;align-items:center;justify-content:space-between;gap:12px}.search-dialog h2{margin:0;font-size:var(--fs-lg)}.search-dialog-field{display:flex;gap:8px}.search-dialog-field select,.search-dialog-field input{height:36px;padding:5px 10px;border:1px solid var(--line);border-radius:4px;background-color:var(--bg);color:var(--ink)}.search-dialog-field select{flex:none;padding-right:28px}.search-dialog-field input{flex:1;min-width:0;font-size:var(--fs-base)}.search-dialog-field input::placeholder{color:var(--muted)}.search-dialog-options{display:flex;flex-wrap:wrap;gap:6px 18px;margin:0;padding:0;border:0;color:var(--muted);font-size:var(--fs-sm)}.search-dialog-options[hidden]{display:none}.search-dialog-options legend{float:left;margin-right:4px;padding:0;font-weight:600}.search-dialog-options label{display:flex;align-items:center;gap:5px;cursor:pointer}.search-dialog-options input{accent-color:var(--accent)}.search-dialog-footer{display:flex;align-items:center;justify-content:space-between;gap:12px;color:var(--muted);font-size:var(--fs-sm)}.search-dialog-footer kbd{padding:1px 5px;border:1px solid var(--line);border-radius:3px;font:inherit}.search-dialog-footer .primary{padding:6px 16px}@media(max-width:560px){.search-dialog-field{flex-direction:column}}.bookmark-popover{position:fixed;inset:auto;margin:0;width:300px;padding:14px 16px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);box-shadow:0 12px 32px #0003}.bookmark-popover h2{margin:0 0 10px;font-size:var(--fs-lg)}.bookmark-popover label{display:grid;gap:4px;font-size:var(--fs-sm);color:var(--muted)}.bookmark-popover input{height:32px;padding:5px 8px;border:1px solid var(--line);border-radius:4px;background:var(--bg);color:var(--ink);font:inherit;font-size:var(--fs-base)}.bookmark-popover-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:12px}.bookmark-popover-actions button{border:1px solid var(--line)}
    body>header{position:relative;flex:none}main{position:relative;flex:1;min-height:0;overflow:auto}main>.view{max-width:1180px;margin:auto;padding:28px 22px 80px}.view{display:none}.view.active{display:block}.search{display:grid;grid-template-columns:1fr 170px 170px auto;gap:10px;margin-bottom:22px}.search input,.search select{width:100%;border:1px solid var(--line);border-radius:5px;background-color:var(--panel);color:var(--ink);padding:7px 12px}.search select{padding-right:28px}
    .filters{grid-column:1/-1}.filters summary{color:var(--accent);cursor:pointer}.filtergrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(145px,1fr));gap:8px;margin-top:10px}.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(315px,1fr));gap:14px}.card{background:var(--panel);border:1px solid var(--line);border-radius:5px;padding:17px;box-shadow:0 4px 18px #34230d0b;cursor:pointer}.card:hover{border-color:var(--gold)}h1,h2{font-family:var(--serif)}h1{font-size:var(--fs-3xl);letter-spacing:-.035em}h2{font-size:var(--fs-xl);margin:0 0 8px}.meta,.muted{color:var(--muted);font-size:var(--fs-md)}.chips{display:flex;flex-wrap:wrap;gap:6px;margin-top:13px}.chip{border:1px solid var(--line);border-radius:20px;padding:3px 8px;font-size:var(--fs-sm)}.status{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--sea);margin-right:7px}.status.warn{background:var(--warn)}.status.bad{background:var(--bad)}
    .empty{padding:52px;text-align:center;color:var(--muted);border:1px dashed var(--line);border-radius:6px}.split{display:grid;grid-template-columns:240px 1fr;gap:22px}.panel{background:var(--panel);border:1px solid var(--line);border-radius:6px;padding:18px;margin-bottom:14px}.metric{display:flex;justify-content:space-between;border-bottom:1px solid var(--line);padding:8px 0}.message{border-left:3px solid var(--line);padding:4px 0 4px 12px;margin:12px 0;white-space:pre-wrap}.message.user{border-color:var(--accent)}.message .role{text-transform:uppercase;font-size:var(--fs-xs);color:var(--muted);letter-spacing:.08em}.file{font:var(--fs-md) ui-monospace,SFMono-Regular,monospace;padding:5px 0}.warntext{color:var(--warn)}.badtext{color:var(--bad)}a{color:var(--accent);text-underline-offset:2px}
    .conversation-panel{padding:0}.conversation-head{padding:18px;border-bottom:1px solid var(--line)}.conversation-title-row{display:flex;align-items:flex-start;gap:12px}.conversation-title-row>div{flex:1}.conversation-toolbar{display:flex;gap:9px;align-items:center;margin-top:14px}.conversation-toolbar input{flex:1;min-width:120px;border:1px solid var(--line);border-radius:5px;padding:5px 11px;background:var(--bg);color:var(--ink)}.source-details{margin-top:9px;font-size:var(--fs-sm);color:var(--muted)}.source-details>summary{cursor:pointer}.source-details dl{display:grid;grid-template-columns:auto 1fr;gap:3px 10px}.source-details dd{margin:0;overflow-wrap:anywhere}.turn-list{padding:14px}.turn{margin-bottom:12px;border:1px solid var(--line);border-radius:6px;background:color-mix(in srgb,var(--panel) 80%,var(--bg))}.turn>summary{display:flex;align-items:center;gap:10px;padding:12px 14px;cursor:pointer;list-style:none}.turn>summary::-webkit-details-marker{display:none}.turn>summary::before{content:'›';color:var(--gold);font:bold var(--fs-2xl)/1 var(--serif);transition:transform .16s}.turn[open]>summary::before{transform:rotate(90deg)}.turn-summary-text{flex:1;min-width:0}.turn-summary-prompt{font-weight:650;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.turn-body{padding:2px 14px 14px}.speaker-row{display:flex;gap:9px;align-items:flex-start;margin:11px 0}.speaker-row.human{justify-content:flex-end}.avatar{flex:0 0 28px;height:28px;display:grid;place-items:center;border-radius:50%;background:var(--accent);color:var(--panel);font:bold var(--fs-sm)/1 var(--serif)}.speaker-row.human .avatar{order:2;background:var(--gold)}.chat-bubble{max-width:min(780px,86%);padding:10px 12px;border:1px solid var(--line);border-radius:3px 6px 6px;background:var(--panel);white-space:pre-wrap;overflow-wrap:anywhere}.speaker-row.human .chat-bubble{border-radius:6px 3px 6px;background:color-mix(in srgb,var(--gold) 9%,var(--panel))}.event-time{margin-top:5px;color:var(--muted);font-size:var(--fs-xs)}.tool-cluster,.subagent-run,.tool-event,.structured-event{margin:9px 0;border:1px solid var(--line);border-radius:5px;background:var(--panel)}.tool-cluster>summary,.subagent-run>summary,.tool-event>summary,.structured-event>summary{padding:9px 11px;cursor:pointer;color:var(--muted)}.tool-cluster-body,.subagent-body,.event-body{padding:0 11px 10px}.subagent-run{border-color:color-mix(in srgb,var(--sea) 50%,var(--line));background:color-mix(in srgb,var(--sea) 5%,var(--panel))}.subagent-run>summary{color:var(--sea);font-weight:650}.tool-event{margin:7px 0;background:var(--bg)}.tool-event.error{border-color:var(--bad)}.json-tree{font:var(--fs-sm)/1.5 ui-monospace,SFMono-Regular,monospace}.json-node{margin:5px 0 5px 12px}.json-node>summary{cursor:pointer;color:var(--accent)}.json-row{display:grid;grid-template-columns:minmax(90px,180px) 1fr;gap:9px;padding:3px 0;border-bottom:1px solid color-mix(in srgb,var(--line) 55%,transparent)}.json-key{color:var(--gold);overflow-wrap:anywhere}.json-value{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;color:var(--ink)}.conversation-event[hidden],.turn[hidden]{display:none}
    .drawer{position:fixed;z-index:9000;right:0;top:var(--drawer-top,0px);bottom:0;width:min(400px,calc(100vw - 24px));display:flex;flex-direction:column;background:var(--bg);border-left:1px solid var(--line);box-shadow:-18px 0 45px #0003;transform:translateX(100%);visibility:hidden;transition:transform .2s ease,visibility 0s .2s}.drawer.open{transform:none;visibility:visible;transition:transform .2s ease}@media(prefers-reduced-motion:reduce){.drawer,.drawer.open{transition:none}}.drawer-head{display:flex;align-items:center;gap:10px;padding:16px 18px 10px;border-bottom:0;box-shadow:none}.drawer-head h2{flex:1;margin:0;font-size:var(--fs-xl)}.drawer-close{padding:3px 8px}.drawer-tools{display:flex;gap:8px;padding:0 18px 10px}.drawer-tools input{flex:1;min-width:0;height:32px;box-sizing:border-box;-webkit-appearance:none;appearance:none;border:1px solid var(--line);border-radius:5px;background:var(--panel);color:var(--ink);padding:0 12px;font-size:var(--fs-base)}.drawer-tools input::placeholder{color:var(--muted)}:is(#bookmarkAdd,#historyClear){height:32px;border:1px solid var(--line);background:var(--panel);color:var(--ink)}:is(#bookmarkAdd,#historyClear):hover:not(:disabled){border-color:var(--accent);color:var(--accent)}#bookmarksList{display:flex;flex-direction:column}#bookmarksList>.empty{flex:1;display:flex;align-items:center;justify-content:center;padding:24px}.drawer-hint{margin:0;padding:0 18px 8px;font-size:var(--fs-sm)}.drawer-body{flex:1;overflow:auto;padding:0 18px 18px}.bookmark-list{list-style:none;margin:0;padding:0;border:1px solid var(--line);border-radius:6px;background:var(--panel)}.bookmark-entry{display:flex;align-items:center;gap:8px;padding:8px 10px;border-top:1px solid var(--line);position:relative}.bookmark-entry:first-child{border-top:0}.bookmark-entry.dragging{opacity:.45}.bookmark-entry.drop-before{box-shadow:inset 0 2px 0 var(--accent)}.bookmark-entry.drop-after{box-shadow:inset 0 -2px 0 var(--accent)}.bookmark-handle{flex:none;cursor:grab;color:var(--muted);user-select:none;letter-spacing:-2px}.bookmark-rename{flex:1;min-width:0}.bookmark-edit,.bookmark-remove{flex:none;visibility:hidden;border:0;background:none;color:var(--muted);cursor:pointer;padding:2px 4px}.bookmark-entry:hover :is(.bookmark-edit,.bookmark-remove),.bookmark-entry:focus-within :is(.bookmark-edit,.bookmark-remove){visibility:visible}.bookmark-edit:hover,.bookmark-remove:hover{color:var(--ink)}.history-day{font-size:var(--fs-md);color:var(--muted);margin:12px 0 6px}.history-day:first-child{margin-top:4px}.history-day-toggle{display:flex;align-items:center;gap:6px;width:100%;padding:2px 0;background:none;color:inherit;font:inherit;text-align:left}.history-day-toggle:hover{color:var(--ink)}.history-chevron{display:inline-block;width:10px;font-size:var(--fs-2xs);transition:transform .15s}.history-day-toggle[aria-expanded="false"] .history-chevron{transform:rotate(-90deg)}.history-count{font-weight:400}.history-group[hidden]{display:none}.history-group{list-style:none;margin:0;padding:0;border:1px solid var(--line);border-radius:6px;background:var(--panel)}.history-entry{display:flex;align-items:center;gap:10px;padding:5px 8px 5px 10px;border-top:1px solid var(--line);font-size:var(--fs-md)}.history-entry:first-child{border-top:0}.history-time{flex:none;width:58px;color:var(--muted);font-size:var(--fs-sm);font-variant-numeric:tabular-nums}.history-entry .history-url{font-size:var(--fs-xs)}.history-star{flex:none;visibility:hidden;border:0;background:none;color:var(--muted);cursor:pointer;padding:2px 4px;font-size:var(--fs-base);line-height:1}.history-star.bookmarked{visibility:visible;color:var(--gold,var(--accent))}.history-star:hover{color:var(--accent)}.history-entry:hover .history-star,.history-star:focus-visible{visibility:visible}.history-link,.bookmark-link{flex:1;min-width:0;display:flex;flex-direction:column;gap:2px;color:inherit;text-decoration:none}.history-link:hover .history-title,.bookmark-link:hover .history-title{color:var(--accent);text-decoration:underline}.history-title,.history-url{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.history-url{font-size:var(--fs-sm);color:var(--muted)}.history-remove{flex:none;visibility:hidden;border:0;background:none;color:var(--muted);cursor:pointer;padding:2px 4px}.history-entry:hover .history-remove,.history-remove:focus-visible{visibility:visible}.history-remove:hover{color:var(--ink)}.settings-section{padding:22px 0;border-bottom:1px solid var(--line)}.settings-section:first-of-type{padding-top:8px}.settings-section:last-child{border-bottom:0}.settings-section>h2,.settings-section .view-heading h2{margin:0 0 6px}.settings-section .health{margin-top:12px}.health{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:14px}.big{font:700 var(--fs-3xl)/1.2 var(--serif)}.back{padding-left:0;color:var(--accent)}.view-heading{display:flex;justify-content:space-between;gap:20px;align-items:end;margin-bottom:22px}.view-heading h1{margin-bottom:4px}.source-toolbar{display:flex;align-items:center;flex-wrap:wrap;gap:8px 14px;margin-bottom:16px;padding:12px 14px;border:1px solid var(--line);border-radius:6px;background:var(--panel)}.source-toolbar>div{display:flex;align-items:center;flex-wrap:wrap;gap:8px}.source-toolbar #sourceRunActions{margin-left:auto}.source-toolbar button{border:1px solid var(--line);background:var(--bg);color:var(--ink)}.source-toolbar button.primary{border-color:var(--accent);background:var(--accent);color:var(--panel)}.source-summary{display:flex;gap:8px;flex-wrap:wrap;margin-bottom:16px}.source-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(340px,1fr));gap:14px}.source-card{background:var(--panel);border:1px solid var(--line);border-radius:6px;padding:17px;box-shadow:0 4px 18px #34230d0b}.source-card.attention{border-color:color-mix(in srgb,var(--warn) 55%,var(--line))}.source-card-head{display:flex;align-items:flex-start;justify-content:space-between;gap:12px}.source-card-head>div:first-child{flex:1;min-width:0}.source-card-head h2{margin-bottom:5px}.source-card-head .source-state{display:inline-block}.source-switch{display:flex;align-items:center;gap:8px;white-space:nowrap;color:var(--muted);font-size:var(--fs-sm)}.source-switch .toggle{flex:none}.source-path{margin:11px 0;padding:8px 10px;border-radius:4px;background:color-mix(in srgb,var(--bg) 72%,transparent);color:var(--muted);font:var(--fs-sm)/1.35 ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}.source-facts{display:grid;grid-template-columns:auto 1fr;gap:4px 12px;font-size:var(--fs-md)}.source-facts dt{color:var(--muted)}.source-facts dd{margin:0}.toggle{position:relative;width:38px;height:22px;padding:0;border-radius:20px;background:var(--line)}.toggle::after{content:"";position:absolute;top:3px;left:3px;width:16px;height:16px;border-radius:50%;background:var(--panel);box-shadow:0 1px 3px #0004;transition:.16s}.toggle.on{background:var(--accent)}.toggle.on::after{left:19px}.sync-note{margin-top:9px;font-size:var(--fs-sm)}.button-with-icon{display:inline-flex;align-items:center;gap:7px;border:1px solid var(--line);background:var(--panel);color:var(--ink)}.health-detail{margin-top:7px;overflow-wrap:anywhere}button:disabled{cursor:default;opacity:.5}@media(max-width:760px){.bar{--header-bar-gap:8px;flex-wrap:wrap}.brand{order:1}.header-icons{order:2}.tabs{order:3;flex:1 1 100%;margin:0;min-width:0;overflow:auto}.search{grid-template-columns:1fr}.split{grid-template-columns:1fr}.tabs button{padding:4px 7px}.brand-tagline{display:none}.brand-mark,.brand-beacon{width:30px;height:34px}.source-grid{grid-template-columns:1fr}.view-heading{display:block}.view-heading button{margin-top:10px}}
    .feedback-toolbar{position:fixed;right:22px;bottom:22px;z-index:10000;display:flex;flex-direction:column;width:max-content;max-height:calc(100vh - 16px);background:var(--panel);border:1px solid var(--line);border-radius:6px;box-shadow:0 8px 30px #0003;overflow:hidden}.feedback-toolbar.expanded{right:clamp(8px,calc(100vw - 448px),22px);width:min(440px,calc(100vw - 16px))}.feedback-toolbar-controls{display:flex;align-items:center;gap:8px;padding:7px;flex:none;flex-wrap:wrap}.feedback-toolbar [hidden]{display:none!important}.feedback-toolbar button{border:1px solid var(--line);background:var(--panel);color:var(--ink)}.feedback-toolbar button.active{background:var(--accent);border-color:var(--accent);color:var(--panel)}.feedback-toolbar button.feedback-copy-ready{background:var(--sea);border-color:var(--sea);color:var(--panel)}.feedback-toolbar button.feedback-copy-clears{background:var(--bad);border-color:var(--bad);color:var(--bg)}.feedback-count{min-width:22px;display:inline-block;padding:1px 6px;margin-left:4px;border-radius:6px;background:color-mix(in srgb,var(--accent) 18%,transparent);font-size:var(--fs-sm)}
    body.feedback-targeting,body.feedback-targeting *{cursor:crosshair!important}.feedback-highlight{display:none;position:fixed;z-index:9997;pointer-events:none;border:2px solid #7c3aed;border-radius:3px;background:#7c3aed18;box-shadow:0 0 0 1px #fff8}.feedback-highlight.visible{display:block}.feedback-highlight span{position:absolute;left:-2px;bottom:calc(100% + 4px);max-width:360px;padding:4px 7px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;border-radius:3px;background:#6d28d9;color:white;font:var(--fs-sm)/1.3 ui-monospace,SFMono-Regular,monospace;box-shadow:0 2px 8px #0004}
    .feedback-pin{position:fixed;z-index:9998;display:grid;place-items:center;width:25px;height:25px;padding:0;border:2px solid white;border-radius:50%;background:#7c3aed;color:white;text-align:center;font:bold var(--fs-sm)/1 ui-sans-serif,sans-serif;box-shadow:0 2px 8px #0005;pointer-events:none}.feedback-panel{display:none;flex-direction:column;min-height:0;max-height:620px;overflow:hidden;border-top:1px solid var(--line)}.feedback-panel.open{display:flex}.feedback-item button{padding:3px 7px}.feedback-item .feedback-number{padding:0;border:0;background:#7c3aed;color:white}.feedback-item button:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}.feedback-list{overflow:auto;padding:12px}.feedback-item{padding:12px;margin-bottom:9px;border:1px solid var(--line);border-radius:5px;background:var(--bg)}.feedback-item-top{display:flex;gap:8px;align-items:flex-start}.feedback-number{flex:0 0 23px;height:23px;border-radius:50%;background:#7c3aed;color:white;text-align:center;font:bold var(--fs-sm)/23px ui-sans-serif,sans-serif}.feedback-note{flex:1;min-width:0;white-space:pre-wrap;overflow-wrap:anywhere}.feedback-edit{display:flex;flex-direction:column;gap:8px;white-space:normal}.feedback-edit textarea{width:100%;box-sizing:border-box;min-height:100px;resize:vertical;padding:7px;border:1px solid var(--line);border-radius:4px;background:var(--panel);color:var(--ink);font:inherit}.feedback-edit-actions{display:flex;justify-content:flex-end;gap:6px}.feedback-selector{margin-top:7px;color:var(--muted);font:var(--fs-xs)/1.35 ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}.feedback-actions{display:flex;flex:none;align-items:center;flex-wrap:wrap;gap:8px;padding:12px;border-top:1px solid var(--line)}.feedback-actions .primary{flex:1}.feedback-copy-option{display:flex;align-items:center;gap:5px;color:var(--muted);font-size:var(--fs-sm);white-space:nowrap;cursor:pointer}.feedback-copy-option input{accent-color:var(--accent)}.feedback-composer-backdrop{position:fixed;inset:0;z-index:10002;display:none;place-items:center;padding:20px;background:#0005}.feedback-composer-backdrop.open{display:grid}.feedback-composer{width:min(520px,100%);padding:18px;background:var(--panel);border:1px solid var(--line);border-radius:6px;box-shadow:0 18px 60px #0007}.feedback-composer h2{margin-bottom:4px}.feedback-composer textarea{width:100%;min-height:120px;resize:vertical;margin:15px 0 12px;padding:6px 11px;border:1px solid var(--line);border-radius:5px;background:var(--bg);color:var(--ink);font:inherit}.feedback-composer .row{display:flex;justify-content:flex-end;gap:8px}.feedback-toast{position:fixed;z-index:10003;left:50%;bottom:28px;transform:translate(-50%,20px);padding:9px 14px;border-radius:4px;background:var(--ink);color:var(--panel);opacity:0;pointer-events:none;transition:.18s}.feedback-toast.visible{opacity:1;transform:translate(-50%,0)}
    [data-feedback-drag],body.feedback-targeting [data-feedback-drag],body.feedback-targeting [data-feedback-drag] *{cursor:grab!important;touch-action:none;user-select:none;-webkit-user-select:none}
    body.feedback-dragging,body.feedback-dragging *{cursor:grabbing!important}
    .feedback-drag-handle{display:inline-grid;place-items:center;flex:none;align-self:center;width:26px;height:28px;padding:0!important;border:0!important;background:transparent!important;color:var(--muted);cursor:grab!important;touch-action:none}
    .feedback-drag-handle:hover{color:var(--accent)}.feedback-drag-handle:focus-visible,[data-feedback-drag]:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}
    .feedback-toolbar{max-width:calc(100vw - 16px)}body.feedback-targeting .feedback-toolbar button:not(.feedback-drag-handle),.feedback-toolbar button:not(.feedback-drag-handle){cursor:pointer!important}
    .feedback-list{min-height:0}
    .feedback-composer{max-width:calc(100vw - 16px);max-height:calc(100vh - 16px);overflow:auto}.feedback-composer-heading{display:flex;align-items:center;gap:8px}.feedback-composer-heading h2{margin:0}
    .feedback-highlight span{pointer-events:auto;max-width:min(360px,calc(100vw - 16px))}.feedback-pin{pointer-events:auto}
    .feedback-toast{display:flex;align-items:center;gap:8px;max-width:calc(100vw - 16px);visibility:hidden;transition:opacity .18s,visibility .18s}.feedback-toast.visible{visibility:visible;pointer-events:auto}.feedback-toast .feedback-drag-handle{color:inherit}
    .feedback-positioned{position:fixed!important;right:auto!important;bottom:auto!important;margin:0!important;transform:none!important}
    .optional-behavior{display:flex;align-items:center;justify-content:space-between;gap:24px;padding:16px 0;border-bottom:1px solid var(--line)}.optional-behavior:last-child{border-bottom:0}.optional-behavior>div{min-width:0}.optional-behavior h3{margin:0;font-size:var(--fs-base)}.optional-behavior p{margin:4px 0 0}.optional-behavior .toggle{flex:none}.optional-behavior input[type=email]{flex:0 1 260px;min-width:0;height:32px;box-sizing:border-box;padding:5px 8px;border:1px solid var(--line);border-radius:4px;background:var(--bg);color:var(--ink);font:inherit;font-size:var(--fs-base)}.optional-behavior .toggle:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:3px}
    .settings-content{min-width:0}.settings-content .settings-section{padding:0;border:0}.settings-content .settings-section[hidden]{display:none}.settings-content .health{margin-top:0}.settings-content .source-card-head h3{margin-bottom:5px;font-size:var(--fs-xl)}.settings-content .findings-settings{max-width:none}.source-group-heading{grid-column:1/-1;margin:12px 0 0;font:600 var(--fs-lg)/1.3 var(--serif)}.source-removal{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin-top:10px}.source-removal>p{flex:1 1 100%;margin:0}.source-remove{border:1px solid var(--line);background:var(--bg);color:var(--ink);font-size:var(--fs-sm)}.source-remove.danger{border-color:var(--bad);color:var(--bad)}.settings-content .findings-setting{margin-bottom:14px}
    @media(max-width:760px){.source-toolbar #sourceRunActions{margin-left:0}}
    :root{--header-control-gap:6px}.header-editor{margin-top:24px}.header-editor h3{margin:0 0 12px;font-size:var(--fs-base)}.header-preview{overflow:hidden;border:1px solid var(--line);border-radius:8px;background:linear-gradient(180deg,var(--mast-top),var(--mast-bottom));box-shadow:0 4px 20px #0001}.header-preview-bar.bar{max-width:none;margin:0;padding-bottom:20px;row-gap:25px}.header-preview-brand{flex:none;opacity:.65}.header-preview-context.tabs{opacity:.55}.header-tool-list{display:flex;align-items:start;flex-wrap:wrap;gap:var(--header-control-gap);list-style:none;min-width:0;min-height:50px;margin:0;padding:0}.header-preview .header-tool-list{min-height:32px;margin-left:8px;row-gap:23px}.header-preview .header-tool{height:32px}.header-preview .header-drag-handle{position:absolute;top:35px;left:50%;transform:translateX(-50%)}.header-tool{position:relative;display:flex;flex-direction:column;align-items:center;gap:3px;flex:none;min-width:0;max-width:100%;border-radius:4px}.header-tool-face{display:flex;align-items:center;justify-content:center;height:32px;padding:0;cursor:grab;outline-offset:2px}.header-tool-face:focus-visible{outline:2px solid var(--focus-ring, var(--accent));border-radius:4px}.header-tool-face>*{pointer-events:none}.header-tool-face .header-icon svg{width:18px;height:18px;stroke:currentColor;stroke-width:2;fill:none;stroke-linecap:round;stroke-linejoin:round}.header-tool-face .header-icon[hidden]{display:grid}.header-tool .header-drag-handle{display:flex;align-items:center;justify-content:center;width:24px;height:14px;min-height:0;padding:0;border:1px solid color-mix(in srgb,var(--mast-border) 70%,transparent);border-radius:3px;background:var(--mast-field);color:var(--mast-muted);cursor:grab;touch-action:none}.header-drag-handle svg{width:14px;height:12px;stroke:currentColor;stroke-width:2;fill:none;stroke-linecap:round;stroke-linejoin:round;transform:rotate(90deg)}.header-tool .header-drag-handle:hover{border-color:var(--mast-brass);color:var(--mast-hover)}.header-drag-handle:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}.header-tool.dragging{opacity:.7}.header-tool.drop-before::before,.header-tool.drop-after::after{content:"";position:absolute;top:0;height:32px;width:3px;border-radius:2px;background:var(--accent);box-shadow:0 0 0 2px var(--panel)}.header-tool.drop-before::before{left:-4px}.header-tool.drop-after::after{right:-4px}.header-tool-list.drop-target{background:color-mix(in srgb,var(--accent) 8%,transparent);border-radius:4px}.header-toolbox-section{margin-top:26px}.header-toolbox-section>p{margin:0 0 12px}.header-toolbox-section .header-tool-list{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,140px),1fr));align-items:stretch;gap:12px;min-height:100px;padding:14px;border:1px dashed var(--line);border-radius:8px;background:var(--panel)}.header-toolbox-section .header-tool{padding:8px 12px 4px;border:1px dotted var(--line);border-radius:6px;background:transparent}.header-toolbox-section .header-tool:hover,.header-toolbox-section .header-tool:focus-within{border-color:var(--accent)}.header-toolbox-section .header-tool-face{width:100%;min-width:0}.header-toolbox-section .header-tool-face>*,.header-toolbox-section .combo-button{min-width:0;max-width:100%}.header-toolbox-section .header-tool-list>.muted{grid-column:1 / -1}.header-tool-caption{width:100%;margin-top:4px;font-size:var(--fs-sm);line-height:1.3;color:var(--muted);text-align:center;white-space:normal;overflow-wrap:anywhere}.header-tool-list>.muted{padding:8px;font-size:var(--fs-sm)}.header-tool-menu{position:fixed;inset:auto;margin:0;padding:6px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);box-shadow:0 8px 24px #0004}.header-tool-menu:popover-open{display:flex;gap:6px}.header-tool-menu button{display:inline-flex;align-items:center;justify-content:center;gap:6px;min-height:30px;padding:4px 8px;border:1px solid var(--line)}.header-tool-menu svg{width:14px;height:14px;stroke:currentColor;stroke-width:2;fill:none;stroke-linecap:round;stroke-linejoin:round}
    .layout-preference-card{margin-bottom:16px;border:1px solid var(--line);border-radius:8px;background:var(--panel);box-shadow:0 4px 18px #34230d0b}.layout-preference-card>.optional-behavior{padding:16px 20px;border-bottom:0}
    #quickStatsEditor{min-width:0;margin-block:20px}.quick-stats-preview-heading{margin:0 0 12px;font-size:var(--fs-base)}.usage-summary.quick-stats-preview{position:relative;z-index:auto;display:inline-block;vertical-align:top;max-width:100%;box-sizing:border-box}.quick-stats-preview .usage-summary-scroll{padding-top:24px}.quick-stats-preview table{margin-left:24px}.quick-stats-preview tbody th,.quick-stats-preview thead th{position:relative}.quick-stats-preview tbody th{cursor:grab}.quick-stats-preview thead th[data-group]{cursor:grab}.quick-stats-preview a:not([href]){cursor:grab}.quick-stats-preview .quick-stats-handle{position:absolute;display:grid;place-items:center;width:20px;height:20px;min-height:0;padding:2px;border:1px solid var(--line);border-radius:4px;background:var(--panel);color:var(--muted);cursor:grab;touch-action:none}.quick-stats-handle .app-icon{width:14px;height:14px}.quick-stats-preview .quick-stats-row-handle{left:-24px;top:50%;transform:translateY(-50%)}.quick-stats-preview .quick-stats-column-handle{right:0;top:-24px}.quick-stats-column-handle .app-icon{transform:rotate(90deg)}.quick-stats-preview .quick-stats-handle:hover{border-color:var(--accent);color:var(--ink)}.quick-stats-preview .quick-stats-handle:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}.quick-stats-preview-note{font-size:var(--fs-sm)}.quick-stats-toolboxes{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:24px;margin-top:24px}.quick-stats-toolbox-section{min-width:0}.quick-stats-toolbox-section h4{margin:0 0 8px}.quick-stats-toolbox-section>p{font-size:var(--fs-sm)}.quick-stats-toolbox{min-height:70px;padding:12px;border:1px dashed var(--line);border-radius:8px}.quick-stats-toolbox>.muted{margin:0;font-size:var(--fs-sm)}.quick-stats-toolbox .usage-summary{margin:0;max-width:100%}.quick-stats-toolbox .usage-summary-scroll{padding-top:0}.quick-stats-toolbox table{margin-left:0}.quick-stats-toolbox [data-group]{cursor:grab}.quick-stats-add{border:0!important;background:transparent!important;padding:0!important;min-height:0!important;font:inherit;color:var(--gold)!important;text-decoration:underline;text-underline-offset:2px;cursor:grab}.quick-stats-preview .dragging{opacity:.4}.quick-stats-preview .drop-before>*{box-shadow:inset 0 2px var(--accent)}.quick-stats-preview .drop-after>*{box-shadow:inset 0 -2px var(--accent)}.quick-stats-preview .column-before{box-shadow:inset 2px 0 var(--accent)}.quick-stats-preview .column-after{box-shadow:inset -2px 0 var(--accent)}.quick-stats-preview .column-dragging{background:color-mix(in srgb,var(--accent) 12%,transparent)}.quick-stats-preview.drop-target,.quick-stats-toolbox.drop-target{outline:2px dashed var(--accent);outline-offset:3px}.quick-stats-drag-image{position:fixed!important;left:-10000px!important;top:0!important;max-width:none!important;pointer-events:none}.quick-stats-drag-image table{margin:0}@media(max-width:700px){.quick-stats-toolboxes{grid-template-columns:minmax(0,1fr)}}
    .menu-bar-preferences-actions{display:flex;align-items:center;gap:12px;flex:none}.menu-bar-expand{display:inline-flex;align-items:center;gap:6px;padding:5px 10px;border:1px solid var(--line);color:var(--ink);white-space:nowrap}.menu-bar-expand[hidden]{display:none}.menu-bar-expand:hover{border-color:var(--accent)}.menu-bar-expand:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:3px}
    .menu-bar-expand .app-icon{width:14px;height:14px;flex:none;transition:transform .15s}.menu-bar-expand[aria-expanded="true"] .app-icon{transform:rotate(180deg)}
    .menu-bar-preferences-body{padding:0 20px 4px;border-top:1px solid var(--line)}
    @media(max-width:560px){.layout-preference-card>.optional-behavior{flex-wrap:wrap;gap:12px}.layout-preference-card>.optional-behavior>div:first-child{flex-basis:100%}.menu-bar-preferences-actions,.layout-preference-card>.optional-behavior>.menu-bar-expand{margin-left:auto}}
    @media(prefers-reduced-motion:reduce){.menu-bar-expand .app-icon{transition:none}}
    /* The menu mockups follow macOS appearance, independently of the app theme. */
    .menu-bar-editor,.menu-bar-tool-menu{color-scheme:light dark;--mac-menu-bg:#f5f5f5;--mac-menu-ink:#1d1d1f;--mac-menu-muted:#6e6e73;--mac-menu-line:#d4d4d4;--mac-menu-border:#b8b8b8;--mac-menu-selection:#006aff;--mac-menu-highlight:#fff9;--mac-menu-font:-apple-system,BlinkMacSystemFont,"Helvetica Neue",sans-serif}
    @media(prefers-color-scheme:dark){.menu-bar-editor,.menu-bar-tool-menu{--mac-menu-bg:#2d2d2d;--mac-menu-ink:#f5f5f5;--mac-menu-muted:#aaa;--mac-menu-line:#494949;--mac-menu-border:#626262;--mac-menu-selection:#0a64d8;--mac-menu-highlight:#ffffff14}}
    .menu-bar-editor{display:grid;grid-template-columns:minmax(0,360px) minmax(0,360px);gap:32px;margin:24px 0}.menu-bar-editor h3{margin:0 0 12px;font-size:var(--fs-base)}.menu-bar-desktop{max-width:360px}
    .menu-bar-preview,.menu-bar-toolbox,.menu-bar-tool-menu{list-style:none;padding:5px;margin:0;border:1px solid var(--mac-menu-border);border-radius:6px;background:var(--mac-menu-bg);color:var(--mac-menu-ink);box-shadow:0 12px 30px #0003,0 2px 5px #0002,inset 0 1px 0 var(--mac-menu-highlight);font:13px/18px var(--mac-menu-font);letter-spacing:normal}
    .menu-bar-tool{position:relative;min-width:0;cursor:grab}.menu-bar-entry{display:grid;grid-template-columns:minmax(0,1fr) 22px;align-items:center;gap:6px;border-radius:4px}.menu-bar-face{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:baseline;gap:2px 12px;padding:2px 0 2px 16px;min-width:0}.menu-bar-label{overflow-wrap:anywhere}.menu-bar-value{margin-left:auto;font-variant-numeric:tabular-nums}.menu-bar-detail{flex-basis:100%;color:var(--mac-menu-muted);font-size:11px;overflow-wrap:anywhere}.menu-bar-heading{padding:3px 16px;color:var(--mac-menu-muted);font-size:11px;font-weight:600}.menu-bar-section{margin-top:5px;padding-top:5px;border-top:1px solid var(--mac-menu-line)}
    .menu-bar-tool .menu-bar-handle{display:grid;place-items:center;min-height:22px;width:22px;padding:2px;border:0;border-radius:4px;background:transparent;color:var(--mac-menu-muted);cursor:grab;opacity:.55}.menu-bar-tool:hover .menu-bar-entry,.menu-bar-tool:focus-within .menu-bar-entry{background:var(--mac-menu-selection);color:#fff}.menu-bar-tool:is(:hover,:focus-within) :is(.menu-bar-handle,.menu-bar-detail){color:inherit;opacity:1}.menu-bar-handle:focus-visible,.menu-bar-add:focus-visible{outline:2px solid var(--mac-menu-selection);outline-offset:2px}
    .menu-bar-toolbox{min-height:80px;margin-top:12px}.menu-bar-toolbox-section>p{margin:0}.menu-bar-add{display:flex;align-items:center;justify-content:space-between;gap:12px;width:100%;min-height:22px;margin:0;padding:2px 5px 2px 16px;border:0;border-radius:4px;background:transparent;color:inherit;font:inherit;text-align:left;white-space:normal;cursor:grab}.menu-bar-add:hover,.menu-bar-add:focus-visible{background:var(--mac-menu-selection);color:#fff}.menu-bar-add .app-icon{flex:none;width:12px;height:12px;color:var(--mac-menu-muted)}.menu-bar-add:is(:hover,:focus-visible) .app-icon{color:inherit}.menu-bar-toolbox>.muted{padding:6px 16px;color:var(--mac-menu-muted);font:inherit}.menu-bar-preview-note{font-size:var(--fs-sm);margin:10px 0 0}
    .menu-bar-separator-line{display:block;flex:1;min-width:24px;border-top:1px solid var(--mac-menu-line)}.menu-bar-separator .menu-bar-handle{min-height:14px;height:14px}.menu-bar-separator .menu-bar-handle svg{width:12px;height:12px}.menu-bar-add:is(:hover,:focus-visible) .menu-bar-separator-line{border-color:currentColor}.menu-bar-separator .menu-bar-face{align-self:stretch;align-items:center}.menu-bar-separator:is(:hover,:focus-within) .menu-bar-separator-line{border-color:currentColor}
    .menu-bar-tool.dragging,.menu-bar-available.dragging{opacity:.35}.menu-bar-tool.drop-before{box-shadow:0 -2px var(--mac-menu-selection)}.menu-bar-tool.drop-after{box-shadow:0 2px var(--mac-menu-selection)}:is(.menu-bar-preview,.menu-bar-toolbox).drop-target{outline:2px solid var(--mac-menu-selection);outline-offset:2px}.menu-bar-tool-menu:popover-open{display:flex;flex-direction:column;gap:0;width:170px;max-width:calc(100vw - 28px)}.menu-bar-tool-menu button{display:block;min-height:22px;padding:2px 12px;border:0;border-radius:4px;background:transparent;color:inherit;font:inherit;text-align:left}.menu-bar-tool-menu button:is(:hover,:focus-visible):not(:disabled){background:var(--mac-menu-selection);color:#fff;outline:0}
    @media(max-width:900px){.menu-bar-editor{grid-template-columns:minmax(0,1fr);gap:24px}.menu-bar-toolbox{max-width:360px}}


  </style>
  <style>
    /* Evidence first: aligned operations, prose, and one disclosure to the details. */
    .conversation-panel{padding:0;border-radius:4px;box-shadow:none}
    .conversation-head{padding:18px 22px;background:var(--panel)}
    .conversation-title-row h2{margin:0;font-size:var(--fs-2xl)}.provider-mark{display:none}
    .conversation-head .chips{margin-top:7px;gap:14px}.conversation-head .chip{padding:0;border:0;color:var(--muted)}
    .conversation-toolbar{margin-top:12px}.conversation-toolbar input{padding:4px 10px}
    .conversation-toolbar button{font-size:var(--fs-sm);white-space:nowrap}
    .source-details{font-size:var(--fs-xs)}
    .turn-list{padding:0 22px;background:var(--panel)}
    .turn{margin:0;border:0;border-top:1px solid var(--line);border-radius:0;background:none}
    .turn>summary{padding:13px 0;gap:9px}.turn>summary::before{font-size:var(--fs-xl);color:var(--muted)}
    .turn-number .relative-age{white-space:nowrap}.turn-number{font-size:var(--fs-xs);color:var(--muted);font-variant-numeric:tabular-nums}
    .turn-summary-prompt{font-weight:600;font-size:var(--fs-base);white-space:normal;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
    .turn-summary-outcome{color:var(--muted);font-size:var(--fs-sm);white-space:normal;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
    .turn-summary-row{display:flex;align-items:flex-start;gap:6px;min-width:0;transition:opacity .16s}.turn-summary-row>.turn-summary-prompt,.turn-summary-row>.turn-summary-outcome{flex:1;min-width:0}.turn-summary-expand{flex:0 0 auto;padding:0 3px;color:var(--accent);font-size:var(--fs-2xs);line-height:1.6}.turn-summary-prompt.summary-expanded,.turn-summary-outcome.summary-expanded{display:block;-webkit-line-clamp:unset;overflow:visible}.turn[open]>summary .turn-summary-row{opacity:.45}
    .turn-summary-stats{display:flex;flex-direction:column;align-items:flex-start;gap:2px}.mini-count{color:var(--muted);font-size:var(--fs-xs);white-space:nowrap}
    .tool-summary-trigger{cursor:pointer}.tool-summary-trigger:hover,.tool-summary-trigger[aria-expanded="true"]{color:var(--accent);border-color:var(--accent)}.tool-summary{margin-top:12px;padding:12px;border:1px solid var(--line);border-radius:4px;background:var(--bg)}.tool-summary[hidden]{display:none}.tool-summary h3{margin:0 0 8px;font:600 var(--fs-md) var(--sans)}.tool-summary-table{width:100%;border-collapse:collapse;font-size:var(--fs-sm)}.tool-summary-table th,.tool-summary-table td{padding:6px 8px;border-bottom:1px solid var(--line);text-align:left}.tool-summary-table th{color:var(--muted);font-weight:600}.tool-summary-table th:nth-child(n+2),.tool-summary-table td:nth-child(n+2){text-align:right;font-variant-numeric:tabular-nums}.tool-summary-table tbody tr:last-child td{border-bottom:0}
    .turn-body{padding:0 0 18px}
    .timeline-entry{display:grid;grid-template-columns:45px minmax(0,1fr);gap:12px;margin:2px 0;align-items:start}
    .timeline-time{height:30px;display:flex;flex-wrap:wrap;align-items:center;align-content:center;justify-content:flex-end;color:var(--muted);font:var(--fs-2xs)/1 ui-monospace,SFMono-Regular,monospace;text-align:right;white-space:nowrap}
    .timeline-content{min-width:0}.speaker-row,.speaker-row.human{display:block;margin:9px 0 13px;padding:0;border:0;border-radius:0;background:none}
    .speaker-row.human{border-left:2px solid var(--gold);padding-left:12px}
    .speaker-row::before,.speaker-row::after,.avatar{display:none}
    .message-prose{white-space:pre-wrap;overflow-wrap:anywhere;font:var(--fs-base)/1.65 var(--sans)}
    .prose-truncated{display:-webkit-box;-webkit-line-clamp:6;-webkit-box-orient:vertical;overflow:hidden}
    .message-toggle{display:block;padding:3px 0;color:var(--accent);font-size:var(--fs-xs)}.message-toggle[hidden]{display:none}
    .sequence-more{display:block;margin:4px 0 6px 58px;padding:3px 0;color:var(--accent);font-size:var(--fs-xs)}.clipped-note{display:flex;gap:8px;align-items:baseline;margin:2px 0 4px;color:var(--muted);font-size:var(--fs-xs)}.clipped-note button{color:var(--accent);font-size:var(--fs-xs);padding:0}.conversation-loading{color:var(--muted)}
    .tool-event,.tool-event.error,.subagent-run{margin:0;border:0;border-radius:0;background:none}
    .tool-event>summary,.subagent-run>summary{display:flex;align-items:center;gap:10px;min-height:30px;padding:5px 0;list-style:none;font-size:var(--fs-sm);color:var(--ink)}
    .tool-event>summary::-webkit-details-marker,.subagent-run>summary::-webkit-details-marker{display:none}
    .tool-event>summary::before,.subagent-run>summary::before{content:'›';flex:0 0 8px;color:var(--muted)}
    .tool-event[open]>summary::before,.subagent-run[open]>summary::before{content:'⌄'}
    .tool-event>summary:hover,.subagent-run>summary:hover{background:color-mix(in srgb,var(--line) 20%,transparent)}
    .activity-title{flex:0 0 72px;font-weight:500;color:var(--muted)}
    .activity-target{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--ink);flex:1}
    .target-filename{flex-shrink:0}.target-directory{color:var(--muted);margin-left:9px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.activity-target:has(.target-filename){display:flex;align-items:baseline}.command-target{font:var(--fs-sm) ui-monospace,SFMono-Regular,monospace}
    .error-preview{flex-basis:100%;margin-left:90px;font-size:var(--fs-xs);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.tool-event.error>summary{flex-wrap:wrap}.activity-status{margin-left:auto;font-size:var(--fs-2xs);white-space:nowrap;color:var(--muted)}
    .tool-event.error>summary .activity-title,.badtext{color:var(--bad)}
    .event-body{margin:0 0 10px 18px;padding:4px 12px 8px;border-left:1px solid var(--line)}
    .mcp-target{overflow-wrap:anywhere;min-width:0}.tool-output.mcp-prose{font-family:inherit}.mcp-table{overflow:auto;max-height:420px}.mcp-presentation{min-width:0}.tool-inputs{display:grid;grid-template-columns:90px minmax(0,1fr);gap:4px 10px;margin:4px 0;font:var(--fs-sm)/1.5 ui-monospace,SFMono-Regular,monospace}
    .tool-inputs dt{color:var(--muted)}.tool-inputs dd{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;max-height:300px;overflow:auto}
    .tool-detail-section{margin-top:9px}.tool-detail-label{display:block;margin-bottom:4px;font-size:var(--fs-xs);color:var(--muted);overflow-wrap:anywhere}
    .tool-output,.raw-json{margin:5px 0;max-height:420px;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font:var(--fs-sm)/1.55 ui-monospace,SFMono-Regular,monospace}
    .raw-event{margin-top:6px;color:var(--muted);font-size:var(--fs-2xs)}.raw-event>summary{cursor:pointer}.raw-json{padding:8px;background:var(--bg)}
    .change-chips{display:flex;flex:1;min-width:0;flex-wrap:wrap;gap:4px}
    .change-chip{display:inline-flex;min-width:0;align-items:baseline;gap:6px;border:1px solid color-mix(in srgb,var(--line) 70%,transparent);border-radius:2px;padding:0 5px;font:var(--fs-xs)/1.7 ui-monospace,SFMono-Regular,monospace}
    .change-path{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:390px;text-align:left}
    .change-directory{color:var(--muted);max-width:120px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.change-chip{max-width:100%}.delta-add{color:var(--sea)}.delta-remove{color:var(--bad)}.delta-unknown{color:var(--muted)}
    .diff-view{max-height:460px;overflow:auto;font:var(--fs-sm)/1.6 ui-monospace,SFMono-Regular,monospace}
    .diff-line{display:block;min-height:1.6em;padding:0 7px;white-space:pre}
    .diff-line.add{background:color-mix(in srgb,var(--sea) 10%,transparent);color:var(--sea)}
    .diff-line.remove{background:color-mix(in srgb,var(--bad) 9%,transparent);color:var(--bad)}
    .diff-line.hunk{color:var(--muted)}
    .subagent-run{margin:0;border:0;border-radius:0;background:none}
    .subagent-run>summary{font-weight:400}.subagent-run>summary .activity-title{flex:0 0 auto;min-width:35px}.subagent-summary-stats{display:flex;align-items:center;flex-shrink:0}
    .subagent-body{margin:0 0 4px 20px;padding:2px 8px 4px;border-left:1px solid var(--line)}.subagent-body .timeline-entry{grid-template-columns:38px minmax(0,1fr)}.subagent-assignment{white-space:pre-wrap;overflow-wrap:anywhere;font-size:var(--fs-xs);line-height:1.45}.subagent-details>summary{color:var(--muted)}
    .token-column{display:flex;flex-direction:column;align-items:stretch;flex:0 0 var(--token-column-width,60px);width:var(--token-column-width,60px);min-width:0;gap:3px}.conversation-panel:not(.has-token-usage) .token-column{display:none}
    .token-column>.context-evidence{display:flex;flex-direction:column;align-items:stretch;gap:3px;min-width:0}.token-column .token-badge,.token-column .spend-badge,.token-column .compaction-badge{display:block;box-sizing:border-box;width:100%;margin-left:0;padding:1px 2px;text-align:center;white-space:normal;overflow-wrap:anywhere}
    .token-badge{display:inline-block;flex-shrink:0;font:var(--fs-2xs)/1.5 ui-monospace,SFMono-Regular,monospace;white-space:nowrap;padding:1px 5px;border:1px solid currentColor;border-radius:3px;margin-left:6px;background:var(--panel)}
    .context-badge{color:var(--heat-color,var(--muted))}
    .compaction-badge{font-size:var(--fs-2xs);color:var(--muted);white-space:nowrap;margin-left:5px}.compaction-badge::before{content:'';display:inline-block;width:9px;height:2px;margin:0 4px 2px 0;vertical-align:middle;background:var(--ink)}.token-legend{display:flex;align-items:center;flex-wrap:wrap;gap:10px;margin:8px 0 0;font-size:var(--fs-2xs);color:var(--muted)}.token-legend button{font-size:var(--fs-xs);padding:1px 6px}
    .token-highlight{animation:token-flash 2s ease-out}@keyframes token-flash{from{background:color-mix(in srgb,var(--gold) 25%,transparent)}to{background:transparent}}
    .compaction-badge.manual-compaction::before{background:var(--heat-1)}.compaction-badge.auto-compaction::before{background:var(--heat-3)}
    .export-document{max-height:60vh;overflow:auto;overflow-wrap:anywhere;min-width:0;margin:12px 0}.message-citations{font-size:var(--fs-sm);color:var(--muted);margin:10px 0;overflow-wrap:anywhere}.message-citations ol{padding-left:20px;margin:4px 0}.message-citations li{margin:3px 0}.message-citations blockquote{margin:4px 0}
    .spend-badge{display:inline-block;margin-left:6px;padding:1px 5px;border:1px dashed var(--muted);border-radius:3px;color:var(--muted);font:var(--fs-2xs)/1.5 ui-monospace,monospace;white-space:nowrap}.context-key{color:#43877e}.agent-context-key{color:#b07a18}
    @media(min-width:901px){.conversation-panel{margin-right:65px}}
    summary:focus-visible,button:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}
    @media(min-width:901px){.conversation-panel{margin-right:30px}}
    @media(max-width:760px){.conversation-head{padding:14px}.conversation-toolbar{flex-wrap:wrap}.conversation-toolbar input{flex-basis:100%}.turn-list{padding:0 10px}.turn-summary-stats{display:none}.timeline-entry{grid-template-columns:32px minmax(0,1fr);gap:7px}.activity-title{flex-basis:48px;font-size:var(--fs-xs);overflow-wrap:anywhere}.change-path{max-width:150px}.tool-inputs{grid-template-columns:1fr}.event-body{padding-left:8px}.subagent-body{padding-left:8px}}
  </style>
  <style>
    /* Compact operation groups; chip previews and full action details are independent. */
    .action-row .tool-event>summary::before,.subagent-run>summary::before{display:none}
    .kind-icon{display:inline-block;flex:0 0 15px;width:15px;color:var(--muted);font:var(--fs-base)/1.3 ui-monospace,SFMono-Regular,monospace;text-align:center}
    .action-row .activity-title{flex:0 0 auto;min-width:42px}
    .action-chips{display:flex;flex:1;min-width:0;flex-wrap:wrap;align-items:center;gap:5px}
    .file-chip{display:inline-flex;align-items:center;flex-wrap:wrap;max-width:100%;min-width:0;gap:3px;padding:1px 4px;border:1px solid var(--line);border-radius:3px;font:var(--fs-sm)/1.5 ui-monospace,SFMono-Regular,monospace;background:color-mix(in srgb,var(--bg) 30%,transparent)}
    .file-chip button{padding:0 2px;font:inherit;border-radius:2px;color:inherit}
    .file-chip .file-prefix{color:var(--muted);overflow-wrap:anywhere;text-align:left;max-width:100%}
    .file-prefix[aria-expanded=true]{white-space:normal}
    .file-target{color:var(--ink);overflow-wrap:anywhere;max-width:100%;text-align:left}
    .file-target:hover,.query-chip:hover{text-decoration:underline;text-underline-offset:3px}
    .file-chip .file-copy,.file-chip .file-history-link{font-size:var(--fs-sm);color:var(--muted)}.file-history-link{display:inline-grid;place-items:center}.file-history-link:hover{color:var(--accent)}
    .action-category{font-size:var(--fs-2xs);color:var(--muted);white-space:nowrap}
    .reader-line-tag button.usage-legend-item,.reader-line-tag>button{padding:1px 3px;font:inherit;color:inherit;text-align:left;border:1px solid transparent;border-radius:3px}
    .reader-line-tag>button::after{content:'⌄';margin-left:3px;color:var(--muted)}
    .reader-line-tag button:hover,.reader-line-tag button:focus-visible{border-color:var(--line);color:var(--ink)}
    .reader-line-tag .usage-legend-caret{width:9px;height:9px;margin-left:2px}
    .conversation-panel [data-tag-hidden=true]:not(.filter-exempt){display:none}
    .transcript-tag-key:not([hidden]){display:flex;align-items:center;flex-wrap:wrap;gap:6px}
    .query-chip,.file-target{display:inline-flex;align-items:center;gap:4px}
    .query-chip::after,.file-target::after{content:'›';display:inline-block;flex:none;font-size:14px;line-height:1;transition:transform .16s}
    .query-chip[aria-expanded=true]::after,.file-target[aria-expanded=true]::after{transform:rotate(90deg)}
    .action-web-link{min-width:0;max-width:100%;overflow-wrap:anywhere}
    .action-web-link .external-indicator{display:inline-flex;vertical-align:middle;margin-left:4px}.action-web-link .external-indicator svg{width:1em;height:1em}
    .query-chip{padding:1px 6px;border:1px solid var(--line);border-radius:3px;font-size:var(--fs-sm);color:var(--ink);text-align:left;overflow-wrap:anywhere;max-width:100%}
    .file-chip.failed,.query-chip.failed{border-color:var(--bad)}
    .line-details-hint{margin-left:auto;color:var(--muted);font-size:var(--fs-2xs)}
    .chip-preview{position:relative;margin:5px 0 12px 25px;padding:10px 12px;border-left:2px solid var(--accent);background:color-mix(in srgb,var(--bg) 30%,transparent);min-width:0}
    .chip-preview[hidden]{display:none}.preview-close{float:right;padding:0 0 5px 10px;font-size:var(--fs-xs);color:var(--accent)}
    .action-row>.tool-event>.event-body>.event-body{margin-left:0;border:0;padding-left:0}
    .todo-list{display:grid;gap:5px;padding:3px 0}.todo-item{display:flex;align-items:baseline;gap:9px;font-size:var(--fs-md)}.todo-state{color:var(--sea)}.todo-status{margin-left:auto;font-size:var(--fs-2xs);color:var(--muted)}.todo-item.completed>span:nth-child(2){color:var(--muted)}.todo-item.deleted>span:nth-child(2){text-decoration:line-through}
    .speaker-row.agent{padding:13px 16px 13px 29px;border-left:2px solid var(--sea);background:color-mix(in srgb,var(--sea) 4%,var(--panel));margin:14px 0 16px}
    .speaker-row .message-card{position:relative}.message-prose{font-size:var(--fs-base);line-height:1.7;padding-right:25px}
    .speaker-row.agent .prose-truncated{-webkit-line-clamp:10}
    .message-kind{position:absolute;left:-22px;top:0;height:1.45em;display:grid;place-items:center;line-height:1;color:var(--sea)}.speaker-row.human{padding-left:29px}.human .message-kind{color:var(--gold)}
    .message-raw-button{position:absolute;top:0;right:-5px;padding:1px 4px;font:var(--fs-xs) ui-monospace,SFMono-Regular,monospace;color:var(--muted)}
    .message-card>.raw-event>summary{display:none}.message-card>.raw-event[hidden]{display:none}
    @media(max-width:760px){.action-row .tool-event>summary{gap:5px;flex-wrap:wrap}.action-chips{flex-basis:65%}.line-details-hint{font-size:var(--fs-3xs)}.file-chip{font-size:var(--fs-xs)}.speaker-row.agent{padding-right:8px}.message-prose{font-size:var(--fs-base)}.chip-preview{margin-left:0}.subagent-body{margin-left:6px;padding-left:5px}.subagent-body .timeline-entry{grid-template-columns:24px minmax(0,1fr);gap:4px}.subagent-body .speaker-row.agent{padding-left:19px}}
  </style>
  <style>
    .work-section{min-width:0}.work-section-summary{color:var(--muted);font-size:var(--fs-sm);cursor:pointer;width:max-content}.work-section-summary:hover{color:var(--ink)}.peer-strip{margin:6px 0 2px}.peer-chips{display:flex;flex-wrap:wrap;align-items:center;gap:6px;margin:6px 0 2px;min-width:0}.peer-reasons{margin-top:4px}.peer-chip{color:inherit;text-decoration:none;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.peer-chip:hover{border-color:var(--accent)}.peer-chip.uncertain{border-style:dashed;color:var(--muted)}.peer-reasons{font-size:var(--fs-sm)}.peer-reasons summary{color:var(--accent);cursor:pointer;width:max-content}.peer-reasons li.uncertain{color:var(--muted)}.peer-reader-note{margin:0 0 6px;color:var(--muted);font-size:var(--fs-sm)}.handoff-markers{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px;min-width:0}.tool-event summary .handoff-markers{display:inline-flex;margin:0 0 0 8px}.handoff-marker{border:1px solid var(--line);border-radius:20px;padding:1px 8px;font-size:var(--fs-xs);color:var(--accent);text-decoration:none;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.handoff-marker.uncertain{border-style:dashed;color:var(--muted)}
    .work-heading{position:static;z-index:auto;background:none;backdrop-filter:none;border:0;box-shadow:none;margin-bottom:10px}.work-heading h1{margin:0 0 3px;font-size:var(--fs-2xl)}.work-activity{margin-top:7px}.work-controls{display:flex;align-items:start;flex-wrap:wrap;gap:8px;margin-top:6px;font-size:var(--fs-sm)}.work-repository-link{padding:0;color:var(--accent);font-size:inherit;text-decoration:underline;text-underline-offset:2px}
.work-disclosure{position:relative}.work-disclosure>summary{cursor:pointer;color:var(--muted);padding:2px 5px}.work-disclosure[open]{flex-basis:100%;padding:6px 9px;border:1px solid var(--line);border-radius:4px}.work-disclosure .metric{padding:4px;font-size:var(--fs-sm)}
.conversation-choice{display:flex;flex-wrap:wrap;align-items:center;gap:10px;padding:10px 14px;margin:-10px -14px 10px;background:color-mix(in srgb,var(--accent) 9%,var(--panel));border-bottom:1px solid var(--line)}.conversation-choice label{font-weight:700;color:var(--ink);white-space:nowrap}.conversation-selector{flex:1;min-width:min(320px,100%);max-width:100%;font:inherit;padding:7px 30px 7px 10px;border:1px solid var(--accent);border-radius:5px;background:var(--bg);color:var(--ink)}.file-index-row{display:flex;flex-wrap:wrap;align-items:baseline;gap:8px;padding:2px 0}.file-index-link{font:var(--fs-xs) ui-monospace,monospace;text-align:left;overflow-wrap:anywhere;min-width:0}.file-index-link:disabled{opacity:.65;cursor:default}
.archive-details{margin-top:12px;font-size:var(--fs-sm);color:var(--muted)}.archive-details[hidden],.workspace-inbox.merged .archive-details[hidden]{display:none}.archive-details>summary{cursor:pointer}.archive-body{padding-top:8px}.archive-body .panel{padding:10px}.archive-body h2{font-size:var(--fs-base)}.coverage-warning{font-size:var(--fs-sm);padding:5px 9px;margin:5px 0;border-left:2px solid #bd8627}.conversation-host:empty{display:none}
/* Dense reading layout: structure is carried by alignment, not empty space. */
    .conversation-head{padding:10px 14px}.conversation-title-row h2{font-size:var(--fs-xl)}
    .conversation-toolbar{margin-top:7px;gap:6px}.conversation-toolbar input{padding:3px 8px;font-size:var(--fs-sm)}.conversation-toolbar button{padding:3px 7px;font-size:var(--fs-xs)}
    .conversation-head .chips{margin-top:4px;font-size:var(--fs-xs)}.conversation-head .chip{font-size:var(--fs-xs)}
    .conversation-title-row{align-items:flex-start}.conversation-title-row>.conversation-title-content{min-width:0}.conversation-title-content>.chips{display:flex;flex-wrap:wrap;align-items:center;gap:4px 14px}
    .transcript-filter-label{color:var(--muted);font-weight:600}.transcript-filter-count{color:var(--muted);font-variant-numeric:tabular-nums}
    .conversation-panel.hide-prompts [data-show=prompts]:not(.filter-exempt),.conversation-panel.hide-response [data-show=response]:not(.filter-exempt),.conversation-panel.hide-replies [data-show=replies]:not(.filter-exempt),.conversation-panel.hide-thinking [data-show=thinking]:not(.filter-exempt),.conversation-panel.hide-edits [data-show=edits]:not(.filter-exempt),.conversation-panel.hide-commands [data-show=commands]:not(.filter-exempt),.conversation-panel.hide-reads [data-show=reads]:not(.filter-exempt),.conversation-panel.hide-tools [data-show=tools]:not(.filter-exempt),.conversation-panel.hide-agents [data-show=agents]:not(.filter-exempt),.conversation-panel.hide-events [data-show=events]:not(.filter-exempt),.conversation-panel.hide-usage [data-show=usage]:not(.filter-exempt){display:none}
    .conversation-title-row>.conversation-toolbar{display:flex;flex:0 0 auto;flex-direction:column;align-items:stretch;min-width:135px;margin:0}.conversation-toolbar button{text-align:left}.conversation-toolbar input:not([hidden]){max-width:170px}
    .conversation-head .source-details{margin:0}.conversation-head .source-details[open]{flex-basis:100%}.conversation-head .source-details dl{margin:5px 0 0}.conversation-head .token-legend{display:inline-flex;margin:0;font-size:var(--fs-xs)}
    .compaction-badge{position:relative;display:inline-flex!important;align-items:center;width:auto!important;margin-left:4px!important;padding:2px!important;cursor:default}.compaction-badge::before{width:12px;height:4px;margin:0}.compaction-badge:hover::after{content:attr(data-label);position:absolute;z-index:10;left:100%;top:50%;transform:translate(6px,-50%);padding:4px 7px;border:1px solid var(--line);border-radius:4px;background:var(--panel);color:var(--ink);font-size:var(--fs-xs);white-space:nowrap;box-shadow:0 4px 12px #0003}
    @media(max-width:760px){.conversation-title-row{flex-wrap:wrap}.conversation-title-row>.conversation-toolbar{flex-direction:row;flex-wrap:wrap;width:100%}.conversation-toolbar button{text-align:center}}
    .turn-list{padding:0 14px}.turn>summary{padding:5px 0}.turn-body{padding:0 0 5px}
    .timeline-entry{margin:0;gap:8px;grid-template-columns:38px minmax(0,1fr)}.timeline-time{height:23px;padding:0;font-size:var(--fs-3xs)}
    .speaker-row,.speaker-row.human{margin:2px 0;padding-top:2px;padding-bottom:2px}
    .speaker-row.agent{margin:2px 0;padding:5px 9px 5px 24px}
    .speaker-row.human{padding-left:24px}.message-kind{left:-19px;top:0}
    .message-prose,.todo-item{font-size:var(--fs-sm);line-height:1.45}
    .action-row .tool-event>summary,.subagent-run>summary{min-height:23px;padding:1px 0;gap:6px;font-size:var(--fs-xs)}
    .action-row .activity-title{min-width:35px;font-size:var(--fs-xs)}.kind-icon{font-size:var(--fs-sm);flex-basis:14px;width:14px}
    .action-chips{gap:3px}.file-chip{font-size:var(--fs-2xs);line-height:1.45;padding:0 3px;gap:2px}.file-chip .file-copy,.file-chip .file-history-link{font-size:var(--fs-2xs)}
    .query-chip{font-size:var(--fs-2xs);padding:0 4px}.command-target{font-size:var(--fs-xs)}
    .event-body{margin-bottom:4px;padding:2px 8px 4px}.tool-detail-section{margin-top:4px}.tool-detail-label{font-size:var(--fs-2xs);margin-bottom:2px}
    .tool-inputs{font-size:var(--fs-xs);line-height:1.4;margin:2px 0;gap:2px 8px}.tool-output,.raw-json,.diff-view{font-size:var(--fs-xs);line-height:1.4}
    .chip-preview{margin:2px 0 5px 20px;padding:5px 8px}.todo-list{gap:2px}.message-toggle{font-size:var(--fs-2xs);padding:1px 0}
    .rendered-markdown{white-space:normal}.rendered-markdown.prose-truncated{display:block;max-height:175px;overflow:hidden}
    .markdown-content>:first-child{margin-top:0}.markdown-content>:last-child{margin-bottom:0}
    .xml-chip-row{display:flex;flex-wrap:wrap;align-items:center;gap:3px;margin:3px 0;white-space:normal}.xml-chip-row.inline:not(.chip-open){display:inline-flex;margin:0 5px 0 0;vertical-align:1px}.xml-chip[open]{flex-basis:100%}
    .xml-chip>summary{display:inline-flex;align-items:center;height:17px;padding:0 10px;clip-path:polygon(6px 0,calc(100% - 6px) 0,100% 50%,calc(100% - 6px) 100%,6px 100%,0 50%);background:color-mix(in srgb,var(--xml-chip-tint) var(--xml-chip-mix),var(--panel));color:var(--xml-chip-ink);font:var(--fs-xs)/1 ui-monospace,SFMono-Regular,monospace;cursor:pointer;list-style:none;transition:background .12s,color .12s}.xml-chip>summary::-webkit-details-marker{display:none}.xml-chip>summary:hover,.xml-chip>summary:focus-visible{background:color-mix(in srgb,var(--xml-chip-tint) var(--xml-chip-hover),var(--panel));color:var(--ink);outline:none}.speaker-row{--xml-chip-tint:var(--accent);--xml-chip-mix:30%;--xml-chip-hover:44%;--xml-chip-ink:var(--accent-deep)}.speaker-row.human{--xml-chip-tint:var(--gold);--xml-chip-mix:17%;--xml-chip-hover:30%;--xml-chip-ink:var(--muted)}.xml-chip[open]>summary{display:none}
    .xml-chip-row.inline:not(.chip-open)+.after-xml-chips,.xml-chip-row.inline:not(.chip-open)+.after-xml-chips>p:first-child{display:inline}
    .xml-chip-body{padding:6px 10px 6px 8px;max-height:420px;overflow:auto;border-radius:3px;background:color-mix(in srgb,var(--bg) 65%,var(--panel));overflow-wrap:anywhere;font:var(--fs-xs)/1.5 ui-monospace,SFMono-Regular,monospace}
    .xml-line{color:var(--accent-deep)}.xml-text{white-space:pre-wrap;color:var(--ink)}.xml-children{margin-left:4px;padding-left:12px;border-left:1px solid color-mix(in srgb,var(--line) 70%,transparent)}
    .xml-node>summary{position:relative;cursor:pointer;list-style:none;border-radius:2px}.xml-node>summary::-webkit-details-marker{display:none}.xml-node>summary:hover{background:color-mix(in srgb,var(--xml-chip-tint) 15%,transparent)}.xml-node>summary::before{content:'▾';position:absolute;left:-11px;color:var(--muted);font-size:var(--fs-3xs);line-height:16px}.xml-node:not([open])>summary::before{content:'▸'}.xml-node:not([open])>summary::after{content:attr(data-close);color:var(--muted)}.xml-chip-body>.xml-node{margin-left:10px}

    .markdown-link[href]{text-decoration:none;border-bottom:1px solid currentColor;-webkit-box-decoration-break:clone;box-decoration-break:clone}
    .markdown-link-icon{display:inline-flex;width:1em;height:1em;vertical-align:-.12em;margin-right:.25em;align-items:center;justify-content:center}
    .markdown-link-icon svg.app-icon,.markdown-link-icon img{width:100%;height:100%;object-fit:contain}
    .markdown-link-icon img{border-radius:2px;padding:1px;box-sizing:border-box}
    .markdown-link[data-link-status="unavailable"] .markdown-link-icon{color:var(--bad,#e66b67)}
    .markdown-link[aria-disabled="true"]{cursor:default;text-decoration-style:dotted}
    .markdown-link[data-link-status="checking"] .markdown-link-icon,.markdown-link[data-link-status="unknown"] .markdown-link-icon{color:var(--muted)}
    /* Reader highlights (transcriptHighlightControl): each kind of mark shows only while its show-<id> class is on the panel. */
    mark.human-written{background:none;color:inherit;padding:0}
    .show-writing .human-written{background:color-mix(in srgb,var(--gold) 19%,transparent);color:inherit;border-radius:2px;padding:1px 0;-webkit-box-decoration-break:clone;box-decoration-break:clone}
    .transcript-highlight-note{min-width:0;color:var(--muted)}.label-legend-title{color:var(--muted);font-weight:600}
    /* Local labels tint the whole message card; agent events keep their sentence marks. */
    :root{--label-report:#2a78d6;--label-control:#eb6834;--label-deliberation:#1baf7a;--label-input:#eda100;--label-feedback:#e87ba4;--label-request:#008300;--label-frame:#6250d6;--label-difficulty:#e34948}
    @media(prefers-color-scheme:dark){:root:not([data-theme]){--label-report:#3987e5;--label-control:#d95926;--label-deliberation:#199e70;--label-input:#c98500;--label-feedback:#d55181;--label-request:#008300;--label-frame:#9085e9;--label-difficulty:#e66767}}
    :root[data-theme="dark"]{--label-report:#3987e5;--label-control:#d95926;--label-deliberation:#199e70;--label-input:#c98500;--label-feedback:#d55181;--label-request:#008300;--label-frame:#9085e9;--label-difficulty:#e66767}
    [data-label-family]{--label-hue:var(--muted)}[data-label-family="C.report"]{--label-hue:var(--label-report)}[data-label-family="P.control"]{--label-hue:var(--label-control)}[data-label-family="C.deliberation"]{--label-hue:var(--label-deliberation)}[data-label-family="C.input"]{--label-hue:var(--label-input)}[data-label-family="C.feedback"]{--label-hue:var(--label-feedback)}[data-label-family="P.request"]{--label-hue:var(--label-request)}[data-label-family="P.frame"]{--label-hue:var(--label-frame)}[data-label-family="C.difficulty"]{--label-hue:var(--label-difficulty)}
    .show-labels :is(.speaker-row,.thinking-card)[data-label-family]:not(.reader-label-hidden){background:color-mix(in srgb,var(--label-hue) 13%,var(--panel));border-left:2px solid color-mix(in srgb,var(--label-hue) 65%,var(--line))}
    .show-labels .speaker-row.human[data-label-family]:not(.reader-label-hidden){padding:8px 9px 8px 24px}
    .thinking-card{position:relative;padding:8px 12px;margin:4px 0 8px;border-left:2px solid var(--line);background:color-mix(in srgb,var(--bg) 30%,var(--panel))}
    .label-legend-group{display:inline-flex;flex-wrap:wrap;align-items:center;gap:4px 10px;padding:5px 7px;border:1px solid color-mix(in srgb,var(--ink) 14%,transparent);border-radius:5px;max-width:100%}.label-legend-group>strong{font-size:inherit;font-weight:600;color:var(--ink)}
    .label-legend{display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px;max-width:100%}.label-legend[hidden]{display:none}.label-legend-item{display:inline-flex;align-items:center;gap:5px;white-space:normal;min-width:0}.label-legend-item i,.label-tip-row i{display:inline-block;flex:none;width:11px;height:11px;border-radius:2px;background:color-mix(in srgb,var(--label-hue) 24%,transparent);border:1px solid color-mix(in srgb,var(--label-hue) 60%,transparent)}.label-legend-note{color:var(--muted)}
    .label-tip{position:fixed;z-index:60;max-width:340px;padding:8px 10px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);box-shadow:0 8px 24px #0003;font-size:var(--fs-sm);line-height:1.35;overflow-wrap:anywhere;pointer-events:none}.label-tip[hidden]{display:none}.label-tip-segment{margin-bottom:6px;color:var(--muted);font-size:var(--fs-xs)}.label-tip-row{display:grid;grid-template-columns:11px 1fr auto;column-gap:7px;align-items:center;margin-top:4px}.label-tip-row strong{font-weight:600}.label-tip-row span{color:var(--muted);font-variant-numeric:tabular-nums}.label-tip-row p{grid-column:2/4;margin:1px 0 0;color:var(--muted);font-size:var(--fs-xs)}
    /* Agent events (agentEventsAttacher): an event's sentence in its kind group's hue, what it was about and what the agent did underlined, shown with the Agent events highlight. */
    /* Agent events take the primaries, so they read apart from the secondary and tertiary tool-action hues (web/src/delegation.css) and the message modes: friction red, learning gold (a deep ochre, kept apart from the attention and Find yellows --attention-*), second look green, choices blue. Deep in light mode, pale in dark; each group also has its own shape (web/src/event-link-layout.ts). web/tests/unit/delegation/timeline-palette.test.mjs checks the separations. Links between an event and its target are ink (--event-link); --event-on is text on an event colour. */
    :root{--event-friction:#b0102c;--event-learning:#806400;--event-second-look:#2fa24f;--event-choices:#1849b8;--event-link:#2c2821;--event-on:#fffaf0}
    :root[data-theme="dark"]{--event-friction:#ff3b3b;--event-learning:#a88a2a;--event-second-look:#5edc82;--event-choices:#7f9dff;--event-link:#f3ebdc;--event-on:#10241b}
    @media(prefers-color-scheme:dark){:root:not([data-theme]){--event-friction:#ff3b3b;--event-learning:#a88a2a;--event-second-look:#5edc82;--event-choices:#7f9dff;--event-link:#f3ebdc;--event-on:#10241b}}
    .event-friction{--event-hue:var(--event-friction)}.event-learning{--event-hue:var(--event-learning)}.event-second-look{--event-hue:var(--event-second-look)}.event-choices{--event-hue:var(--event-choices)}
    mark.agent-event,mark.event-context,mark.event-target{background:none;color:inherit;border:0;padding:0}
    /* Two-sided events (bindEventLinks): the target underlined in link ink, both ends numbered inline (the badge names the other end), a gutter arrow from the event back to its target on hover or selection. */
    .show-events mark.event-target:not(.reader-event-hidden){text-decoration:underline 2px dashed var(--event-link);text-underline-offset:3px;background:color-mix(in srgb,var(--event-link) 7%,transparent);border-radius:2px;cursor:pointer;-webkit-box-decoration-break:clone;box-decoration-break:clone}
    .show-events [data-agent-events] .agent-event:not(.reader-event-hidden).is-linked{cursor:pointer;text-decoration:underline 1.5px solid color-mix(in srgb,var(--event-hue) 70%,transparent);text-underline-offset:3px}
    .event-link-n,.event-link-gutter{display:none}
    .event-legend-item.event-target-legend i{background:color-mix(in srgb,var(--event-link) 7%,transparent);border:0;border-bottom:2px dashed var(--event-link);border-radius:0}
    .show-events .event-link-n{display:inline-block;min-width:15px;height:15px;padding:0 3px;margin:0 1px;border:0;border-radius:8px;font:700 9px/15px var(--sans);text-align:center;vertical-align:super;cursor:pointer;background:var(--event-hue);color:var(--event-on);font-style:normal}
    .show-events .event-link-n.is-target{background:var(--panel);color:var(--event-link);box-shadow:inset 0 0 0 1.5px var(--event-link)}
    .show-events .event-link-n:focus-visible{outline:2px solid var(--focus);outline-offset:1px}
    .conversation-panel{position:relative}
    .show-events .event-link-gutter{display:block;position:absolute;left:0;top:0;pointer-events:none;overflow:visible;z-index:2}
    .event-link-gutter path{fill:none;stroke:var(--event-link);stroke-width:1.6;stroke-linecap:round;opacity:.75}.event-link-gutter path.is-weak{stroke-dasharray:4 3}.event-link-gutter path.is-selected{stroke-width:2;opacity:.95}.event-link-gutter marker path{fill:var(--event-link);stroke:none;opacity:1}
    .show-events.has-link-focus mark.is-linked:not(.reader-event-hidden):not(.link-focus),.show-events.has-link-focus mark.event-target:not(.reader-event-hidden):not(.link-focus){opacity:.55}
    .show-events mark.link-focus:not(.reader-event-hidden),.show-events mark.link-selected:not(.reader-event-hidden){outline:1px solid color-mix(in srgb,var(--event-link) 40%,transparent);outline-offset:1px;border-radius:2px}
    .show-events .event-link-n.link-focus,.show-events .event-link-n.link-selected{box-shadow:0 0 0 2px var(--panel),0 0 0 3px var(--event-link)}
    .show-events mark.link-reveal:not(.reader-event-hidden){outline-width:3px;outline-offset:2px;transition:outline-width .6s,outline-offset .6s}
    .show-events [data-agent-events] .agent-event:not(.reader-event-hidden){background:color-mix(in srgb,var(--event-hue) 24%,transparent);border-radius:2px;padding:1px 0;-webkit-box-decoration-break:clone;box-decoration-break:clone;cursor:text}
    .show-events [data-agent-events] .agent-event:not(.reader-event-hidden):hover{background:color-mix(in srgb,var(--event-hue) 40%,transparent)}
    .show-events [data-agent-events] .event-context:not(.reader-event-hidden){text-decoration:underline dotted color-mix(in srgb,var(--muted) 80%,transparent);text-underline-offset:3px;cursor:text}
    .event-badge{display:none}.show-events .event-badge{display:inline-flex;align-items:center;gap:5px;margin-left:8px;color:var(--muted);font-size:var(--fs-xs)}.event-badge i{display:inline-block;width:9px;height:9px;border-radius:2px;background:color-mix(in srgb,var(--event-hue) 40%,transparent)}
    .event-legend-item i,.event-tip-row i{display:inline-block;flex:none;width:11px;height:11px;border-radius:2px;background:color-mix(in srgb,var(--event-hue) 24%,transparent);border:1px solid color-mix(in srgb,var(--event-hue) 60%,transparent)}
    .event-tip-row{display:grid;grid-template-columns:11px 1fr auto;column-gap:7px;align-items:center;margin-top:6px}.event-tip-row strong{font-weight:600}.event-tip-row>span{color:var(--muted);font-variant-numeric:tabular-nums}.event-tip-details{margin:6px 0 0 18px;display:grid;grid-template-columns:auto 1fr;gap:2px 6px;font-size:var(--fs-xs)}.event-tip-details dt{color:var(--muted);grid-column:1}.event-tip-details dd{margin:0;grid-column:2}
    .markdown-content p{margin:3px 0}.markdown-content h1,.markdown-content h2,.markdown-content h3,.markdown-content h4,.markdown-content h5,.markdown-content h6{font-family:var(--sans);font-weight:650;line-height:1.35;margin:6px 0 3px}
    .markdown-content h1{font-size:var(--fs-lg);letter-spacing:0}.markdown-content h2{font-size:var(--fs-base)}.markdown-content h3,.markdown-content h4,.markdown-content h5,.markdown-content h6{font-size:var(--fs-sm)}
    .markdown-content ul,.markdown-content ol{margin:3px 0;padding-left:20px}.markdown-content li{margin:1px 0}.markdown-content li>.markdown-content{display:inline}.markdown-content li p{display:inline}
    .markdown-content blockquote{margin:4px 0;padding-left:9px;border-left:2px solid var(--line);color:var(--muted)}
    .markdown-content code{font:var(--fs-xs)/1.4 ui-monospace,SFMono-Regular,monospace;background:color-mix(in srgb,var(--line) 25%,transparent);padding:0 3px;border-radius:2px}
    .markdown-content pre{margin:4px 0;padding:6px 8px;max-height:340px;overflow:auto;background:color-mix(in srgb,var(--bg) 65%,var(--panel));white-space:pre}
    .markdown-content pre code{background:none;padding:0}.markdown-table{overflow:auto}.markdown-content table{border-collapse:collapse;font-size:var(--fs-xs);margin:4px 0}.markdown-content th,.markdown-content td{padding:3px 7px;border-bottom:1px solid var(--line);text-align:left}.markdown-content hr{border:0;border-top:1px solid var(--line);margin:6px 0}
    @media(max-width:760px){.conversation-head{padding:8px 10px}.turn-list{padding:0 8px}.timeline-entry{grid-template-columns:29px minmax(0,1fr);gap:5px}.message-prose{font-size:var(--fs-sm)}.file-chip{font-size:var(--fs-2xs)}.chip-preview{margin-left:0}}
    @media(min-width:901px){.conversation-panel{margin-right:110px}}
  </style>
  <style>
    /* Pharos identity: the header, beam, highlights, and empty states borrow the app icon's harbor green, stone, brass, and lamp. */
    :root{--lighthouse-unlit:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='3.5 2 29 48'%3E%3Ccircle cx='18' cy='3.9' r='1.2'/%3E%3Cpath d='M14.6 9.7Q14.9 5.9 18 5.3Q21.1 5.9 21.4 9.7Z'/%3E%3Cpath fill-rule='evenodd' d='M15.2 9.2h5.6v5.4h-5.6ZM16.4 10.4v3h3.2v-3Z'/%3E%3Crect x='15.4' y='14.4' width='5.2' height='4.8'/%3E%3Crect x='13.2' y='18.9' width='9.6' height='2.1' rx='.7'/%3E%3Cpath d='M13 27.6h10l-.8-7.2h-8.4Z'/%3E%3Crect x='10.8' y='27.4' width='14.4' height='2.4' rx='.8'/%3E%3Cpath fill-rule='evenodd' d='M10.4 45h15.2l-1.5-15.8H11.9ZM15.9 45v-3.5a2.1 2.1 0 0 1 4.2 0V45Z'/%3E%3Crect x='7' y='46.2' width='22' height='1.1' rx='.55'/%3E%3Crect x='4' y='48.4' width='7' height='1' rx='.5' opacity='.55'/%3E%3Crect x='25' y='48.4' width='7' height='1' rx='.5' opacity='.55'/%3E%3C/svg%3E")}
    :root{--mast-top:#fbf6ec;--mast-bottom:#e9dfcd;--mast-ink:#263e32;--mast-muted:#536b5b;--mast-brass:#80632d;--mast-tower:#294b39;--mast-door:#e5d7bb;--mast-field:#ffffff80;--mast-border:#afa48f;--mast-hover:#315845;--mast-active-ink:#fffaf0;--mast-shadow:#cfc3ad;--mast-nav:#e5dac6;--mast-error:#9c3d36;--mast-beam:#27503b;--mast-brand:#405b49;--mast-tagline:#6a5c3e;--mast-lit-name:#193e2c;--mast-lit-tagline:#315845;--mast-lit-shadow:#31584533;--mast-lamp:#27503b;--mast-halo:#27503b}
    :root[data-theme="dark"]{--mast-top:#2b5241;--mast-bottom:#1b3a2c;--mast-ink:#f3ebdc;--mast-muted:#c3cebc;--mast-brass:#d4a857;--mast-tower:#eadcbc;--mast-door:#1b3a2c;--mast-field:#ffffff0f;--mast-border:#f3ebdc30;--mast-hover:#f1d690;--mast-active-ink:#10241b;--mast-shadow:#0d1f17;--mast-nav:#0000002e;--mast-error:#ffb4a8;--mast-beam:#ffe7a6;--mast-brand:#b3b5a5;--mast-tagline:#9a8657;--mast-lit-name:#fff3cf;--mast-lit-tagline:#ffd978;--mast-lit-shadow:#ffd97866;--mast-lamp:#fff6d8;--mast-halo:#ffd978}
    @media(prefers-color-scheme:dark){:root:not([data-theme]){--mast-top:#2b5241;--mast-bottom:#1b3a2c;--mast-ink:#f3ebdc;--mast-muted:#c3cebc;--mast-brass:#d4a857;--mast-tower:#eadcbc;--mast-door:#1b3a2c;--mast-field:#ffffff0f;--mast-border:#f3ebdc30;--mast-hover:#f1d690;--mast-active-ink:#10241b;--mast-shadow:#0d1f17;--mast-nav:#0000002e;--mast-error:#ffb4a8;--mast-beam:#ffe7a6;--mast-brand:#b3b5a5;--mast-tagline:#9a8657;--mast-lit-name:#fff3cf;--mast-lit-tagline:#ffd978;--mast-lit-shadow:#ffd97866;--mast-lamp:#fff6d8;--mast-halo:#ffd978}}
    body>header{background:linear-gradient(var(--mast-top),var(--mast-bottom));backdrop-filter:none;border-bottom:1px solid color-mix(in srgb,var(--mast-brass) 45%,var(--mast-bottom));box-shadow:0 1px 0 var(--mast-shadow);color:var(--mast-ink);overflow:hidden}
    :is(body>header,.header-editor) .tabs button{color:var(--mast-muted)}:is(body>header,.header-editor) .tabs button:hover{color:var(--mast-ink)}:is(body>header,.header-editor) .tabs button.active{background:var(--mast-brass);color:var(--mast-active-ink)}
    :is(body>header,.header-editor) .header-icon,:is(body>header,.header-editor) .navigator input,:is(body>header,.header-editor) .navigator button{border-color:var(--mast-border);background:var(--mast-field);color:var(--mast-ink)}
    :is(body>header,.header-editor) .header-icon:hover,:is(body>header,.header-editor) .navigator button:hover{border-color:var(--mast-brass);color:var(--mast-hover)}
    :is(body>header,.header-editor) .navigator-toggle[aria-expanded="true"],:is(body>header,.header-editor) .header-icon[aria-pressed="true"],:is(body>header,.header-editor) .header-icon.active,:is(body>header,.header-editor) .navigator .header-icon.active,:is(body>header,.header-overflow) .ask-pharos-toggle[aria-current="page"],:is(body>header,.header-overflow) :is(.ask-pharos-toggle,#feedbackSubmitToggle,#dataGuideToggle)[aria-expanded="true"]{border-color:var(--mast-brass);background:var(--mast-brass);color:var(--mast-active-ink)}
    @keyframes header-spin{to{transform:rotate(360deg)}}
    /* Combo button: adjacent buttons joined into one control (built by comboButton(), or hand-built as .combo-button > button). Segments overlap by a pixel so they share one inner edge, only the outer corners are rounded, and the hovered, focused, pressed, or expanded segment is raised so the shared edge lights with it. A context sizes and colors it through the --combo-* properties. */
    .combo-button{--combo-height:28px;--combo-radius:5px;--combo-icon:14px;--combo-border:var(--line);--combo-bg:var(--panel);--combo-ink:var(--muted);--combo-accent:var(--accent);--combo-hover-ink:var(--accent);--combo-pressed-bg:color-mix(in srgb,var(--combo-accent) 16%,var(--combo-bg));--combo-pressed-ink:var(--ink);display:inline-flex;vertical-align:middle}.combo-button>button{position:relative;display:inline-flex;align-items:center;justify-content:center;gap:5px;height:var(--combo-height);min-height:0;margin:0;padding:0 10px;border:1px solid var(--combo-border);border-radius:0;background:var(--combo-bg);color:var(--combo-ink);font-size:inherit;font-weight:600;white-space:nowrap}.combo-button>button+button{margin-left:-1px}.combo-button>button:first-child{border-radius:var(--combo-radius) 0 0 var(--combo-radius)}.combo-button>button:last-child{border-radius:0 var(--combo-radius) var(--combo-radius) 0}.combo-button>button:only-child{border-radius:var(--combo-radius)}.combo-button>button[aria-pressed=true]{z-index:1;border-color:var(--combo-accent);background:var(--combo-pressed-bg);color:var(--combo-pressed-ink)}.combo-button>button[aria-expanded=true]{z-index:1;border-color:var(--combo-accent);color:var(--combo-hover-ink)}.combo-button>button:hover:not(:disabled),.combo-button>button:focus-visible{z-index:2;border-color:var(--combo-accent);color:var(--combo-hover-ink)}.combo-button>button:disabled{opacity:.5;cursor:default}.combo-button>.combo-icon{width:var(--combo-height);padding:0}.combo-button svg.app-icon{width:var(--combo-icon);height:var(--combo-icon)}.combo-button>button[aria-busy=true] svg.app-icon,.header-sync[aria-busy=true] svg.app-icon{animation:header-spin 1s linear infinite}@media(prefers-reduced-motion:reduce){.combo-button>button[aria-busy=true] svg.app-icon,.header-sync[aria-busy=true] svg.app-icon{animation:none}}:is(body>header,.header-editor) .combo-button{--combo-height:32px;--combo-radius:4px;--combo-icon:18px;--combo-border:var(--mast-border);--combo-bg:var(--mast-field);--combo-ink:var(--mast-ink);--combo-accent:var(--mast-brass);--combo-hover-ink:var(--mast-hover);--combo-pressed-bg:var(--mast-brass);--combo-pressed-ink:var(--mast-active-ink)}
    .usage-summary{position:fixed;z-index:8000;max-width:calc(100vw - 24px);padding:10px 14px 8px;background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:10px;box-shadow:0 18px 50px #0005}.usage-summary[hidden]{display:none}#usageSummary{box-sizing:border-box;overflow-y:auto}
    .usage-summary td.usage-summary-pending{text-align:center}.usage-summary td a{display:block;color:var(--gold);text-decoration:underline;text-underline-offset:2px}.usage-summary td a:hover{color:var(--accent);text-decoration-thickness:2px}.usage-summary td a:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px;border-radius:2px}
.usage-summary-scroll{overflow-x:auto}.usage-summary table{border-collapse:collapse;font-size:var(--fs-md);font-variant-numeric:tabular-nums}.usage-summary th,.usage-summary td{padding:5px 0 5px 16px;text-align:right;white-space:nowrap}.usage-summary thead th{padding-top:0;border-bottom:1px solid var(--line);color:var(--muted);font-size:var(--fs-sm);font-weight:600}.usage-summary tbody th{padding-left:0;text-align:left;font-weight:600}.usage-summary tbody th a{color:var(--gold);text-decoration:underline;text-underline-offset:2px}.usage-summary tbody th a:hover{color:var(--gold)}.usage-summary tbody th a:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px;border-radius:2px}.usage-summary tbody tr+tr>*{border-top:1px solid color-mix(in srgb,var(--line) 55%,transparent)}.usage-summary-note{margin:6px 0 0;color:var(--muted);font-size:var(--fs-xs)}.usage-summary-note:empty{display:none}@media(max-width:420px){.usage-summary{padding-inline:10px}.usage-summary th,.usage-summary td{padding-left:10px}}.usage-summary thead th:first-child{padding-left:0;text-align:left}.usage-summary-controls{--combo-height:22px;font-size:var(--fs-xs)}
    body>header .navigator{border-top-color:var(--mast-shadow);background:var(--mast-nav);box-shadow:inset 0 1px 0 color-mix(in srgb,var(--mast-brass) 30%,transparent)}
    body>header .navigator .navigator-error{color:var(--mast-error)}
    body>header :focus-visible{outline-color:var(--mast-brass)}
    /* One beam leaves the lantern window; .brand-lit repeats the wordmark in lamp colors, clipped to that same beam, so letters read lit inside it and dim outside. The beam ends at a fixed length rather than 100%, since the app shell sizes the lit copy to the wordmark. */
    .brand{position:relative;flex:none;color:inherit;text-decoration:none;cursor:pointer;--beam:polygon(10.3px 46.7px,420px 0,420px 100%,10.3px 50px)}.brand::before,.brand-lit{position:absolute;left:0;top:50%;width:420px;height:120px;margin-top:-60px;pointer-events:none}.brand::before,.brand-lit-content{clip-path:var(--beam)}.brand-lit{overflow:hidden}.brand-lit-content{height:100%}.brand::before{content:"";z-index:-1;background:linear-gradient(90deg,color-mix(in srgb,var(--mast-beam) 50%,transparent),color-mix(in srgb,var(--mast-beam) 22%,transparent) 12%,color-mix(in srgb,var(--mast-beam) 8%,transparent) 60%,transparent)}.brand-lit{user-select:none}.brand-lit-content{display:flex;align-items:center;gap:11px}.brand-lit-spacer{flex:none;width:37px;height:42px}.brand-name{color:var(--mast-brand)}.brand-tagline{color:var(--mast-tagline);white-space:nowrap}.brand-lit .brand-name{color:var(--mast-lit-name);text-shadow:0 0 8px var(--mast-lit-shadow)}.brand-lit .brand-tagline{color:var(--mast-lit-tagline)}
    input:focus-visible,select:focus-visible,textarea:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:0}
    input[type=checkbox],input[type=radio],input[type=range]{accent-color:var(--accent)}
    ::selection{background:color-mix(in srgb,var(--sea) 35%,transparent)}
    .empty::before,.conversation-results-empty.no-results::before,.mcp-empty::before,.qt-empty::before{content:"";display:block;width:24px;height:40px;margin:0 auto 12px;background:currentColor;opacity:.6;-webkit-mask:var(--lighthouse-unlit) center/contain no-repeat;mask:var(--lighthouse-unlit) center/contain no-repeat}
    .qt-empty::before{display:inline-block;width:19px;height:32px;margin:0 12px 0 0;vertical-align:middle}
    body .qt-qb-num{background:var(--panel);color:var(--ink)}
    .feedback-list .empty::before{content:none}
    @media(max-width:760px){.brand{--beam:polygon(8.4px 49.3px,240px 0,240px 100%,8.4px 51.9px)}.brand::before,.brand-lit{width:240px}.brand-lit-spacer,.brand-beacon{width:30px;height:34px}}
    /* While a source sync runs or a page loads, .brand.sweeping turns the beam around the tower (.loading turns it faster): 0deg points right (the static pose), 90deg at the viewer, 180deg left, 270deg away. The beam's length follows cos; its brightness and the lantern flash follow sin. Each part animates only transform and opacity, sampled every 15deg, so the compositor runs the sweep: animating the clip, gradient or a custom property repaints and re-composites the whole page every frame, which slows long pages in WebKit. The glow scales from the lantern. The lit wordmark keeps the static beam's clip and is revealed up to the beam's end by a clipping box and its content moving in opposite directions. */
    .brand{--lx:7.7px;--lw:2.6px;--ly:46.7px;--lh:3.3px;--reach:412px;--sweep:4s}.brand-mark{overflow:visible}.brand-mark .tower{fill:var(--mast-tower)}.brand-mark .brass{fill:var(--mast-brass)}.brand-mark .door{fill:var(--mast-door)}.brand-mark #brandHalo stop:first-child{stop-color:var(--mast-lamp)}.brand-mark #brandHalo stop:not(:first-child){stop-color:var(--mast-halo)}
    .brand-beacon{position:absolute;left:0;top:0;bottom:0;margin:auto 0;width:37px;height:42px;opacity:0;pointer-events:none}.brand-lamp rect{fill:var(--mast-lamp)}.brand-halo circle{fill:url(#brandHalo)}
    .brand.sweeping.loading{--sweep:1.5s}.brand-mark .door-hit{fill:transparent}
    .brand.sweeping::before{--o:calc(420px + var(--lx));left:-420px;width:840px;transform-origin:var(--o) 50%;clip-path:polygon(calc(var(--o) + var(--lw)) var(--ly),calc(var(--o) + var(--reach)) 0,calc(var(--o) + var(--reach)) 100%,calc(var(--o) + var(--lw)) calc(var(--ly) + var(--lh)));background:linear-gradient(90deg,transparent calc(var(--o) - var(--reach)),color-mix(in srgb,var(--mast-beam) 12%,transparent) calc(var(--o) - .6*var(--reach)),color-mix(in srgb,var(--mast-beam) 34%,transparent) calc(var(--o) - .12*var(--reach)),color-mix(in srgb,var(--mast-beam) 78%,transparent) var(--o),color-mix(in srgb,var(--mast-beam) 34%,transparent) calc(var(--o) + .12*var(--reach)),color-mix(in srgb,var(--mast-beam) 12%,transparent) calc(var(--o) + .6*var(--reach)),transparent calc(var(--o) + var(--reach)))}
    .brand.sweeping::before{animation:pharos-sweep-glow var(--sweep) linear infinite}.brand.sweeping .brand-lit{animation:pharos-sweep-lit var(--sweep) linear infinite}.brand.sweeping .brand-lit-content{animation:pharos-sweep-lit-content var(--sweep) linear infinite}.brand.sweeping .brand-lamp{animation:pharos-sweep-lamp var(--sweep) linear infinite}.brand.sweeping .brand-halo{animation:pharos-sweep-halo var(--sweep) linear infinite}
    @keyframes pharos-sweep-glow{0%{transform:scaleX(1);opacity:0.65}4.1667%{transform:scaleX(0.9659);opacity:0.7406}8.3333%{transform:scaleX(0.866);opacity:0.825}12.5%{transform:scaleX(0.7071);opacity:0.8975}16.6667%{transform:scaleX(0.5);opacity:0.9531}20.8333%{transform:scaleX(0.2588);opacity:0.9881}25%{transform:scaleX(0);opacity:1}29.1667%{transform:scaleX(-0.2588);opacity:0.9881}33.3333%{transform:scaleX(-0.5);opacity:0.9531}37.5%{transform:scaleX(-0.7071);opacity:0.8975}41.6667%{transform:scaleX(-0.866);opacity:0.825}45.8333%{transform:scaleX(-0.9659);opacity:0.7406}50%{transform:scaleX(-1);opacity:0.65}54.1667%{transform:scaleX(-0.9659);opacity:0.5594}58.3333%{transform:scaleX(-0.866);opacity:0.475}62.5%{transform:scaleX(-0.7071);opacity:0.4025}66.6667%{transform:scaleX(-0.5);opacity:0.3469}70.8333%{transform:scaleX(-0.2588);opacity:0.3119}75%{transform:scaleX(0);opacity:0.3}79.1667%{transform:scaleX(0.2588);opacity:0.3119}83.3333%{transform:scaleX(0.5);opacity:0.3469}87.5%{transform:scaleX(0.7071);opacity:0.4025}91.6667%{transform:scaleX(0.866);opacity:0.475}95.8333%{transform:scaleX(0.9659);opacity:0.5594}100%{transform:scaleX(1);opacity:0.65}}
    @keyframes pharos-sweep-lit{0%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*1 - 100%)));opacity:1}4.1667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.9659 - 100%)));opacity:1}8.3333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.866 - 100%)));opacity:1}12.5%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.7071 - 100%)));opacity:1}16.6667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.5 - 100%)));opacity:1}20.8333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.2588 - 100%)));opacity:1}25%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}29.1667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}33.3333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}37.5%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}41.6667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}45.8333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}50%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:1}54.1667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.8447}58.3333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.7}62.5%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.5757}66.6667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.4804}70.8333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.4204}75%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0 - 100%)));opacity:0.4}79.1667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.2588 - 100%)));opacity:0.4204}83.3333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.5 - 100%)));opacity:0.4804}87.5%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.7071 - 100%)));opacity:0.5757}91.6667%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.866 - 100%)));opacity:0.7}95.8333%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*0.9659 - 100%)));opacity:0.8447}100%{transform:translateX(min(0%,calc(var(--lx) + var(--reach)*1 - 100%)));opacity:1}}
    @keyframes pharos-sweep-lit-content{0%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*1)))}4.1667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.9659)))}8.3333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.866)))}12.5%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.7071)))}16.6667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.5)))}20.8333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.2588)))}25%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}29.1667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}33.3333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}37.5%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}41.6667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}45.8333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}50%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}54.1667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}58.3333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}62.5%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}66.6667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}70.8333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}75%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0)))}79.1667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.2588)))}83.3333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.5)))}87.5%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.7071)))}91.6667%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.866)))}95.8333%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*0.9659)))}100%{transform:translateX(max(0%,calc(100% - var(--lx) - var(--reach)*1)))}}
    @keyframes pharos-sweep-lamp{0%{opacity:0.125}4.1667%{opacity:0.2493}8.3333%{opacity:0.4219}12.5%{opacity:0.6219}16.6667%{opacity:0.8122}20.8333%{opacity:0.9498}25%{opacity:1}29.1667%{opacity:0.9498}33.3333%{opacity:0.8122}37.5%{opacity:0.6219}41.6667%{opacity:0.4219}45.8333%{opacity:0.2493}50%{opacity:0.125}54.1667%{opacity:0.0509}58.3333%{opacity:0.0156}62.5%{opacity:0.0031}66.6667%{opacity:0.0003}70.8333%{opacity:0}75%{opacity:0}79.1667%{opacity:0}83.3333%{opacity:0.0003}87.5%{opacity:0.0031}91.6667%{opacity:0.0156}95.8333%{opacity:0.0509}100%{opacity:0.125}}
    @keyframes pharos-sweep-halo{0%{opacity:0.0156}4.1667%{opacity:0.0622}8.3333%{opacity:0.178}12.5%{opacity:0.3867}16.6667%{opacity:0.6597}20.8333%{opacity:0.902}25%{opacity:1}29.1667%{opacity:0.902}33.3333%{opacity:0.6597}37.5%{opacity:0.3867}41.6667%{opacity:0.178}45.8333%{opacity:0.0622}50%{opacity:0.0156}54.1667%{opacity:0.0026}58.3333%{opacity:0.0002}62.5%{opacity:0}66.6667%{opacity:0}70.8333%{opacity:0}75%{opacity:0}79.1667%{opacity:0}83.3333%{opacity:0}87.5%{opacity:0}91.6667%{opacity:0.0002}95.8333%{opacity:0.0026}100%{opacity:0.0156}}
    @media(max-width:760px){.brand{--lx:6.3px;--lw:2.1px;--ly:49.3px;--lh:2.6px;--reach:232px}.brand.sweeping::before{--o:calc(240px + var(--lx));left:-240px;width:480px}}
    @media(prefers-reduced-motion:reduce){.brand.sweeping::before,.brand.sweeping :is(.brand-lit,.brand-lit-content,.brand-beacon){animation:none}.brand.sweeping::before{opacity:.65}.brand.sweeping .brand-lamp{opacity:.125}.brand.sweeping .brand-halo{opacity:.0156}}
    .health-link{cursor:pointer;text-decoration:none;color:inherit;transition:border-color .15s}.health-link:hover,.health-link:focus-visible{border-color:var(--accent)}.health-link .health-open{color:var(--accent)}.health-pricing-open{display:block;margin-top:8px;padding:0;border:0;background:none;color:var(--accent);font:inherit;cursor:pointer;text-align:left}.health-pricing-open:hover{text-decoration:underline}.health-host-list{display:grid;gap:7px;margin-top:9px}.health-host-list>div{display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap}
    .authorship-target .message-card{outline:3px solid var(--attention-ring);outline-offset:3px;border-radius:8px;transition:outline-color 1s}
    /* All interface icons share the annotation target's light 24px line system. See docs/iconography.md. */
    .app-icon-sprite{position:absolute;width:0;height:0;overflow:hidden;pointer-events:none}
    :root{--select-caret-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m5.5 9 6.5 6 6.5-6' fill='none' stroke='%23756c5f' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E")}
    :root[data-theme=dark]{--select-caret-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m5.5 9 6.5 6 6.5-6' fill='none' stroke='%23b5ab93' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E")}
    @media(prefers-color-scheme:dark){:root:not([data-theme]){--select-caret-image:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m5.5 9 6.5 6 6.5-6' fill='none' stroke='%23b5ab93' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E")}}
    /* Keep the caret and its geometry together above component background shorthands.
       Theme changes only replace the inherited image; custom/native carets can opt out. */
    :root body select:not([multiple]){background-image:var(--select-caret-image);background-position:right 9px center;background-size:14px 14px;background-repeat:no-repeat;padding-right:28px}
    svg.app-icon{display:inline-block;flex:none;width:16px;height:16px;vertical-align:middle;fill:none;stroke:currentColor;stroke-width:1.6;stroke-linecap:round;stroke-linejoin:round;overflow:visible}
    .header-icon svg.app-icon{width:18px;height:18px;stroke-width:1.6}
    .navigator-go svg.app-icon{width:16px;height:16px;stroke-width:1.6}
    .bookmark-star.bookmarked svg.app-icon{fill:none}
    .button-with-icon,.back,#bookmarkAdd,#feedbackTarget{display:inline-flex;align-items:center;justify-content:center;gap:7px}
    .drawer-close,.history-star,.history-remove,.bookmark-edit,.bookmark-remove{display:inline-grid;place-items:center}
    .drawer-close svg.app-icon{width:17px;height:17px}
    .history-star svg.app-icon,.history-remove svg.app-icon,.bookmark-edit svg.app-icon,.bookmark-remove svg.app-icon,.bookmark-handle svg.app-icon{width:15px;height:15px}
    .history-chevron{display:inline-flex;align-items:center;justify-content:center;width:14px;transition:transform .15s}
    .history-chevron svg.app-icon{width:14px;height:14px}
    .kind-icon{display:inline-flex;align-items:center;justify-content:center;flex:0 0 16px;width:16px;height:16px;line-height:1}
    .kind-icon svg.app-icon,.todo-state svg.app-icon{width:15px;height:15px}
    .file-copy,.message-raw-button{display:inline-grid;place-items:center}
    .conversation-title-line{display:flex;flex-wrap:wrap;align-items:center;gap:6px 14px}
    .copy-values{display:inline-flex;flex-wrap:wrap;gap:6px}
    .copy-value{display:inline-flex;align-items:center;gap:4px;padding:1px 6px;border:1px solid var(--line);border-radius:4px;background:none;color:var(--muted);font-size:var(--fs-xs);line-height:1.5;cursor:pointer}
    .copy-value:hover,.copy-value:focus-visible{color:var(--ink);border-color:var(--muted)}
    .copy-value svg.app-icon{width:12px;height:12px}
    .message-card>.copy-values{position:absolute;z-index:1;top:-11px;right:22px;padding:2px;border-radius:5px;background:var(--panel);box-shadow:0 1px 4px #0003;opacity:0;pointer-events:none;transition:opacity .12s}
    .speaker-row:hover .message-card>.copy-values,.message-card>.copy-values:focus-within{opacity:1;pointer-events:auto}
    @media(hover:none){.message-card>.copy-values{opacity:1;pointer-events:auto}}
    .file-copy svg.app-icon,.message-raw-button svg.app-icon,.markdown-task svg.app-icon{width:14px;height:14px}
    .markdown-task{display:inline-flex;vertical-align:middle;margin-right:4px}
    .todo-item{align-items:center}.todo-state{display:inline-flex;align-items:center}
    .turn>summary::before,.tool-event>summary::before,.subagent-run>summary::before,.xml-node>summary::before{content:'';display:inline-block;flex:0 0 12px;width:12px;height:12px;background:currentColor;-webkit-mask:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m9.5 5.5 6.5 6.5-6.5 6.5' fill='none' stroke='black' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E") center/contain no-repeat;mask:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24'%3E%3Cpath d='m9.5 5.5 6.5 6.5-6.5 6.5' fill='none' stroke='black' stroke-width='1.6' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E") center/contain no-repeat}
    .turn[open]>summary::before,.tool-event[open]>summary::before,.subagent-run[open]>summary::before,.xml-node[open]>summary::before{content:'';transform:rotate(90deg)}
    .xml-node:not([open])>summary::before{content:''}
    .action-row .tool-event>summary::before,.subagent-run>summary::before{display:none}
    .xml-node>summary::before{position:absolute;left:-13px;top:2px;line-height:normal}
    .health{grid-template-columns:repeat(4,minmax(0,1fr))}
    @media(max-width:1050px){.health{grid-template-columns:repeat(3,minmax(0,1fr))}}
    @media(max-width:760px){.health{grid-template-columns:repeat(2,minmax(0,1fr))}}
    @media(max-width:500px){.health{grid-template-columns:1fr}.header-icons{flex:0 1 auto;min-width:0;flex-wrap:wrap;row-gap:6px}.navigator.open{flex-wrap:wrap}.navigator-uri{order:-1;flex-basis:100%}.navigator .navigator-error{order:1;flex-basis:100%}}
    .dictation-button[data-state=listening],.dictation-button[data-state=stopping]{color:var(--bad)!important}
    .dictation-button[data-state=starting],.dictation-button[data-state=preparing]{opacity:.6}
    @media(prefers-reduced-motion:no-preference){.dictation-button[data-state=listening] .app-icon{animation:dictation-listening 1.4s ease-in-out infinite}}
    @keyframes dictation-listening{50%{opacity:.4}}
    .dictation-field{position:relative}
    .dictation-field>.dictation-button:not([hidden]){position:absolute;top:6px;right:10px;display:inline-flex;align-items:center;justify-content:center;width:28px;height:28px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--muted)}
    .dictation-field>.dictation-button:hover{background:color-mix(in srgb,var(--accent) 10%,transparent);color:var(--accent)}
    .dictation-field>.dictation-button .app-icon{width:17px;height:17px}
    .dictation-field:has(>.dictation-button:not([hidden]))>textarea{padding-right:40px}
    .feedback-composer .dictation-field{margin:15px 0 12px}.feedback-composer .dictation-field>textarea{display:block;margin:0}
  </style>
  <script src="/assets/host-services.js"></script>
  <script src="/assets/dictation.js"></script>
  <script src="/assets/page-errors.js"></script>
  <script src="/assets/currency.js"></script>
  <script src="/assets/preferences.js"></script>
  <script src="/assets/managed-storage.js"></script>
  <script>window.pharosPrefs=(function(){
    // Preferences and saved views live in the library (internal/archive/preferences.go), so they follow it between Macs. A page with no library behind it keeps them in the web view's own storage instead.
    const source=window.pharosPreferenceValues,values=source&&typeof source==='object'?{...source}:null;
    const LEGACY=new Set(['pharos-theme','pharos-show-nav-button','pharos-show-agent-annotation-button','pharos-bookmarks-v1','pharos-transcript-filter','pharos-carbon-v1','pharos-concurrency-v1','pharos-ui-feedback-clear-on-copy','pharos-pricing-acknowledged-models','pharos-usage-view','pharos-usage-summary-auto','pharos-usage-chart','pharos-writing-view','tl1-project','tl1-window']),LEGACY_PREFIXES=['query-table:saved:','query-table:default:'];
    const isRaw=key=>key==='pharos-theme'||key==='pharos-usage-view'||key==='tl1-project'||key.startsWith('query-table:default:');
    const parse=raw=>{try{return JSON.parse(raw)}catch{return raw}};
    const send=body=>{const text=JSON.stringify(body);return fetch('/api/preferences',{method:'POST',headers:{'Content-Type':'application/json'},body:text,keepalive:text.length<60000}).then(response=>{if(!response.ok)throw new Error('HTTP '+response.status);dispatchEvent(new CustomEvent('pharos:preferences-saved',{detail:body}))})};
    let pending={set:{},delete:new Set()},timer=0;
    // flush resolves once queued changes are saved (or failed), for callers that refetch server values derived from them.
    function flush(){clearTimeout(timer);timer=0;const body={set:pending.set,delete:[...pending.delete]};if(!Object.keys(body.set).length&&!body.delete.length)return Promise.resolve();pending={set:{},delete:new Set()};return send(body).catch(error=>console.warn('Could not save preferences',error))}
    const queue=()=>{if(!timer)timer=setTimeout(flush,250)};
    if(values){
      addEventListener('pagehide',flush);
      // This web view's earlier copies move into the library once. A copy the library already has is left alone, so a second Mac's values are never overwritten.
      const moved={};
      try{for(let index=0;index<localStorage.length;index++){const key=localStorage.key(index);if(key&&!(key in values)&&(LEGACY.has(key)||LEGACY_PREFIXES.some(prefix=>key.startsWith(prefix))))moved[key]=isRaw(key)?localStorage.getItem(key):parse(localStorage.getItem(key))}}catch{}
      if(Object.keys(moved).length){Object.assign(values,moved);send({set:moved}).then(()=>{try{for(const key of Object.keys(moved))localStorage.removeItem(key)}catch{}}).catch(error=>console.warn('Could not move preferences into the library',error))}
    }
    return{
      get(key,fallback){if(values)return key in values?values[key]:fallback;try{const raw=localStorage.getItem(key);return raw===null?fallback:parse(raw)}catch{return fallback}},
      set(key,value){if(values){values[key]=value;pending.set[key]=value;pending.delete.delete(key);queue()}else{try{localStorage.setItem(key,typeof value==='string'?value:JSON.stringify(value))}catch{}}},
      remove(key){if(values){delete values[key];delete pending.set[key];pending.delete.add(key);queue()}else{try{localStorage.removeItem(key)}catch{}}},
      keys(prefix){if(values)return Object.keys(values).filter(key=>key.startsWith(prefix));const found=[];try{for(let index=0;index<localStorage.length;index++){const key=localStorage.key(index);if(key&&key.startsWith(prefix))found.push(key)}}catch{}return found},
      flush
    }
  })()</script>
  <script>(function(){const theme=pharosPrefs.get('pharos-theme','dark');if(theme==='light'||theme==='dark')document.documentElement.dataset.theme=theme})()</script>
  <script src="/assets/onboarding.js" defer></script>
  <script src="/assets/upgrade.js" defer></script>
  <script src="/assets/status-store.js" defer></script>
  <script src="/assets/library.js" defer></script>
  <script src="/assets/status-panel.js" defer></script>
  <script src="/assets/cloud-backup.js" defer></script>
  <script src="/assets/sync.js" defer></script>
  <script src="/assets/background.js" defer></script>
  <script src="/assets/carbon.js" defer></script>
  <script src="/assets/menu-bar.js" defer></script>
  <link rel="stylesheet" href="/assets/query-tables.css">
<link rel="stylesheet" href="/assets/ask-pharos.css"></head>
<body><svg class="app-icon-sprite" xmlns="http://www.w3.org/2000/svg" aria-hidden="true"><defs><symbol id="ph-icon-chat" viewBox="0 0 24 24"><path d="M3 13a4 4 0 0 0 4 4h9l5 4V7a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4Z"/></symbol><symbol id="ph-icon-thought" viewBox="0 0 24 24"><path d="M6.6 12.3C4.6 12.6 3 11.2 3 9.2C3 7.2 4.6 5.6 6.8 5.9C7.5 3.7 9.3 2.5 11.5 2.5C13.8 2.5 15.8 3.9 16.4 6C19.3 5.7 21.6 7.7 21.6 10.2C21.6 12.2 20.1 13.6 18.4 13.8C16.8 15.1 15.2 14.2 13.8 12.7C11.8 10.9 9.2 11.2 6.6 12.3Z"/><circle cx="9.8" cy="17.1" r="2.6"/><circle cx="3.3" cy="21" r="1.9"/></symbol><symbol id="ph-icon-search" viewBox="0 0 24 24"><circle cx="10.8" cy="10.8" r="6.6"/><path d="m16 16 4.3 4.3"/></symbol><symbol id="ph-icon-target" viewBox="0 0 24 24"><circle cx="12" cy="12" r="6.5"/><circle cx="12" cy="12" r="2.4"/><path d="M12 2v3.1M12 18.9V22M2 12h3.1M18.9 12H22"/></symbol><symbol id="ph-icon-settings" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3.1"/><path d="M9.8 3.3h4.4l.5 2.1 1.4.8 2.1-.6 2.2 3.8-1.6 1.5v1.8l1.6 1.5-2.2 3.8-2.1-.6-1.4.8-.5 2.1H9.8l-.5-2.1-1.4-.8-2.1.6-2.2-3.8 1.6-1.5v-1.8L3.6 9.4l2.2-3.8 2.1.6 1.4-.8z"/></symbol><symbol id="ph-icon-theme-system" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8"/><path d="M12 4v16"/></symbol><symbol id="ph-icon-sun" viewBox="0 0 24 24"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4m11.4 11.4 1.4 1.4M2 12h2m16 0h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></symbol><symbol id="ph-icon-moon" viewBox="0 0 24 24"><path d="M19.3 16.3A8.3 8.3 0 0 1 7.7 4.7 8.3 8.3 0 1 0 19.3 16.3Z"/></symbol><symbol id="ph-icon-compass" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="m15.3 8.7-1.9 4.7-4.7 1.9 1.9-4.7z"/></symbol><symbol id="ph-icon-tree" viewBox="0 0 24 24"><rect x="3" y="3" width="5" height="5" rx="1"/><rect x="15" y="10" width="6" height="4" rx="1"/><rect x="15" y="17" width="6" height="4" rx="1"/><path d="M5.5 8v9a2 2 0 0 0 2 2H15M5.5 12H15"/></symbol><symbol id="ph-icon-sidebar" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="M9 4v16"/></symbol><symbol id="ph-icon-chevron-left" viewBox="0 0 24 24"><path d="m14.5 5.5-6.5 6.5 6.5 6.5"/></symbol><symbol id="ph-icon-chevron-right" viewBox="0 0 24 24"><path d="m9.5 5.5 6.5 6.5-6.5 6.5"/></symbol><symbol id="ph-icon-chevron-down" viewBox="0 0 24 24"><path d="m5.5 9 6.5 6 6.5-6"/></symbol><symbol id="ph-icon-chevron-up" viewBox="0 0 24 24"><path d="m5.5 15 6.5-6 6.5 6"/></symbol><symbol id="ph-icon-arrow-left" viewBox="0 0 24 24"><path d="M20 12H4m6-6-6 6 6 6"/></symbol><symbol id="ph-icon-arrow-right" viewBox="0 0 24 24"><path d="M4 12h16m-6-6 6 6-6 6"/></symbol><symbol id="ph-icon-arrow-up" viewBox="0 0 24 24"><path d="M12 20V4m-6 6 6-6 6 6"/></symbol><symbol id="ph-icon-arrow-down" viewBox="0 0 24 24"><path d="M12 4v16m-6-6 6 6 6-6"/></symbol><symbol id="ph-icon-star" viewBox="0 0 24 24"><path d="m12 2.8 2.8 5.8 6.4.9-4.6 4.5 1.1 6.3-5.7-3-5.7 3 1.1-6.3-4.6-4.5 6.4-.9z"/></symbol><symbol id="ph-icon-bookmark" viewBox="0 0 24 24"><path d="M6.5 3.5h11v17l-5.5-4-5.5 4z"/></symbol><symbol id="ph-icon-history" viewBox="0 0 24 24"><path d="M3.4 10a8.7 8.7 0 1 1 .2 4.7M3.4 4.8V10h5.2M12 7v5l3.2 2"/></symbol><symbol id="ph-icon-summary" viewBox="0 0 24 24"><path d="M4 20h16M7 16.5V12m5 4.5V6.5m5 10v-7"/></symbol><symbol id="ph-icon-refresh" viewBox="0 0 24 24"><path d="M20 9a8.2 8.2 0 0 0-14.2-3L4 8m0-5v5h5M4 15a8.2 8.2 0 0 0 14.2 3L20 16m0 5v-5h-5"/></symbol><symbol id="ph-icon-close" viewBox="0 0 24 24"><path d="M5.5 5.5 18.5 18.5M18.5 5.5 5.5 18.5"/></symbol><symbol id="ph-icon-more" viewBox="0 0 24 24"><circle cx="5.5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="18.5" cy="12" r="1"/></symbol><symbol id="ph-icon-grip" viewBox="0 0 24 24"><circle cx="9" cy="6" r=".7"/><circle cx="15" cy="6" r=".7"/><circle cx="9" cy="12" r=".7"/><circle cx="15" cy="12" r=".7"/><circle cx="9" cy="18" r=".7"/><circle cx="15" cy="18" r=".7"/></symbol><symbol id="ph-icon-edit" viewBox="0 0 24 24"><path d="M4 19.5h16M6.5 15.5l9.9-9.9 2.4 2.4-9.9 9.9-3.2.8zM15.5 6.5l2-2a1.7 1.7 0 0 1 2.4 2.4l-2 2"/></symbol><symbol id="ph-icon-check" viewBox="0 0 24 24"><path d="m4.5 12.5 5 5 10-11"/></symbol><symbol id="ph-icon-info" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M12 10.5v5m0-8h.01"/></symbol><symbol id="ph-icon-severity-high" viewBox="0 0 24 24"><path d="M12 3.5 21 20H3zM12 9v5m0 3h.01"/></symbol><symbol id="ph-icon-severity-medium" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5v5.5m0 3.5h.01"/></symbol><symbol id="ph-icon-severity-low" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/></symbol><symbol id="ph-icon-minus" viewBox="0 0 24 24"><path d="M5 12h14"/></symbol><symbol id="ph-icon-plus" viewBox="0 0 24 24"><path d="M12 5v14M5 12h14"/></symbol><symbol id="ph-icon-git" viewBox="0 0 24 24"><circle cx="7" cy="5" r="1.7"/><circle cx="7" cy="19" r="1.7"/><circle cx="17" cy="10" r="1.7"/><path d="M7 6.7v10.6m0-7.3c0 2.5 2.4 3.5 6 3.5s4-1 4-1.8"/></symbol><symbol id="ph-icon-code" viewBox="0 0 24 24"><path d="m8.5 7-5 5 5 5m7-10 5 5-5 5M13.7 4l-3.4 16"/></symbol><symbol id="ph-icon-typecheck" viewBox="0 0 24 24"><path d="M3 5h12M9 5v13m-4 0h8m2-4 2.2 2.2 3.8-4.2"/></symbol><symbol id="ph-icon-lint" viewBox="0 0 24 24"><path d="M5 7h14M5 12h11M5 17h8"/></symbol><symbol id="ph-icon-todo" viewBox="0 0 24 24"><rect x="3.5" y="3.5" width="17" height="17" rx="2"/><path d="m7 12 3 3 6-6"/></symbol><symbol id="ph-icon-command" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="2"/><path d="m6.5 9 3 3-3 3M12 15h5"/></symbol><symbol id="ph-icon-read" viewBox="0 0 24 24"><path d="M12 6.5C9.7 4.9 6.9 4.7 3.5 5v13c3.4-.3 6.2-.1 8.5 1.5 2.3-1.6 5.1-1.8 8.5-1.5V5c-3.4-.3-6.2-.1-8.5 1.5ZM12 6.5v13"/></symbol><symbol id="ph-icon-web" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M3.8 12h16.4M12 3.5c2.4 2.4 3.5 5.2 3.5 8.5S14.4 18.1 12 20.5c-2.4-2.4-3.5-5.2-3.5-8.5S9.6 5.9 12 3.5Z"/></symbol><symbol id="ph-icon-agent" viewBox="0 0 24 24"><path d="M12 3.5 20.5 12 12 20.5 3.5 12z"/><circle cx="12" cy="12" r="2.4"/></symbol><symbol id="ph-icon-human" viewBox="0 0 24 24"><circle cx="12" cy="7.5" r="3"/><path d="M5.5 20v-2.2a6.5 6.5 0 0 1 13 0V20"/></symbol><symbol id="ph-icon-error" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M12 7v6m0 3.5h.01"/></symbol><symbol id="ph-icon-event" viewBox="0 0 24 24"><circle cx="12" cy="12" r="2.2" fill="currentColor" stroke="none"/></symbol><symbol id="ph-icon-context" viewBox="0 0 24 24"><path d="M4 8.5c2.7-2.7 5.3-2.7 8 0s5.3 2.7 8 0M4 15.5c2.7-2.7 5.3-2.7 8 0s5.3 2.7 8 0"/></symbol><symbol id="ph-icon-tool" viewBox="0 0 24 24"><path d="m14.5 4.5 5 5-9.8 9.8-5-5zM12 7l5 5"/></symbol><symbol id="ph-icon-checkbox" viewBox="0 0 24 24"><rect x="4" y="4" width="16" height="16" rx="2"/></symbol><symbol id="ph-icon-undo" viewBox="0 0 24 24"><path d="M9 8H4V3m0 5a8.5 8.5 0 1 1 1.4 9.8"/></symbol><symbol id="ph-icon-redo" viewBox="0 0 24 24"><path d="M15 8h5V3m0 5a8.5 8.5 0 1 0-1.4 9.8"/></symbol><symbol id="ph-icon-mic" viewBox="0 0 24 24"><rect x="9" y="3" width="6" height="11.5" rx="3"/><path d="M5.5 11a6.5 6.5 0 0 0 13 0M12 17.5V21"/></symbol><symbol id="ph-icon-copy" viewBox="0 0 24 24"><rect x="8" y="8" width="11.5" height="11.5" rx="1.5"/><path d="M16 8V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16H8"/></symbol><symbol id="ph-icon-file" viewBox="0 0 24 24"><path d="M14 3H5v18h14V8zM14 3v5h5M8 12h8M8 16h6"/></symbol><symbol id="ph-icon-server" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4m-5-12 2 2-2 2m5 0h4"/></symbol><symbol id="ph-icon-link-out" viewBox="0 0 24 24"><path d="M13 5h6v6M19 5l-9 9M19 14v4a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h4"/></symbol><symbol id="ph-icon-link" viewBox="0 0 24 24"><path d="m9 15 6-6M8 16l-1 1a4 4 0 0 1-6-6l4-4a4 4 0 0 1 6 0m2 10a4 4 0 0 0 6 0l4-4a4 4 0 0 0-6-6l-1 1"/></symbol><symbol id="ph-icon-mail" viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3 6 9 7 9-7"/></symbol><symbol id="ph-icon-cpu" viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="1.5"/><rect x="9.5" y="9.5" width="5" height="5" rx=".5"/><path d="M9.5 3v3m5-3v3m-5 12v3m5-3v3M3 9.5h3m-3 5h3m12-5h3m-3 5h3"/></symbol><symbol id="ph-icon-memory" viewBox="0 0 24 24"><rect x="3" y="7" width="18" height="9" rx="1.5"/><path d="M7 10v3m3.3-3v3m3.4-3v3M17 10v3M6 16v2.5m4-2.5v2.5m4-2.5v2.5m4-2.5v2.5"/></symbol><symbol id="ph-icon-stop" viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2"/></symbol><symbol id="ph-icon-eject" viewBox="0 0 24 24"><path d="M12 5 4.5 14h15zM4.5 18.5h15"/></symbol><symbol id="ph-icon-io" viewBox="0 0 24 24"><path d="M8 20V5m-4 4 4-4 4 4M16 4v15m-4-4 4 4 4-4"/></symbol><symbol id="ph-icon-disk" viewBox="0 0 24 24"><ellipse cx="12" cy="6" rx="7.5" ry="2.5"/><path d="M4.5 6v12c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5V6M4.5 12c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5"/></symbol><symbol id="ph-icon-freshness-database" viewBox="0 0 24 24"><ellipse cx="9" cy="6" rx="5.5" ry="2.5"/><path d="M3.5 6v11c0 1.4 2.5 2.5 5.5 2.5M14.5 6v3M3.5 11c0 1.4 2.5 2.5 5.5 2.5"/></symbol><symbol id="ph-icon-freshness-check" viewBox="0 0 24 24"><path d="m12.5 17 3 3 5.5-6"/></symbol><symbol id="ph-icon-freshness-clock" viewBox="0 0 24 24"><circle cx="16" cy="16" r="5"/></symbol><symbol id="ph-icon-freshness-unknown" viewBox="0 0 24 24"><circle cx="16" cy="16" r="5"/><path d="M14.5 14.3a1.5 1.5 0 1 1 2.3 1.3c-.8.4-.8.7-.8 1.1m0 2h.01"/></symbol><symbol id="ph-icon-freshness-missing" viewBox="0 0 24 24"><circle cx="16" cy="16" r="5"/><path d="M13.5 16h5"/></symbol></defs></svg><header data-feedback-label="App header"><div class="bar"><a class="brand" href="/" aria-label="Pharos home" data-feedback-label="Pharos brand"><svg class="brand-mark" viewBox="10 2 38 43.5" aria-hidden="true"><defs><radialGradient id="brandHalo"><stop offset="0" stop-color="#fff6d8"/><stop offset=".3" stop-color="#ffd978" stop-opacity=".7"/><stop offset="1" stop-color="#ffd978" stop-opacity="0"/></radialGradient></defs><g class="tower"><polygon points="10.4,45 25.6,45 24.1,29.2 11.9,29.2"/><polygon points="13,27.6 23,27.6 22.2,20.4 13.8,20.4"/><rect x="15.4" y="15.2" width="5.2" height="4"/><path d="M14.6 9.7Q14.9 5.9 18 5.3Q21.1 5.9 21.4 9.7Z"/><circle cx="18" cy="3.9" r="1.2"/></g><g class="brass"><rect x="10.8" y="27.4" width="14.4" height="2.4" rx=".8"/><rect x="13.2" y="18.9" width="9.6" height="2.1" rx=".7"/><rect x="15.2" y="9.2" width="5.6" height="5.4" rx=".9"/></g><path class="door" d="M15.9 45v-3.5a2.1 2.1 0 0 1 4.2 0V45Z"/><rect class="door-hit" x="14.4" y="38.4" width="7.2" height="7.1"/></svg><svg class="brand-beacon brand-halo" viewBox="10 2 38 43.5" aria-hidden="true"><circle cx="18" cy="11.9" r="7"/></svg><svg class="brand-beacon brand-lamp" viewBox="10 2 38 43.5" aria-hidden="true"><rect x="16.1" y="10.1" width="3.8" height="3.6" rx=".4"/></svg><span class="brand-words"><span class="brand-name">Pharos</span><small class="brand-tagline">Token catalog and optimizer</small></span><span class="brand-lit" aria-hidden="true"><span class="brand-lit-content"><span class="brand-lit-spacer"></span><span class="brand-words"><span class="brand-name">Pharos</span><small class="brand-tagline">Token catalog and optimizer</small></span></span></span></a><div id="appLocation"></div><nav class="tabs" aria-label="Primary navigation" data-feedback-label="Primary navigation"><button data-view="home" class="active">Home</button><button data-view="library">Search</button><button data-view="analyze">Analyze</button><button data-view="findings" id="findingsTab">Optimize<span class="findings-badge" id="findingsBadge" hidden></span></button></nav><div class="header-icons"><div id="headerHistory" class="combo-button header-history" role="group" aria-label="Page navigation"><button id="navBack" class="header-icon combo-icon" type="button" aria-label="Back" title="Back (⌘[)"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-left"/></svg></button><button id="navForward" class="header-icon combo-icon" type="button" aria-label="Forward" title="Forward (⌘])"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-right"/></svg></button></div><button id="headerBackOnly" class="header-icon" type="button" aria-label="Back" title="Back (⌘[)" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-left"/></svg></button><div id="headerLibrary"></div><button id="headerSyncOnly" class="header-icon header-sync" type="button" aria-label="Index changes" title="Index changes" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-refresh"/></svg></button><button id="searchToggle" class="header-icon search-toggle" type="button" aria-label="Search library" title="Search library (⌘K)" aria-haspopup="dialog" aria-controls="searchDialog"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-search"/></svg></button><button id="askPharosToggle" class="header-icon ask-pharos-toggle" type="button" aria-label="Ask Pharos" title="Ask Pharos about this page" aria-haspopup="dialog" aria-controls="askPharosDialog" disabled><span class="ask-pharos-icon" aria-hidden="true"><svg class="app-icon" viewBox="0 0 24 24"><use href="#ph-icon-chat"/></svg></span></button><button id="dataReadinessToggle" class="header-icon" type="button" data-view-field="readiness_open" aria-label="Data updates" title="Data updates" aria-haspopup="dialog" aria-controls="dataReadinessDialog"></button><button id="usageSummaryToggle" class="header-icon usage-summary-toggle" type="button" aria-label="Quick Stats" title="Quick Stats" aria-haspopup="dialog" aria-expanded="false" aria-controls="usageSummary"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-summary"/></svg></button><button id="dataGuideToggle" class="header-icon" type="button" hidden data-view-field="guide_open" aria-label="Data Guide" title="Data Guide" aria-haspopup="dialog" aria-controls="dataGuideDialog" aria-expanded="false" onclick="pharosOpenDataGuide(undefined,headerToolAnchor(this))"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-read"/></svg></button><button id="feedbackToggle" class="header-icon feedback-toggle" type="button" hidden data-feedback-ui title="Show annotation toolbar" aria-label="Show annotation toolbar" aria-pressed="false"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-target"/></svg></button><button id="feedbackSubmitToggle" class="header-icon" type="button" aria-label="Send feedback" title="Send feedback"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-edit"/></svg></button><button id="settingsToggle" data-view="settings" class="header-icon settings-toggle" type="button" aria-label="Settings" title="Settings"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-settings"/></svg></button><button id="themeToggle" class="header-icon theme-toggle" type="button" aria-label="Color theme: System" title="Theme: System. Click to use Light."><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-theme-system"/></svg></button><button id="navigatorToggle" class="header-icon navigator-toggle" type="button" hidden title="Show navigation bar" aria-label="Navigation bar" aria-controls="navigator" aria-expanded="false"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-compass"/></svg></button><button id="headerMore" class="header-icon header-more" type="button" aria-label="More tools" title="More tools" aria-haspopup="dialog" aria-controls="headerOverflow" aria-expanded="false" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-more"/></svg></button></div></div><form id="navigator" class="navigator" aria-label="Navigation bar" data-feedback-label="Navigation bar"><span class="navigator-uri"><input id="navigatorURI" type="text" aria-label="Pharos URI" spellcheck="false" autocomplete="off"><button id="navigatorGo" class="navigator-go" type="submit" aria-label="Go" title="Go" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-arrow-right"/></svg></button></span><button id="navigatorCopy" type="button">Copy</button><span id="navigatorError" class="navigator-error" role="alert"></span><button id="bookmarkStar" class="header-icon bookmark-star" type="button" aria-label="Bookmark this page" title="Bookmark this page"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-star"/></svg></button><button id="bookmarksToggle" class="header-icon bookmarks-toggle" type="button" aria-label="Bookmarks" title="Bookmarks" aria-expanded="false" aria-controls="bookmarksDrawer"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-bookmark"/></svg></button><button id="historyToggle" data-view-field="overlay" class="header-icon history-toggle" type="button" aria-label="History" title="History" aria-expanded="false" aria-controls="historyDrawer"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-history"/></svg></button></form></header><section id="headerOverflow" class="header-overflow" popover="auto" role="dialog" aria-label="Header tools"><div class="header-overflow-heading"><strong>Tools</strong><button id="headerOverflowClose" type="button" aria-label="Close tools"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-close"/></svg></button></div><div id="headerOverflowTools"></div></section><dialog id="searchDialog" class="search-dialog" aria-labelledby="searchDialogTitle"><div id="searchComposerDialog"></div></dialog><section id="usageSummary" class="usage-summary" role="dialog" aria-label="Quick Stats" data-feedback-label="Usage summary" hidden></section>
<main>
  <section id="home" class="view active" data-feedback-label="Home view"><div id="homePage"></div></section>
  <section id="cards" class="view" data-feedback-label="Card Library view"><div id="cardLibraryPage"></div></section>
  <section id="library" class="view" data-feedback-label="Search view"><div id="queryTableLibrary"></div></section>
  <section id="settings" class="view settings-view" data-feedback-label="Settings view"><div id="settingsPage"></div>
    <div id="settingsContent" class="settings-content">
    <section id="health" class="settings-section" data-settings-route="/settings" data-page-title="Library health" data-page-description="Check your library’s usage, storage, and recent activity." aria-labelledby="settingsPageHeading"><div id="healthCards" class="health"></div><div id="storageBreakdown"></div></section>
    <section id="backupSettings" class="settings-section" data-settings-route="/settings/backups" data-page-title="Backups" data-page-description="Back up to another drive, and plan a private cloud copy." aria-labelledby="settingsPageHeading" hidden><div id="backupSettingsLocal"></div><div id="cloudBackupPlanner"></div></section>
    <section id="sources" class="settings-section" data-settings-route="/settings/sources" data-page-title="Sources" data-page-description="The AI tools whose history Pharos reads on this Mac. Pharos keeps them current on the Background Processing page." aria-labelledby="settingsPageHeading" hidden><div class="source-toolbar" id="sourceToolbar" aria-label="Source actions"><div id="sourceSetupAction"></div><button type="button" data-view-field="guide_open" onclick="pharosOpenDataGuide('sources',this)">Data Guide</button><a href="/settings/sync">Open Background Processing →</a></div><div class="source-summary" id="sourceSummary"></div><div class="source-grid" id="sourceGrid"></div><div id="claudeDownloadsWatchSection" class="layout-preference-card claude-downloads-watch" hidden></div></section>
    <section id="syncSettings" class="settings-section" data-settings-route="/settings/sync" data-page-title="Background Processing" data-page-description="Pharos keeps your library current on its own. Each kind of work runs as often as its cost allows: quick work every few minutes, heavier work hourly, the heaviest overnight." aria-labelledby="settingsPageHeading" hidden><div id="syncDashboard">
      <div id="backgroundProcessingWorkload"></div><div id="backgroundSchedule"></div>
      <div id="syncHistoryTables"></div>
      <div id="syncSourceTables"></div><div id="syncDiagnosticTables"></div>
    </div></section>
    <section id="mcpSettings" class="settings-section" data-settings-route="/settings/mcp" data-page-title="MCP" data-page-description="Control agent access to Pharos and connect your agent clients." aria-labelledby="settingsPageHeading" hidden><div id="mcpSettingsPage"></div></section>
    <section id="layoutPreferences" class="settings-section" data-settings-route="/settings/layout" data-page-title="Layout Preferences" data-page-description="Drag and drop to customize your macOS menu bar, header, and Quick Stats. Changes save automatically and follow your library between Macs." aria-labelledby="settingsPageHeading" hidden>
      <div id="menuBarPreferences" class="menu-bar-preferences layout-preference-card">
        <div class="optional-behavior"><div><h3 id="showMacMenuBarTitle">macOS menu bar</h3><p id="showMacMenuBarDescription" class="muted">Show Pharos in your Mac’s menu bar and choose which items appear in its menu.</p></div><div class="menu-bar-preferences-actions"><button id="menuBarExpand" data-view-key="menuBarPreferences" data-view-label="macOS menu bar" data-view-field="disclosures" class="menu-bar-expand" type="button" aria-expanded="false" aria-controls="menuBarOptions" hidden><span data-view-action>Expand</span><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-down"/></svg></button><button id="showMacMenuBar" class="toggle" type="button" role="switch" aria-labelledby="showMacMenuBarTitle" aria-describedby="showMacMenuBarDescription" aria-checked="true"></button></div></div>
        <div id="menuBarOptions" class="menu-bar-preferences-body" hidden>
          <p class="muted">Show a still lighthouse and status dot in your Mac’s menu bar. Green means idle, yellow means work is running, and red means Pharos needs attention.</p>
          <label>Usage period <select id="menuBarUsagePeriod"><option value="today">Today</option><option value="24h">Last 24 hours</option></select></label>
          <div class="settings-section-heading"><button id="menuBarReset" type="button">Reset to default</button></div>
          <p id="menuBarEditorHint" class="muted">Drag items and separators to arrange your menu. Click a handle for move and remove actions. Open Pharos, status, Settings, and Quit always stay available.</p>
          <div class="menu-bar-editor" aria-describedby="menuBarEditorHint">
            <section aria-labelledby="menuBarActiveHeading"><h3 id="menuBarActiveHeading">Your Mac menu</h3><div class="menu-bar-desktop"><ol id="menuBarActive" class="menu-bar-preview" aria-label="Mac menu items"></ol></div><p class="muted menu-bar-preview-note">Live library values. These settings apply to the Pharos Mac app.</p></section>
            <section class="menu-bar-toolbox-section" aria-labelledby="menuBarToolboxHeading"><h3 id="menuBarToolboxHeading">Toolbox</h3><p class="muted">Drag an item into your menu, or click to add it. Add as many separators as you need. Drop items here to remove them.</p><ul id="menuBarToolbox" class="menu-bar-toolbox" aria-label="Available Mac menu items"></ul></section>
          </div><div id="menuBarToolMenu" class="header-tool-menu menu-bar-tool-menu" popover aria-label="Mac menu item actions"></div><p id="menuBarEditorStatus" class="muted" role="status" aria-live="polite"></p>
        </div>
      </div>
      <div id="headerSettings" class="layout-preference-card">
        <div class="optional-behavior"><div><h3 id="headerLayoutTitle">Header</h3><p class="muted">Choose your header tools and their order. Smaller windows keep extra tools in More.</p></div><button id="headerExpand" class="menu-bar-expand" type="button" data-layout-disclosure data-view-key="headerSettings" data-view-label="Header" data-view-field="disclosures" aria-expanded="false" aria-controls="headerOptions" aria-label="Expand Header"><span data-view-action>Expand</span><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-down"/></svg></button></div>
        <div id="headerOptions" class="menu-bar-preferences-body" hidden>
          <div class="settings-section-heading"><button id="headerReset" class="button-with-icon" type="button">Reset to default</button></div>
          <p id="headerEditorHint" class="muted">Drag the handles to arrange your header. Click a handle for move and remove actions. Smaller windows keep extra tools in More. Settings always stays available.</p>
          <div class="header-editor" aria-describedby="headerEditorHint">
            <section aria-labelledby="headerActiveHeading"><h3 id="headerActiveHeading">Your header</h3><div class="header-preview"><div class="bar header-preview-bar"><div id="headerPreviewBrand" class="header-preview-brand" aria-hidden="true"></div><div id="headerPreviewTabs" class="header-preview-context tabs" aria-hidden="true" inert></div><ol id="headerActive" class="header-icons header-tool-list" aria-label="Header controls"></ol></div></div></section>
            <section class="header-toolbox-section" aria-labelledby="headerToolboxHeading"><h3 id="headerToolboxHeading">Toolbox</h3><p class="muted">Drag a button into your header, or click to add it. Drop buttons here to remove them.</p><ul id="headerToolbox" class="header-tool-list" aria-label="Available header tools"></ul></section>
          </div><div id="headerToolMenu" class="header-tool-menu" popover aria-label="Header control actions"></div><p id="headerEditorStatus" class="muted" role="status" aria-live="polite"></p>
        </div>
      </div>
      <div id="quickStatsSettings" class="layout-preference-card">
        <div class="optional-behavior"><div><h3 id="quickStatsLayoutTitle">Quick Stats</h3><p class="muted">Choose the metrics and time periods shown when you click the stats button in the header.</p></div><button id="quickStatsExpand" class="menu-bar-expand" type="button" data-layout-disclosure data-view-key="quickStatsSettings" data-view-label="Quick Stats" data-view-field="disclosures" aria-expanded="false" aria-controls="quickStatsOptions" aria-label="Expand Quick Stats"><span data-view-action>Expand</span><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-chevron-down"/></svg></button></div>
        <div id="quickStatsOptions" class="menu-bar-preferences-body" hidden>
          <div class="settings-section-heading"><button id="quickStatsReset" type="button">Reset to default</button></div>
          <p id="quickStatsEditorHint" class="muted">Drag rows up or down and columns left or right in the preview. Drop them in the toolbox to hide them, or drag them back to add them. Click a handle for actions; Alt+arrow keys move items and Delete removes them. Keep at least one row and one column.</p>
          <div id="quickStatsEditor" aria-describedby="quickStatsEditorHint"></div>
          <p id="quickStatsEditorStatus" class="muted" role="status" aria-live="polite"></p>
          <div class="optional-behavior"><div><h3 id="usageSummaryLinksTitle">Clickable Quick Stats</h3><p id="usageSummaryLinksDescription" class="muted">Supported values open their analysis for the displayed interval. Calendar periods use local time; weeks begin on Monday.</p></div><button id="usageSummaryLinks" class="toggle" type="button" role="switch" aria-labelledby="usageSummaryLinksTitle" aria-describedby="usageSummaryLinksDescription" aria-checked="false"></button></div>
        </div>
      </div>
    </section>
    <section id="askSettings" class="settings-section" data-settings-route="/settings/ask" data-page-title="Ask Pharos" data-page-description="Choose your CLI, model, effort, and AI preferences on this Mac." aria-labelledby="settingsPageHeading" hidden><div id="askSettingsPage"></div></section>
    <section id="projectColorsSettings" class="settings-section" data-settings-route="/settings/projects" data-page-title="Project colors" data-page-description="Choose consistent project colors for charts, cards, and dashboards." aria-labelledby="settingsPageHeading" hidden><div id="projectColorSettings"></div></section>
    <section id="modelColorsSettings" class="settings-section" data-settings-route="/settings/models" data-page-title="Model &amp; provider colors" data-page-description="Choose provider colors and coordinated model shades for charts, cards, and dashboards." aria-labelledby="settingsPageHeading" hidden><div id="modelColorSettings"></div></section>
    <section id="optionalBehaviors" class="settings-section" data-settings-route="/settings/preferences" data-page-title="App preferences" data-page-description="Choose highlight shortcuts, lighthouse behavior, activity preferences, and your feedback contact email." aria-labelledby="settingsPageHeading" hidden>
      <div class="optional-behavior"><div><h3 id="escapeClearsHighlightsTitle">Escape clears highlights</h3><p id="escapeClearsHighlightsDescription" class="muted">Press Escape to clear linked highlights when no dialog or editor is active. On by default.</p></div><button id="escapeClearsHighlights" class="toggle" type="button" role="switch" aria-labelledby="escapeClearsHighlightsTitle" aria-describedby="escapeClearsHighlightsDescription" aria-checked="true"></button></div>
      <div class="optional-behavior"><div><h3 id="uncertainRelatedWorkTitle">Show uncertain related work</h3><p id="uncertainRelatedWorkDescription" class="muted">Include dotted-line related work in workspace agent sessions, timelines, and conversations. These relationships are lower confidence. Off by default.</p></div><button id="uncertainRelatedWork" class="toggle" type="button" role="switch" aria-labelledby="uncertainRelatedWorkTitle" aria-describedby="uncertainRelatedWorkDescription" aria-checked="false"></button></div>
      <div class="optional-behavior"><div><h3 id="lighthouseSpinOnClickTitle">Lighthouse spins on click</h3><p id="lighthouseSpinOnClickDescription" class="muted">Turn the lighthouse beam once around when you click the door of the lighthouse in the menu bar.</p></div><button id="lighthouseSpinOnClick" class="toggle" type="button" role="switch" aria-labelledby="lighthouseSpinOnClickTitle" aria-describedby="lighthouseSpinOnClickDescription" aria-checked="false"></button></div>
      <div class="optional-behavior"><div><h3 id="lighthouseLoadingIndicatorTitle">Lighthouse loading indicator</h3><p id="lighthouseLoadingIndicatorDescription" class="muted">Turn the lighthouse beam while sources are indexed and while pages load. The drive badge pulses while the library is busy either way.</p></div><button id="lighthouseLoadingIndicator" class="toggle" type="button" role="switch" aria-labelledby="lighthouseLoadingIndicatorTitle" aria-describedby="lighthouseLoadingIndicatorDescription" aria-checked="false"></button></div>
      <div class="optional-behavior"><div><h3 id="pauseIndexingForQueriesTitle">Pause indexing during queries</h3><p id="pauseIndexingForQueriesDescription" class="muted">Give queries priority by pausing indexing and library maintenance while they run. Sustained browsing or agent queries can delay indexing. Off by default; changes apply without restarting.</p></div><button id="pauseIndexingForQueries" class="toggle" type="button" role="switch" aria-labelledby="pauseIndexingForQueriesTitle" aria-describedby="pauseIndexingForQueriesDescription" aria-checked="false"></button></div>
      <div class="optional-behavior"><div><h3 id="feedbackContactEmailTitle">Contact email for feedback</h3><p id="feedbackContactEmailDescription" class="muted">Prefilled in Send feedback so we can follow up. Leave it empty to send feedback without one. Changing it in the feedback form updates it here.</p></div><input id="feedbackContactEmail" type="email" maxlength="254" autocomplete="email" placeholder="you@example.com" aria-labelledby="feedbackContactEmailTitle" aria-describedby="feedbackContactEmailDescription"></div>
    </section>
    <section id="logsSettings" class="settings-section" data-settings-route="/settings/logs" data-page-title="Logs" data-page-description="Errors, slow requests, output and crash reports from this Mac, by source." aria-labelledby="settingsPageHeading" hidden><div id="logsPage"></div></section></div>
  </section>
  <section id="analyze" class="view" data-feedback-label="Analyze view"><div id="queryTableUsage"></div></section>
  <section id="findings" class="view" data-feedback-label="Findings view"><div id="findingsPage"></div></section>
  <section id="files" class="view" data-feedback-label="File history view"><div id="fileHistoryPage"></div></section>
  <section id="ask" class="view" data-feedback-label="Ask Pharos view"><div id="askPage"></div></section>
  <section id="detail" class="view"><button class="back" id="back"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-arrow-left"/></svg> Back to Search</button><div id="detailBody"></div></section>
</main>
<aside class="drawer" id="bookmarksDrawer" aria-label="Bookmarks" data-feedback-label="Bookmarks drawer"><header class="drawer-head"><h2>Bookmarks</h2><button id="bookmarksClose" class="drawer-close" type="button" aria-label="Close bookmarks" title="Close (Escape)"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-close"/></svg></button></header><div class="drawer-tools"><button id="bookmarkAdd" type="button"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-star"/></svg> Bookmark this page</button></div><div class="drawer-body" id="bookmarksList"></div></aside><div id="bookmarkPopover" class="bookmark-popover" popover="auto" role="dialog" aria-labelledby="bookmarkPopoverTitle" data-feedback-label="Bookmark popover"><form id="bookmarkPopoverForm"><h2 id="bookmarkPopoverTitle">Bookmark added</h2><label>Name<input id="bookmarkName" type="text" autocomplete="off" spellcheck="false"></label><div class="bookmark-popover-actions"><button id="bookmarkRemove" type="button">Remove</button><button type="submit" class="primary">Done</button></div></form></div>
<aside class="drawer" id="historyDrawer" aria-label="History" data-feedback-label="History drawer"><header class="drawer-head"><h2>History</h2><button id="historyClose" class="drawer-close" type="button" aria-label="Close history" title="Close (Escape)"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-close"/></svg></button></header><div class="drawer-tools"><input data-view-field="history_search" id="historyFilter" type="search" placeholder="Filter history…" aria-label="Filter history"><button id="historyClear" type="button">Clear</button></div><div class="drawer-body" id="historyList" data-view-field="history_collapsed"></div></aside>
<div class="feedback-highlight" id="feedbackHighlight" data-feedback-ui><span data-feedback-drag tabindex="0" role="button" aria-label="Move annotation target label" title="Drag to move the target label. Arrow keys move it when focused."></span></div>
<div id="feedbackPins" data-feedback-ui></div>
<aside class="feedback-toolbar" aria-label="Annotation toolbar" data-feedback-ui>
<div class="feedback-toolbar-controls" data-feedback-drag>
  <button class="feedback-drag-handle" type="button" aria-label="Move annotation toolbar" title="Drag to move. Arrow keys move when focused."><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-grip"/></svg></button>
  <button id="feedbackTarget" title="Click an element to annotate. Use Up and Down arrows to choose its parent or child." aria-pressed="false"><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-target"/></svg> Annotate</button>
  <button id="feedbackOpen" title="Show UI feedback" aria-expanded="false" aria-controls="feedbackPanel" hidden>Feedback <span class="feedback-count" id="feedbackCount">0</span></button>
  <button id="feedbackQuickCopy" type="button" title="Copy feedback" aria-label="Copy feedback" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-copy"/></svg></button>
</div>
<section class="feedback-panel" id="feedbackPanel" aria-label="UI feedback">
  <div class="feedback-list" id="feedbackList"></div>
  <div class="feedback-actions"><button id="feedbackClear">Clear</button><label class="feedback-copy-option"><input type="checkbox" id="feedbackClearOnCopy"> Clear on Copy</label><button class="primary" id="feedbackCopy">Copy feedback</button></div>
</section>
</aside>
<div class="feedback-composer-backdrop" id="feedbackComposer" data-feedback-ui>
  <section class="feedback-composer" role="dialog" aria-modal="true" aria-labelledby="feedbackComposerTitle">
    <div class="feedback-composer-heading" data-feedback-drag><button class="feedback-drag-handle" type="button" aria-label="Move annotation composer" title="Drag to move. Arrow keys move when focused."><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-grip"/></svg></button><h2 id="feedbackComposerTitle">Add UI feedback</h2></div><div class="feedback-selector" id="feedbackTargetDescription"></div>
    <div class="dictation-field"><textarea id="feedbackNote" placeholder="What should change? Include what you expected and why."></textarea><button type="button" id="feedbackDictate" class="dictation-button" aria-label="Dictate" title="Dictate" aria-pressed="false" hidden><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-mic"/></svg></button></div>
    <div class="feedback-attachment-options"><label><input type="checkbox" id="feedbackIncludeURL"> Include the URL captured for this annotation</label><label><input type="checkbox" id="feedbackIncludeText"> Include the element's text</label><button type="button" id="feedbackScreenshot">Take a screenshot of this element</button><button type="button" id="feedbackRemoveScreenshot" hidden>Remove screenshot</button><p id="feedbackScreenshotStatus" role="status"></p><img id="feedbackScreenshotPreview" alt="Screenshot of the annotated element" hidden style="max-width:100%;max-height:180px;object-fit:contain"></div>
    <div class="row"><button id="feedbackCancel">Cancel</button><button class="primary" id="feedbackAdd">Add annotation</button></div>
  </section>
</div>
<div class="feedback-toast" id="feedbackToast" role="status" data-feedback-ui data-feedback-drag><button class="feedback-drag-handle" type="button" aria-label="Move feedback status message" title="Drag to move. Arrow keys move when focused."><svg class="app-icon" viewBox="0 0 24 24" aria-hidden="true"><use href="#ph-icon-grip"/></svg></button><span id="feedbackToastMessage"></span></div>
<script src="/assets/header.js"></script>
<script src="/assets/quick-stats.js"></script>
<script src="/assets/event-spans.js"></script>
<script src="/assets/reader-model.js"></script>
<script src="/assets/storage-breakdown.js"></script>
<script>
const $=s=>document.querySelector(s), esc=s=>String(s??'');
// The page scrolls inside main, so its scrollbars start below the header.
const pageScroller=$('main');
function icon(name){const svg=document.createElementNS('http://www.w3.org/2000/svg','svg'),use=document.createElementNS('http://www.w3.org/2000/svg','use');svg.classList.add('app-icon');svg.setAttribute('viewBox','0 0 24 24');svg.setAttribute('aria-hidden','true');use.setAttribute('href','#ph-icon-'+name);svg.append(use);return svg}
function iconLabel(name,label){return[icon(name),document.createTextNode(' '+label)]}
// Unsent annotations, the open toolbar, page history, and the open navigation bar stay in this web view's storage: they describe this Mac's session, not the library.
// A combo button: one control of adjacent buttons that share their inner edges (styled by .combo-button). Each segment is {label, icon, ariaLabel, title, id, className, pressed, onClick}; giving `pressed` makes that segment a toggle, and an icon-only segment needs an ariaLabel. The group's buttons are group.buttons, in order.
function comboButton(segments,{label,className}={}){const group=node('div',className?`combo-button ${className}`:'combo-button');group.setAttribute('role','group');if(label)group.setAttribute('aria-label',label);group.buttons=segments.map(segment=>{const button=node('button',[segment.label?'':'combo-icon',segment.className].filter(Boolean).join(' '));button.type='button';if(segment.id)button.id=segment.id;if(segment.icon)button.append(icon(segment.icon));if(segment.label)button.append(segment.label);if(segment.ariaLabel)button.setAttribute('aria-label',segment.ariaLabel);if(segment.title)button.title=segment.title;if('pressed' in segment)button.setAttribute('aria-pressed',String(Boolean(segment.pressed)));if(segment.onClick)button.onclick=segment.onClick;return button});group.append(...group.buttons);return group}
const FEEDBACK_KEY='pharos-ui-feedback-v1',FEEDBACK_DRAFT_KEY='pharos-ui-feedback-draft-v1',FEEDBACK_CLEAR_KEY='pharos-ui-feedback-clear-on-copy';
let feedbackAnnotations=loadFeedback(),feedbackMode=false,feedbackTarget=null,feedbackTargetElement=null,feedbackHoverElement=null,feedbackHoverTrail=[],feedbackHoverIndex=0,feedbackToastTimer,feedbackEditing=null,feedbackCopying=false;
const FEEDBACK_LAYOUT_KEY='pharos-ui-feedback-layout-v1',feedbackDraggables=new Map();
const feedbackResizeObserver=new ResizeObserver(()=>requestAnimationFrame(restoreFeedbackLayout));
let feedbackDrag=null;
const feedbackLayout=(()=>{try{const value=JSON.parse(localStorage.getItem(FEEDBACK_LAYOUT_KEY)||'{}');return value&&typeof value==='object'&&!Array.isArray(value)?value:{}}catch{return{}}})();
function saveFeedbackLayout(){try{localStorage.setItem(FEEDBACK_LAYOUT_KEY,JSON.stringify(feedbackLayout))}catch{}}
// Panels use viewport coordinates; labels and pins keep an offset from their
// target. Moving feedback never changes the element recorded in the report.
function makeFeedbackDraggable(element,handle,key,anchor){
  handle.setAttribute('data-feedback-drag','');
  let suppressClick=false;
  const controller={element,restore,finish};feedbackDraggables.set(key,controller);feedbackResizeObserver.observe(element);
  function place(x,y,remember=false){
    const rect=element.getBoundingClientRect(),inset=element.classList.contains('feedback-pin')?2:8;
    x=Math.max(inset,Math.min(Math.max(inset,innerWidth-rect.width-inset),x));
    y=Math.max(inset,Math.min(Math.max(inset,innerHeight-rect.height-inset),y));
    element.classList.add('feedback-positioned');element.style.left=`${x}px`;element.style.top=`${y}px`;
    if(remember){const origin=anchor?.()||{x:0,y:0};feedbackLayout[key]={x:x-origin.x,y:y-origin.y};saveFeedbackLayout()}
  }
  function restore(){
    if(!element.getClientRects().length||feedbackDrag?.controller===controller)return;
    const saved=feedbackLayout[key],origin=anchor?.();
    if(saved&&Number.isFinite(saved.x)&&Number.isFinite(saved.y))place(saved.x+(origin?.x||0),saved.y+(origin?.y||0));
    else if(origin)place(origin.x,origin.y);
  }
  function finish(event){
    if(feedbackDrag?.controller!==controller||event&&event.pointerId!==feedbackDrag.pointerId)return;
    const {pointerId,moved}=feedbackDrag;feedbackDrag=null;document.body.classList.remove('feedback-dragging');
    if(handle.hasPointerCapture(pointerId))handle.releasePointerCapture(pointerId);
    if(moved){suppressClick=true;const rect=element.getBoundingClientRect();place(rect.left,rect.top,true)}
    if(element.id==='feedbackToast')feedbackToastTimer=setTimeout(()=>element.classList.remove('visible'),1800);
  }
  handle.addEventListener('pointerdown',event=>{
    if(event.button!==0||feedbackDrag||event.target.closest('button,input,textarea,select,a,[contenteditable]')&&!event.target.closest('.feedback-drag-handle')&&event.target!==handle)return;
    suppressClick=false;const rect=element.getBoundingClientRect();feedbackDrag={controller,pointerId:event.pointerId,x:event.clientX,y:event.clientY,left:rect.left,top:rect.top,moved:false};
    handle.setPointerCapture(event.pointerId);document.body.classList.add('feedback-dragging');
    if(element.id==='feedbackToast')clearTimeout(feedbackToastTimer);
    event.preventDefault();
  });
  handle.addEventListener('pointermove',event=>{
    if(feedbackDrag?.controller!==controller||event.pointerId!==feedbackDrag.pointerId)return;
    const dx=event.clientX-feedbackDrag.x,dy=event.clientY-feedbackDrag.y;
    if(!feedbackDrag.moved&&Math.hypot(dx,dy)<3)return;
    feedbackDrag.moved=true;place(feedbackDrag.left+dx,feedbackDrag.top+dy);event.preventDefault();
  });
  for(const type of ['pointerup','pointercancel','lostpointercapture'])handle.addEventListener(type,finish);
  handle.addEventListener('click',event=>{if(suppressClick){suppressClick=false;event.preventDefault();event.stopImmediatePropagation()}},true);
  handle.addEventListener('keydown',event=>{
    if(event.target!==handle&&!event.target.closest('.feedback-drag-handle'))return;
    const direction={ArrowLeft:[-1,0],ArrowRight:[1,0],ArrowUp:[0,-1],ArrowDown:[0,1]}[event.key];
    if(!direction)return;
    event.preventDefault();event.stopPropagation();const rect=element.getBoundingClientRect(),step=event.shiftKey?1:10;
    place(rect.left+direction[0]*step,rect.top+direction[1]*step,true);
  });
  restore();return controller;
}
function restoreFeedbackLayout(){for(const controller of feedbackDraggables.values())controller.restore()}
function feedbackPinKey(item,index){return'pin:'+(item.id||`${item.selector}:${index}`)}
function loadFeedback(){try{const value=JSON.parse(localStorage.getItem(FEEDBACK_KEY)||'[]');return Array.isArray(value)?value:[]}catch{return[]}}
function saveFeedback(){localStorage.setItem(FEEDBACK_KEY,JSON.stringify(feedbackAnnotations));const pins=new Set(feedbackAnnotations.map(feedbackPinKey));for(const key of Object.keys(feedbackLayout))if(key.startsWith('pin:')&&!pins.has(key))delete feedbackLayout[key];saveFeedbackLayout();renderFeedback()}
function loadFeedbackDraft(){try{const value=JSON.parse(localStorage.getItem(FEEDBACK_DRAFT_KEY)||'null');return value?.target&&typeof value.note==='string'?value:null}catch{return null}}
function saveFeedbackDraft(){if(!feedbackTarget){localStorage.removeItem(FEEDBACK_DRAFT_KEY);return}localStorage.setItem(FEEDBACK_DRAFT_KEY,JSON.stringify({target:feedbackTarget,note:$('#feedbackNote').value}))}
function feedbackEscape(value){return window.CSS&&CSS.escape?CSS.escape(value):value.replace(/[^a-zA-Z0-9_-]/g,x=>'\\'+x)}
function feedbackSelector(element){
  if(element.id)return'#'+feedbackEscape(element.id);
  const parts=[];let current=element;
  while(current&&current!==document.body&&parts.length<6){
    if(current.id){parts.unshift('#'+feedbackEscape(current.id));break}
    let part=current.tagName.toLowerCase();
    const classes=[...current.classList].filter(x=>!['active','visible','open'].includes(x)&&!x.startsWith('feedback-')).slice(0,2);
    if(classes.length)part+='.'+classes.map(feedbackEscape).join('.');
    const parent=current.parentElement;
    if(parent){const siblings=[...parent.children].filter(x=>x.tagName===current.tagName);if(siblings.length>1)part+=`:nth-of-type(${siblings.indexOf(current)+1})`}
    parts.unshift(part);current=parent
  }
  return parts.join(' > ')
}
function feedbackView(){const view=document.querySelector('.view.active');return view?{id:view.id,title:view.querySelector('h1')?.textContent||view.id}:{id:'unknown',title:'Unknown view'}}
function feedbackText(element){const value=('value'in element&&element.value)||element.getAttribute('aria-label')||element.innerText||element.textContent||'';return value.replace(/\s+/g,' ').trim().slice(0,240)}
function feedbackRecord(element){
  const rect=element.getBoundingClientRect(),style=getComputedStyle(element),view=feedbackView(),chosen=window.getSelection(),selection=chosen?.anchorNode&&element.contains(chosen.anchorNode)?chosen.toString().trim().slice(0,240):'';
  return{id:crypto.randomUUID?crypto.randomUUID():`${Date.now()}-${Math.random()}`,note:'',selector:feedbackSelector(element),label:element.dataset.feedbackLabel||element.getAttribute('aria-label')||element.tagName.toLowerCase(),tag:element.tagName.toLowerCase(),text:feedbackText(element),selected_text:selection,view,route:location.pathname,url:window.uiFeedback?.safeURL(location.href)||location.origin+location.pathname,bounds:{x:Math.round(rect.x),y:Math.round(rect.y),width:Math.round(rect.width),height:Math.round(rect.height)},viewport:{width:innerWidth,height:innerHeight,device_pixel_ratio:devicePixelRatio},styles:{display:style.display,position:style.position,color:style.color,background:style.backgroundColor,font_size:style.fontSize,font_weight:style.fontWeight,padding:style.padding,margin:style.margin,gap:style.gap,border_radius:style.borderRadius},source:'internal/archive/assets/ui.py (APP_HTML)',created_at:new Date().toISOString()}
}
function isFeedbackUI(element){return element instanceof Element&&Boolean(element.closest('[data-feedback-ui]'))}
const FEEDBACK_ENABLED_KEY='pharos-feedback-enabled';
function setFeedbackEnabled(enabled){document.body.classList.toggle('feedback-enabled',enabled);const button=$('#feedbackToggle'),label=enabled?'Hide annotation toolbar':'Show annotation toolbar';button.setAttribute('aria-pressed',String(enabled));button.setAttribute('aria-label',label);button.title=label;if(!enabled){if(feedbackMode)setFeedbackMode(false);setFeedbackExpanded(false)}else restoreFeedbackLayout();try{localStorage.setItem(FEEDBACK_ENABLED_KEY,String(enabled))}catch{}}
function setFeedbackExpanded(expanded){const open=expanded&&feedbackAnnotations.length>0;$('.feedback-toolbar').classList.toggle('expanded',open);$('#feedbackPanel').classList.toggle('open',open);$('#feedbackOpen').setAttribute('aria-expanded',String(open));$('#feedbackOpen').classList.toggle('active',open);$('#feedbackOpen').title=open?'Hide UI feedback':'Show UI feedback';restoreFeedbackLayout()}
function setFeedbackMode(enabled){feedbackMode=enabled;feedbackHoverElement=null;feedbackHoverTrail=[];feedbackHoverIndex=0;document.body.classList.toggle('feedback-targeting',enabled);$('#feedbackTarget').classList.toggle('active',enabled);$('#feedbackTarget').setAttribute('aria-pressed',String(enabled));$('#feedbackTarget').replaceChildren(...iconLabel(enabled?'close':'target',enabled?'Cancel':'Annotate'));if(!enabled)$('#feedbackHighlight').classList.remove('visible')}
function feedbackTargetTrail(element){const trail=[];let current=element;while(current instanceof Element&&current!==document.body&&current!==document.documentElement&&!isFeedbackUI(current)){trail.push(current);current=current.parentElement}return trail}
function highlightFeedbackTarget(element){feedbackHoverElement=element;const rect=element.getBoundingClientRect(),box=$('#feedbackHighlight'),level=feedbackHoverIndex?`parent ${feedbackHoverIndex} of ${feedbackHoverTrail.length-1} · `:'';box.style.left=`${rect.left}px`;box.style.top=`${rect.top}px`;box.style.width=`${rect.width}px`;box.style.height=`${rect.height}px`;box.querySelector('span').textContent=`↑ parent · ↓ child · ${level}${element.dataset.feedbackLabel||element.tagName.toLowerCase()} · ${feedbackSelector(element)}`;box.classList.add('visible');feedbackDraggables.get('target-label')?.restore()}
function onFeedbackHover(event){if(!feedbackMode||feedbackDrag||isFeedbackUI(event.target))return;feedbackHoverTrail=feedbackTargetTrail(event.target);feedbackHoverIndex=0;if(feedbackHoverTrail.length)highlightFeedbackTarget(feedbackHoverTrail[0])}
function stepFeedbackTarget(direction){if(!feedbackMode||!feedbackHoverTrail.length)return false;const next=Math.max(0,Math.min(feedbackHoverTrail.length-1,feedbackHoverIndex+direction));if(next===feedbackHoverIndex)return false;feedbackHoverIndex=next;highlightFeedbackTarget(feedbackHoverTrail[next]);return true}
function onFeedbackClick(event){
  if(!feedbackMode||isFeedbackUI(event.target))return;
  event.preventDefault();event.stopImmediatePropagation();const element=feedbackHoverElement?.contains(event.target)?feedbackHoverElement:event.target;feedbackTargetElement=element;feedbackTarget=feedbackRecord(element);setFeedbackMode(false);
  openFeedbackComposer(feedbackTarget,'')
}
function openFeedbackComposer(target,note){feedbackTarget=target;$('#feedbackTargetDescription').textContent=`${target.label} · ${target.selector}`;$('#feedbackNote').value=note;$('#feedbackIncludeURL').checked=target.include_url===true;$('#feedbackIncludeText').checked=target.include_text===true;$('#feedbackScreenshotPreview').hidden=true;$('#feedbackScreenshotStatus').textContent=target.has_screenshot?'Screenshot attached.':'';$('#feedbackRemoveScreenshot').hidden=!target.has_screenshot;$('#feedbackComposer').classList.add('open');restoreFeedbackLayout();saveFeedbackDraft();setTimeout(()=>$('#feedbackNote').focus(),0)}
function closeFeedbackComposer(){if(feedbackTarget&&!feedbackAnnotations.some(item=>item.id===feedbackTarget.id))window.uiFeedback?.removeScreenshot(feedbackTarget.id);feedbackTarget=null;feedbackTargetElement=null;localStorage.removeItem(FEEDBACK_DRAFT_KEY);$('#feedbackComposer').classList.remove('open')}
for(const [id,key]of [['feedbackIncludeURL','include_url'],['feedbackIncludeText','include_text']])$('#'+id).onchange=event=>{if(feedbackTarget){feedbackTarget[key]=event.target.checked;saveFeedbackDraft()}};
$('#feedbackScreenshot').onclick=async()=>{if(!feedbackTarget||!window.uiFeedback)return;const target=feedbackTarget,element=feedbackTargetElement||findFeedbackElement(target.selector),button=$('#feedbackScreenshot');button.disabled=true;$('#feedbackAdd').disabled=true;$('#feedbackScreenshotStatus').textContent='Taking screenshot…';try{if(!element?.isConnected)throw new Error('The original element is no longer available. Annotate it again to take a screenshot.');if(window.uiFeedback.safeURL(location.href)!==target.url)throw new Error('Return to the original page, or annotate this element again.');const image=await window.uiFeedback.capture(element,target.id);if(feedbackTarget!==target){window.uiFeedback.removeScreenshot(target.id);return}target.has_screenshot=true;saveFeedbackDraft();$('#feedbackScreenshotPreview').src=`data:${image.mime};base64,${image.data}`;$('#feedbackScreenshotPreview').hidden=false;$('#feedbackRemoveScreenshot').hidden=false;$('#feedbackScreenshotStatus').textContent='Screenshot attached. Review it before sending.'}catch(error){$('#feedbackScreenshotStatus').textContent=String(error.message||error)}finally{button.disabled=false;$('#feedbackAdd').disabled=false}};
$('#feedbackRemoveScreenshot').onclick=()=>{if(!feedbackTarget)return;window.uiFeedback?.removeScreenshot(feedbackTarget.id);feedbackTarget.has_screenshot=false;saveFeedbackDraft();$('#feedbackScreenshotPreview').hidden=true;$('#feedbackRemoveScreenshot').hidden=true;$('#feedbackScreenshotStatus').textContent='Screenshot removed.'};
function addFeedback(){if($('#feedbackAdd').disabled)return;const note=$('#feedbackNote').value.trim();if(!feedbackTarget||!note){$('#feedbackNote').focus();return}feedbackAnnotations.push({...feedbackTarget,note});saveFeedback();closeFeedbackComposer();setFeedbackExpanded(true);feedbackToast('Annotation added')}
function findFeedbackElement(selector){try{return document.querySelector(selector)}catch{return null}}
function renderFeedbackPins(){
  const root=$('#feedbackPins'),modal=document.querySelector('dialog[open]'),visible=new Set();
  feedbackAnnotations.forEach((item,index)=>{
    const element=findFeedbackElement(item.selector);if(!element||element.closest('.view:not(.active)')||modal&&!modal.contains(element))return;
    const rect=element.getBoundingClientRect();if(!rect.width&&!rect.height)return;
    const key=feedbackPinKey(item,index);visible.add(key);let controller=feedbackDraggables.get(key);
    if(!controller){const pin=node('button','feedback-pin');pin.type='button';root.append(pin);controller=makeFeedbackDraggable(pin,pin,key,()=>{const bounds=(findFeedbackElement(item.selector)||element).getBoundingClientRect();return{x:bounds.right-12,y:bounds.top-12}})}
    controller.element.textContent=String(index+1);controller.element.setAttribute('aria-label',`Go to annotation ${index+1}`);controller.element.title='Click to show this annotation. Drag to move the marker; arrow keys move it when focused.';controller.element.onclick=()=>{setFeedbackExpanded(true);$('#feedbackList').children[index]?.scrollIntoView({block:'nearest'});jumpToFeedback(item)};controller.restore();
  });
  for(const [key,controller] of feedbackDraggables)if(key.startsWith('pin:')&&!visible.has(key)){controller.finish();feedbackResizeObserver.unobserve(controller.element);controller.element.remove();feedbackDraggables.delete(key)}
}
function jumpToFeedback(item){
  if(feedbackMode)setFeedbackMode(false);
  const element=findFeedbackElement(item.selector),modal=[...document.querySelectorAll('dialog[open]')].at(-1);
  if(!element||!element.getClientRects().length||element.closest('.view:not(.active)')||modal&&!modal.contains(element)){feedbackToast('This element is unavailable in the current view.');return}
  const reduced=matchMedia('(prefers-reduced-motion: reduce)').matches;
  element.scrollIntoView({behavior:reduced?'instant':'smooth',block:'center',inline:'nearest'});
  if(!reduced){const animation=element.animate([{outline:'3px solid #7c3aed',outlineOffset:'4px'},{outline:'3px solid transparent',outlineOffset:'4px'}],{duration:1200});animation.onfinish=renderFeedbackPins}
  renderFeedbackPins()
}
function editFeedback(item,index){feedbackEditing=item;renderFeedback();const field=$('#feedbackList').children[index]?.querySelector('textarea');field?.focus();field?.setSelectionRange(field.value.length,field.value.length)}
function updateFeedbackCopyButtons(){
  const ready=feedbackAnnotations.length>0,clears=ready&&$('#feedbackClearOnCopy').checked,label=clears?'Copy and clear feedback':'Copy feedback';
  for(const button of [$('#feedbackCopy'),$('#feedbackQuickCopy')]){button.disabled=!ready||feedbackCopying;button.classList.toggle('feedback-copy-ready',ready);button.classList.toggle('feedback-copy-clears',clears);button.title=label;button.setAttribute('aria-label',label)}
}
function renderFeedback(){
  const count=feedbackAnnotations.length;$('#feedbackCount').textContent=String(count);updateFeedbackCopyButtons();$('#feedbackClear').disabled=!count;$('#feedbackOpen').hidden=!count;$('#feedbackQuickCopy').hidden=!count;if(!count)setFeedbackExpanded(false);if(!feedbackAnnotations.includes(feedbackEditing))feedbackEditing=null;
  const root=$('#feedbackList');root.replaceChildren();
  feedbackAnnotations.forEach((item,index)=>{
    const card=node('article','feedback-item'),top=node('div','feedback-item-top'),number=node('button','feedback-number',String(index+1)),note=node('div','feedback-note'),edit=node('button'),remove=node('button');
    number.type='button';number.setAttribute('aria-label',`Go to annotation ${index+1}`);number.title='Scroll to annotated element';number.onclick=()=>jumpToFeedback(item);
    edit.type='button';edit.append(icon('edit'));edit.setAttribute('aria-label',`Edit annotation ${index+1}`);edit.title='Edit annotation';edit.onclick=()=>editFeedback(item,index);
    remove.type='button';remove.append(icon('close'));remove.setAttribute('aria-label',`Remove annotation ${index+1}`);remove.title='Remove annotation';remove.onclick=()=>{window.uiFeedback?.removeScreenshot(item.id);feedbackAnnotations.splice(index,1);saveFeedback()};
    if(feedbackEditing===item){
      const form=node('form','feedback-edit'),field=node('textarea'),actions=node('div','feedback-edit-actions'),cancel=node('button','','Cancel'),save=node('button','primary','Save');field.value=item.note;field.setAttribute('aria-label',`Edit annotation ${index+1} feedback`);cancel.type='button';save.type='submit';
      const discard=()=>{feedbackEditing=null;renderFeedback();root.children[index]?.querySelector(`[aria-label="Edit annotation ${index+1}"]`)?.focus()};cancel.onclick=discard;
      field.oninput=()=>save.disabled=!field.value.trim();form.onsubmit=event=>{event.preventDefault();const value=field.value.trim();if(!value)return;item.note=value;feedbackEditing=null;saveFeedback();feedbackToast('Annotation updated')};
      field.onkeydown=event=>{if(event.key==='Escape'){event.preventDefault();event.stopPropagation();discard()}else if((event.metaKey||event.ctrlKey)&&event.key==='Enter'){event.preventDefault();form.requestSubmit()}};
      actions.append(cancel,save);form.append(field,actions);note.append(form);edit.hidden=true;
    }else note.textContent=item.note;
    top.append(number,note,edit,remove);card.append(top,node('div','feedback-selector',`${item.view.title} · ${item.selector}`));root.append(card)
  });
  renderFeedbackPins();restoreFeedbackLayout()
}
function feedbackMarkdownInline(value){return String(value||'').replace(/`/g,'\\`').replace(/\s+/g,' ').trim()}
function feedbackMarkdown(annotations=feedbackAnnotations,screenshots={}){
  const lines=['# Pharos UI feedback','',`Generated: ${new Date().toISOString()}`,'','Please address each annotation below and verify the result in the rendered UI.',''];
  annotations.forEach((item,index)=>{lines.push(`## Annotation ${index+1}`,'',`- Feedback: ${JSON.stringify(item.note)}`,`- View: ${item.view.title} (\`#${feedbackMarkdownInline(item.view.id)}\`)`,`- Element: ${item.label} (\`<${item.tag}>\`)`,`- Selector: \`${feedbackMarkdownInline(item.selector)}\``,`- Bounds: x=${item.bounds.x}, y=${item.bounds.y}, width=${item.bounds.width}, height=${item.bounds.height}`,`- Viewport: ${item.viewport.width} × ${item.viewport.height} @ ${item.viewport.device_pixel_ratio}x`,`- Source hint: \`${item.source}\``);if(screenshots[item.id])lines.push(`- Screenshot: [Open screenshot](<${screenshots[item.id]}>)`);if(item.text)lines.push(`- Element text: ${JSON.stringify(item.text)}`);if(item.selected_text)lines.push(`- Selected text: ${JSON.stringify(item.selected_text)}`);lines.push(`- Computed styles: \`${feedbackMarkdownInline(JSON.stringify(item.styles))}\``,'')});return lines.join('\n')
}
async function copyFeedbackText(text){return window.pharosHost.clipboard.writeText(text)}
function clearFeedback(){if(!feedbackAnnotations.length)return;for(const item of feedbackAnnotations)window.uiFeedback?.removeScreenshot(item.id);feedbackAnnotations=[];saveFeedback();feedbackToast('Feedback cleared')}
async function copyFeedback(){
  if(!feedbackAnnotations.length||feedbackCopying)return;
  const originals=feedbackAnnotations.slice(),annotations=originals.map(item=>({...item})),count=annotations.length,clearAfterCopy=$('#feedbackClearOnCopy').checked,screenshots={};
  feedbackCopying=true;updateFeedbackCopyButtons();
  try{
    for(const item of annotations)if(item.has_screenshot){
      if(!window.uiFeedback?.exportScreenshot)throw new Error('Screenshot export is unavailable. Reload Pharos and try again.');
      screenshots[item.id]=await window.uiFeedback.exportScreenshot(item.id)
    }
    await copyFeedbackText(feedbackMarkdown(annotations,screenshots));
    if(clearAfterCopy){for(const item of originals)window.uiFeedback?.removeScreenshot(item.id);feedbackAnnotations=feedbackAnnotations.filter(item=>!originals.includes(item));saveFeedback()}
    feedbackToast(`Copied ${count} annotation${count===1?'':'s'}${clearAfterCopy?' and cleared':''}`)
  }catch(error){feedbackToast(`Could not copy feedback. ${error.message||'Check clipboard access and try again.'}`)}
  finally{feedbackCopying=false;updateFeedbackCopyButtons()}
}
function feedbackToast(message){const toast=$('#feedbackToast');$('#feedbackToastMessage').textContent=message;toast.classList.add('visible');restoreFeedbackLayout();clearTimeout(feedbackToastTimer);feedbackToastTimer=setTimeout(()=>toast.classList.remove('visible'),1800)}
addEventListener('pharos:session-save-error',()=>feedbackToast('Could not save feedback. Keep Pharos open and retry before quitting.'));
makeFeedbackDraggable($('.feedback-toolbar'),$('.feedback-toolbar-controls'),'toolbar');
makeFeedbackDraggable($('.feedback-composer'),$('.feedback-composer-heading'),'composer');
makeFeedbackDraggable($('#feedbackToast'),$('#feedbackToast'),'toast');
makeFeedbackDraggable($('#feedbackHighlight span'),$('#feedbackHighlight span'),'target-label',()=>{const rect=$('#feedbackHighlight').getBoundingClientRect();return{x:rect.left-2,y:rect.top-$('#feedbackHighlight span').offsetHeight-4}});
$('#feedbackClearOnCopy').checked=pharosPrefs.get(FEEDBACK_CLEAR_KEY,false)===true;$('#feedbackClearOnCopy').onchange=event=>{pharosPrefs.set(FEEDBACK_CLEAR_KEY,event.target.checked);updateFeedbackCopyButtons()};$('#feedbackTarget').onclick=()=>setFeedbackMode(!feedbackMode);$('#feedbackToggle').onclick=()=>setFeedbackEnabled(!document.body.classList.contains('feedback-enabled'));{let enabled=false;try{enabled=localStorage.getItem(FEEDBACK_ENABLED_KEY)==='true'}catch{}setFeedbackEnabled(enabled)}$('#feedbackOpen').onclick=()=>setFeedbackExpanded(!$('#feedbackPanel').classList.contains('open'));$('#feedbackCancel').onclick=closeFeedbackComposer;$('#feedbackAdd').onclick=addFeedback;$('#feedbackCopy').onclick=copyFeedback;$('#feedbackQuickCopy').onclick=copyFeedback;$('#feedbackClear').onclick=clearFeedback;window.pharosDictation.attach({button:$('#feedbackDictate'),field:$('#feedbackNote'),onError:feedbackToast});$('#feedbackNote').oninput=saveFeedbackDraft;$('#feedbackNote').onkeydown=e=>{if((e.metaKey||e.ctrlKey)&&e.key==='Enter')addFeedback()};
const feedbackDraft=loadFeedbackDraft();if(feedbackDraft)openFeedbackComposer(feedbackDraft.target,feedbackDraft.note);
// Feedback controls sit above dialogs. Keep their pointer events from reaching
// document-level outside-click handlers that would dismiss the dialog.
for(const element of document.querySelectorAll('[data-feedback-ui]'))for(const type of ['pointerdown','mousedown','click'])element.addEventListener(type,event=>event.stopPropagation());
// Targeting starts before the click. A backdrop may otherwise dismiss on press.
for(const type of ['pointerdown','mousedown'])document.addEventListener(type,event=>{if(feedbackMode&&!isFeedbackUI(event.target))event.stopImmediatePropagation()},true);
// Native modal dialogs live in the browser's top layer, above every z-index.
// Keep the feedback UI inside the active dialog so it remains visible and usable.
const feedbackOverlay=[...document.querySelectorAll('#feedbackHighlight,#feedbackPins,.feedback-toolbar,#feedbackComposer,#feedbackToast')];
function placeFeedbackOverModal(){const modal=[...document.querySelectorAll('dialog[open]')].at(-1),host=modal||document.body;for(const element of feedbackOverlay)if(element.parentElement!==host)host.append(element);renderFeedbackPins();restoreFeedbackLayout()}
new MutationObserver(mutations=>{if(mutations.some(mutation=>mutation.type==='attributes'?mutation.target instanceof HTMLDialogElement:[...mutation.addedNodes,...mutation.removedNodes].some(node=>node instanceof HTMLDialogElement)))placeFeedbackOverModal()}).observe(document.body,{subtree:true,childList:true,attributes:true,attributeFilter:['open']});
document.addEventListener('pointerover',onFeedbackHover,true);document.addEventListener('click',onFeedbackClick,true);document.addEventListener('keydown',event=>{if(feedbackMode&&(event.key==='ArrowUp'||event.key==='ArrowDown')){event.preventDefault();stepFeedbackTarget(event.key==='ArrowUp'?1:-1);return}if(event.key==='Escape'){if($('#feedbackComposer').classList.contains('open'))closeFeedbackComposer();else if(feedbackMode)setFeedbackMode(false);else setFeedbackExpanded(false)}if(event.altKey&&event.key.toLowerCase()==='a'){event.preventDefault();setFeedbackMode(!feedbackMode)}});addEventListener('resize',()=>{renderFeedbackPins();restoreFeedbackLayout()});pageScroller.addEventListener('scroll',renderFeedbackPins,{passive:true});
async function api(path,opts={}){const r=await fetch(path,{...opts,headers:{'Content-Type':'application/json',...(opts.headers||{})}});if(!r.ok){const error=new Error((await r.json().catch(()=>({error:r.statusText}))).error);error.status=r.status;throw error}return r.json()}
const ROUTES=new Set(['home','cards','library','analyze','findings','mcp','settings','ask','files']);
const themeOrder=['system','light','dark'];
function applyTheme(theme,save=true){if(theme==='system')document.documentElement.removeAttribute('data-theme');else document.documentElement.dataset.theme=theme;const button=$('#themeToggle'),next=themeOrder[(themeOrder.indexOf(theme)+1)%themeOrder.length],label=theme[0].toUpperCase()+theme.slice(1),nextLabel=next[0].toUpperCase()+next.slice(1);button.replaceChildren(icon(theme==='dark'?'moon':theme==='light'?'sun':'theme-system'));button.setAttribute('aria-label',`Color theme: ${label}. Activate to switch to ${nextLabel}.`);button.title=`Theme: ${label}. Click to use ${nextLabel}.`;if(save)pharosPrefs.set('pharos-theme',theme)}
// React owns the shared composer and preserves its draft between openings.
function openQuickSearch(){window.dispatchEvent(new Event('pharos:open-search'))}

function syncNavigatorGo(){$('#navigatorGo').hidden=$('#navigatorURI').value.trim()===location.href}
function syncNavigator(){if(document.activeElement!==$('#navigatorURI'))$('#navigatorURI').value=location.href;syncNavigatorGo()}
function setRoute(path){if(location.pathname!==path){const params=new URLSearchParams(location.search);if(window.pharosView){window.pharosView.retainPage(params,path)}if(location.pathname==='/findings'){for(const key of ['finding','tab','list','repo'])params.delete(key);if(params.get('view')==='settings')params.delete('view')}if(location.pathname==='/cards'&&path!=='/cards'){if(params.get('cards')==='library')params.delete('cards');for(const key of ['card_search','card_data','card_topic','card_kind','card_sort'])params.delete(key)}if(path==='/')for(const key of ['q_library','search','kind','fuzzy','case','separators','view'])params.delete(key);history.pushState(null,'',path+(params.size?'?'+params:''));syncNavigator();recordVisit()}}
// The panels are the route registry for the shell and the shared React layout.
const SETTINGS_PAGES=Object.fromEntries(Array.from(document.querySelectorAll('[data-settings-route]'),panel=>[panel.dataset.settingsRoute,panel.id]));
// The feedback form (web/src/uifeedback/pharos-client.tsx) prefills and updates the same preference.
const FEEDBACK_CONTACT_KEY='pharos-feedback-contact-email';
$('#feedbackContactEmail').oninput=event=>{const email=event.target.value.trim();if(email)pharosPrefs.set(FEEDBACK_CONTACT_KEY,email);else pharosPrefs.remove(FEEDBACK_CONTACT_KEY)};
function showSettingsPage(){const path=location.pathname in SETTINGS_PAGES?location.pathname:'/settings';for(const [route,id] of Object.entries(SETTINGS_PAGES))$('#'+id).hidden=route!==path;if(path==='/settings')loadHealth();if(path==='/settings/sources')loadSources();if(path==='/settings/preferences')$('#feedbackContactEmail').value=pharosPrefs.get(FEEDBACK_CONTACT_KEY,'');document.querySelector('main').scrollTop=0}
function show(name,fromLocation=false){if(name!=='detail')workspaceLoad?.abort();if(['sources','activity','health'].includes(name))name='settings';document.querySelectorAll('.view').forEach(x=>x.classList.toggle('active',x.id===name));document.querySelectorAll('button[data-view]').forEach(x=>x.classList.toggle('active',x.dataset.view===(name==='detail'?'library':name)));if(!fromLocation&&ROUTES.has(name))setRoute(name==='home'?'/':'/'+name);if(name==='settings')showSettingsPage();if(name==='home'||name==='cards')document.querySelector('main').scrollTop=0;if(name==='analyze')window.pharosQueryTables?.refreshDashboards('usage');renderFeedbackPins();window.dispatchEvent(new CustomEvent('pharos:view',{detail:name}))}
function isLegacyLibraryURL(url){return url.pathname==='/'&&(url.searchParams.has('q_library')||url.searchParams.has('search'))}
function routeFromLocation(){const url=new URL(location.href);if(url.pathname==='/settings/header'){url.pathname='/settings/layout';url.searchParams.set('disclosures',JSON.stringify({...window.pharosView?.read('disclosures',{}),headerSettings:true}));url.searchParams.set('reveal','section:headerSettings');history.replaceState(null,'',url)}if(url.pathname==='/mcp'){url.pathname='/analyze';url.searchParams.set('analyze_topic','mcp');url.searchParams.set('analyze_data','mcp_calls');url.searchParams.set('analyze_mode',url.searchParams.has('q_mcp_calls')?'query':'dashboards');history.replaceState(null,'',url)}if(url.pathname==='/tools'){url.pathname='/analyze';url.searchParams.set('tools_view',url.searchParams.get('tools_view')||'calls');history.replaceState(null,'',url)}if(isLegacyLibraryURL(url)){url.pathname='/library';history.replaceState(null,'',url)}if(['/','/home'].includes(url.pathname)&&(url.searchParams.get('cards')==='library'||['card_search','card_data','card_topic','card_kind','card_sort'].some(key=>url.searchParams.get(key)))){url.pathname='/cards';history.replaceState(null,'',url)}let path=location.pathname,name=path.slice(1);if(path.startsWith('/work/')&&path.length>6){let id;try{const parts=path.slice(6).split('/');if(parts.length!==1&&!(parts.length===3&&parts[1]==='conversation'))throw new Error('Invalid work URI');id=decodeURIComponent(parts[0])}catch{feedbackToast('Invalid work URI');return}void detail(id,true).catch(()=>{show('library',true);feedbackToast('Work unavailable')})}else{if(path==='/settings/optimization'){path='/findings';name='findings';const params=new URLSearchParams(location.search);params.set('view','settings');history.replaceState(null,'',path+'?'+params)}if(['sources','activity','health'].includes(name)){path=name==='sources'?'/settings/sources':'/settings';history.replaceState(null,'',path+location.search)}if(path in SETTINGS_PAGES)name='settings';show(ROUTES.has(name)?name:path==='/'?'home':'library',true);recordVisit()}window.dispatchEvent(new Event('pharos:route'));syncNavigator()}
document.querySelectorAll('button[data-view]').forEach(b=>b.onclick=()=>{if(b.dataset.view==='analyze'){openHistoryEntry('/analyze');return}if(b.dataset.view==='findings'&&location.pathname==='/findings'&&location.search){openHistoryEntry('/findings');return}show(b.dataset.view)});$('#back').onclick=()=>show('library');$('.brand').onclick=event=>{if(event.metaKey||event.ctrlKey||event.shiftKey||event.button)return;event.preventDefault();show('home')};
let currentTheme=pharosPrefs.get('pharos-theme','dark');if(!themeOrder.includes(currentTheme))currentTheme='dark';applyTheme(currentTheme,false);$('#themeToggle').onclick=()=>{currentTheme=themeOrder[(themeOrder.indexOf(currentTheme)+1)%themeOrder.length];applyTheme(currentTheme)};
$('#searchToggle').onclick=openQuickSearch;
$('#askPharosToggle').onclick=()=>window.dispatchEvent(new Event('pharos:ask-open'));
document.addEventListener('keydown',event=>{if((event.metaKey||event.ctrlKey)&&!event.altKey&&!event.shiftKey&&event.key.toLowerCase()==='k'){event.preventDefault();openQuickSearch()}});

$('#navigator').onsubmit=event=>{event.preventDefault();const input=$('#navigatorURI');let target;try{target=new URL(input.value.trim(),location.href)}catch{$('#navigatorError').textContent='Invalid URI';return}const name=target.pathname.slice(1);if(target.origin!==location.origin||!(target.pathname==='/'||ROUTES.has(name)||target.pathname in SETTINGS_PAGES||target.pathname==='/settings/optimization'||target.pathname==='/settings/header'||target.pathname.startsWith('/work/'))){$('#navigatorError').textContent='Enter a Pharos URI on this service';return}$('#navigatorError').textContent='';history.pushState(null,'',target.pathname+target.search);input.blur();routeFromLocation()};
$('#navigatorURI').addEventListener('input',syncNavigatorGo);
$('#navigatorCopy').onclick=async()=>{try{await copyFeedbackText(location.href);feedbackToast('URI copied')}catch{feedbackToast('Could not copy URI')}};
addEventListener('popstate',routeFromLocation);
addEventListener('pharos:uri-changed',syncNavigator);
const HISTORY_KEY='pharos-history-v1',HISTORY_LIMIT=1000,VIEW_TITLES={'':'Home',home:'Home',cards:'Card Library',library:'Search',analyze:'Analyze',findings:'Findings',mcp:'MCP',ask:'Ask Pharos',files:'File history',...Object.fromEntries(Object.entries(SETTINGS_PAGES).map(([path,id])=>[path.slice(1),`Settings · ${$('#'+id).dataset.pageTitle}`]))},VIEW_DATASETS={library:['library'],analyze:['usage','mcp_calls','conversation_activity','writing','writing_messages','tools','tool_calls','skill_usages','parallelism','tl1_attempts'],tools:['tools','tool_calls'],mcp:['mcp_calls']};
function loadHistory(){try{const value=JSON.parse(localStorage.getItem(HISTORY_KEY)||'[]');return Array.isArray(value)?value.filter(entry=>entry&&typeof entry.url==='string'&&typeof entry.title==='string'&&Number.isFinite(entry.at)):[]}catch{return[]}}
function saveHistory(entries,explicit=false){try{localStorage.setItem(HISTORY_KEY,serializePageHistory(entries,{limit:HISTORY_LIMIT}));if(explicit&&window.pharosSession)void window.pharosSession.preserve(HISTORY_KEY).catch(()=>feedbackToast('Could not update page history. Keep Pharos open and retry.'))}catch{}}
function updatePrivateVisits(path,body){const write=()=>api('/api/ask/'+path,{method:'POST',body:JSON.stringify(body)});return typeof askWriteVisit==='function'?askWriteVisit(write):write()}
function historyQuerySummary(token){if(window.pharosDescribeVisitQuery)return window.pharosDescribeVisitQuery(token);try{const state=JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(token.replace(/-/g,'+').replace(/_/g,'/')),c=>c.charCodeAt(0)))),label=field=>String(field).replace(/_/g,' '),clause=c=>c.value===undefined||c.value===null||c.value===''?`${label(c.field)} ${c.op}`:`${label(c.field)} ${c.op} ${Array.isArray(c.value)?c.value.join(', '):c.value}`,parts=[];const where=(state.w||[]).map(term=>Array.isArray(term.any)?'('+term.any.map(branch=>Array.isArray(branch.all)?'('+branch.all.map(clause).join(' and ')+')':clause(branch)).join(' or ')+')':clause(term));if(where.length)parts.push(where.join(', '));const groups=[...new Set((state.g||[]).flatMap(aggregation=>aggregation.groupBy||[]))];if(groups.length)parts.push('by '+groups.map(label).join(', '));return parts.join('; ')}catch{return''}}
function historyTitle(url,page){const target=new URL(url,location.origin),name=target.pathname.slice(1)||(isLegacyLibraryURL(target)?'library':'home'),params=target.searchParams,details=[];if(target.pathname.startsWith('/work/'))return page||'Workspace';if(name==='library'&&params.get('search'))details.push(`“${params.get('search')}”`);if(name==='library'&&params.get('view')==='conversation')details.push('conversations');if(name==='findings'){if(params.get('finding'))details.push(page||'finding');else if(params.get('view')==='settings')details.push('settings');else if(params.get('view')==='table')details.push('table');else if(params.get('list'))details.push({watching:'watching',wins:'wins',dismissed:'dismissed'}[params.get('list')]||'open')}for(const dataset of VIEW_DATASETS[name]||[]){const token=params.get('q_'+dataset);if(token){const summary=historyQuerySummary(token);if(summary)details.push(summary)}}return[VIEW_TITLES[name]||name,...details].join(' · ')}
let visitTimer=0;
function recordVisit(replace=false,page=''){clearTimeout(visitTimer);const path=location.pathname;const url=path+location.search,entries=loadHistory(),latest=entries[0],same=latest&&(replace?latest.url.split('?')[0]===path:latest.url===url),entry={url,at:Date.now(),page:page||(same&&latest.page)||''};entry.title=historyTitle(url,entry.page);if(same)entries[0]=entry;else entries.unshift(entry);saveHistory(entries);window.dispatchEvent(new CustomEvent('pharos:visit',{detail:{entry,replaced:same?latest:null}}));if($('#historyDrawer').classList.contains('open'))renderHistory();syncBookmarkState()}
function historyPath(url){try{return decodeURIComponent(url.split('?')[0])}catch{return url.split('?')[0]}}
function renderHistory(){const list=$('#historyList'),all=loadHistory(),filter=$('#historyFilter').value.trim().toLowerCase(),entries=all.filter(entry=>!filter||entry.title.toLowerCase().includes(filter)||historyPath(entry.url).toLowerCase().includes(filter));$('#historyClear').disabled=!all.length;list.replaceChildren();if(!entries.length){list.append(empty(filter?'No visits match this filter.':'No history yet. Pages you visit in Pharos appear here. Stored only on this Mac.'));return}const today=new Date(),yesterday=new Date(),counts={},bookmarked=new Set(loadBookmarks().map(item=>bookmarkPageURL(item.url)));yesterday.setDate(today.getDate()-1);for(const entry of entries){const key=new Date(entry.at).toDateString();counts[key]=(counts[key]||0)+1}let day='',group;for(const entry of entries){const date=new Date(entry.at),key=date.toDateString();if(key!==day){day=key;const heading=node('h2','history-day'),toggle=node('button','history-day-toggle'),collapsed=(window.pharosView?.read('history_collapsed',[])||[]).includes(key),count=counts[key];group=node('ol','history-group');group.id='history-day-'+date.getTime();group.hidden=collapsed;toggle.type='button';toggle.setAttribute('aria-expanded',String(!collapsed));toggle.setAttribute('aria-controls',group.id);toggle.append(node('span','history-chevron',icon('chevron-down')),node('span','',key===today.toDateString()?'Today':key===yesterday.toDateString()?'Yesterday':date.toLocaleDateString(undefined,{weekday:'long',month:'long',day:'numeric',year:'numeric'})));if(collapsed)toggle.append(node('span','history-count',`· ${count} ${count===1?'page':'pages'}`));toggle.onclick=()=>{const collapsed=new Set(window.pharosView?.read('history_collapsed',[])||[]);collapsed.has(key)?collapsed.delete(key):collapsed.add(key);window.pharosView?.write('history_collapsed',[...collapsed].sort());renderHistory();$(`#historyList [aria-controls="${group.id}"]`)?.focus()};heading.append(toggle);list.append(heading,group)}const item=node('li','history-entry'),time=node('time','history-time',date.toLocaleTimeString(undefined,{hour:'numeric',minute:'2-digit'})),link=node('a','history-link'),remove=node('button','history-remove',icon('close')),star=node('button','history-star'),marked=bookmarked.has(bookmarkPageURL(entry.url));star.type='button';star.replaceChildren(icon('star'));star.classList.toggle('bookmarked',marked);star.title=marked?'Remove bookmark':'Bookmark this page';star.setAttribute('aria-label',marked?`Remove bookmark for ${entry.title}`:`Bookmark ${entry.title}`);star.setAttribute('aria-pressed',String(marked));star.onclick=()=>{toggleHistoryBookmark(entry);renderHistory();$(`#historyList .history-star[data-key="${entry.at}"]`)?.focus()};star.dataset.key=entry.at;time.dateTime=date.toISOString();time.title=date.toLocaleString();link.href=entry.url;link.title=entry.title;link.append(node('span','history-title',entry.title),node('span','history-url',historyPath(entry.url)));link.onclick=event=>{event.preventDefault();openHistoryEntry(entry.url)};remove.type='button';remove.title='Remove from history';remove.setAttribute('aria-label',`Remove ${entry.title} from history`);remove.onclick=()=>{saveHistory(loadHistory().filter(other=>other.url!==entry.url||other.at!==entry.at),true);void updatePrivateVisits('remove-visit',{url:entry.url,at:entry.at}).catch(()=>{});renderHistory()};item.append(time,link,star,remove);group.append(item)}}
function toggleHistoryBookmark(entry){const items=loadBookmarks();if(items.some(item=>bookmarkPageURL(item.url)===bookmarkPageURL(entry.url))){saveBookmarks(items.filter(item=>bookmarkPageURL(item.url)!==bookmarkPageURL(entry.url)));feedbackToast('Bookmark removed')}else{saveBookmarks([...items,{id:crypto.randomUUID?crypto.randomUUID():`${Date.now()}-${Math.random()}`,url:bookmarkPageURL(entry.url),title:entry.title,created_at:Date.now()}]);feedbackToast('Bookmarked')}if($('#bookmarksDrawer').classList.contains('open'))renderBookmarks()}
// View-state writes arrive in bursts (each disclosure writes two fields; Open
// turns toggles every turn), and each visit rewrites the history and posts it
// to Ask. Record the burst's final URL once.
addEventListener('pharos:uri-changed',()=>{clearTimeout(visitTimer);visitTimer=setTimeout(()=>recordVisit(true),150)});
$('#historyFilter').oninput=()=>{window.pharosView?.write('history_search',$('#historyFilter').value);renderHistory()};$('#historyClear').onclick=()=>{saveHistory([],true);void updatePrivateVisits('clear-visits',{}).catch(()=>{});renderHistory();feedbackToast('History cleared')};
addEventListener('storage',event=>{if(event.key===HISTORY_KEY&&$('#historyDrawer').classList.contains('open'))renderHistory();if(event.key===BOOKMARKS_KEY){syncBookmarkState();if($('#bookmarksDrawer').classList.contains('open'))renderBookmarks()}});
const BOOKMARKS_KEY='pharos-bookmarks-v1',DRAWERS={historyDrawer:'#historyToggle',bookmarksDrawer:'#bookmarksToggle'};
function loadBookmarks(){const value=pharosPrefs.get(BOOKMARKS_KEY,[]);return Array.isArray(value)?value.filter(item=>item&&typeof item.id==='string'&&typeof item.url==='string'&&typeof item.title==='string').map(item=>({...item,url:bookmarkPageURL(item.url)})):[]}
function saveBookmarks(items){pharosPrefs.set(BOOKMARKS_KEY,items);syncBookmarkState();window.dispatchEvent(new Event('pharos:bookmarks'))}
function currentPageURL(){return bookmarkPageURL()}
function syncBookmarkState(){const marked=loadBookmarks().some(item=>bookmarkPageURL(item.url)===currentPageURL()),add=$('#bookmarkAdd'),toggle=$('#bookmarksToggle');add.replaceChildren(...iconLabel('star',marked?'This page is bookmarked':'Bookmark this page'));add.disabled=marked;toggle.title=marked?'Bookmarks (this page is bookmarked)':'Bookmarks';const star=$('#bookmarkStar'),label=marked?'Edit bookmark':'Bookmark this page';star.classList.toggle('bookmarked',marked);star.setAttribute('aria-label',label);star.title=label}
function openHistoryEntry(url){if(new URL(url,location.href).pathname.startsWith('/conversation/')){location.assign(url);return}history.pushState(null,'',url);routeFromLocation()}
async function createBookmark(){const promoted=await window.pharosQueryTables?.createLink?.(),parsed=new URL(promoted?.url||location.href),url=bookmarkPageURL(parsed.href),visit=loadHistory().find(entry=>entry.url===url),item={id:crypto.randomUUID?crypto.randomUUID():`${Date.now()}-${Math.random()}`,url,title:visit?.title||historyTitle(url),created_at:Date.now(),query:promoted?.query,dataset:promoted?.dataset,savedAt:Date.now()};saveBookmarks([...loadBookmarks(),item]);return item}
async function addBookmark(){try{renderBookmarks((await createBookmark()).id)}catch(error){feedbackToast(`Bookmark failed: ${error.message}`)}}
let bookmarkPopoverId=null;
async function openBookmarkPopover(){try{const existing=loadBookmarks().find(item=>bookmarkPageURL(item.url)===currentPageURL()),item=existing||await createBookmark(),popover=$('#bookmarkPopover'),rect=$('#bookmarkStar').getBoundingClientRect();bookmarkPopoverId=item.id;$('#bookmarkPopoverTitle').textContent=existing?'Edit bookmark':'Bookmark added';$('#bookmarkName').value=item.title;popover.style.top=rect.bottom+6+'px';popover.style.right=Math.max(8,innerWidth-rect.right)+'px';popover.showPopover();$('#bookmarkName').focus();$('#bookmarkName').select();if($('#bookmarksDrawer').classList.contains('open'))renderBookmarks()}catch(error){feedbackToast(`Bookmark failed: ${error.message}`)}}
function commitBookmarkName(save){const id=bookmarkPopoverId,title=$('#bookmarkName').value.trim();bookmarkPopoverId=null;if(save&&id&&title)saveBookmarks(loadBookmarks().map(item=>item.id===id?{...item,title}:item));if($('#bookmarksDrawer').classList.contains('open'))renderBookmarks()}
function closeBookmarkPopover(save){commitBookmarkName(save);if($('#bookmarkPopover').matches(':popover-open'))$('#bookmarkPopover').hidePopover()}
function moveBookmark(id,to){const items=loadBookmarks(),from=items.findIndex(item=>item.id===id);if(from<0)return;to=Math.max(0,Math.min(items.length-1,to));if(to===from)return;const [item]=items.splice(from,1);items.splice(to,0,item);saveBookmarks(items);renderBookmarks();$(`#bookmarksList li[data-id="${CSS.escape(id)}"] .bookmark-link`)?.focus()}
function renameBookmark(id){const row=$(`#bookmarksList li[data-id="${CSS.escape(id)}"]`),item=loadBookmarks().find(other=>other.id===id);if(!row||!item)return;const link=row.querySelector('.bookmark-link'),input=node('input','bookmark-rename');input.value=item.title;input.setAttribute('aria-label','Bookmark name');let done=false;const finish=save=>{if(done)return;done=true;const title=input.value.trim();if(save&&title&&title!==item.title)saveBookmarks(loadBookmarks().map(other=>other.id===id?{...other,title}:other));renderBookmarks();$(`#bookmarksList li[data-id="${CSS.escape(id)}"] .bookmark-link`)?.focus()};input.onkeydown=event=>{event.stopPropagation();if(event.key==='Enter'){event.preventDefault();finish(true)}else if(event.key==='Escape'){event.preventDefault();finish(false)}};input.onblur=()=>finish(true);link.replaceWith(input);row.draggable=false;input.focus();input.select()}
function renderBookmarks(renameId){const list=$('#bookmarksList'),items=loadBookmarks();list.replaceChildren();syncBookmarkState();if(!items.length){list.append(empty('No bookmarks yet. Open a page and choose Bookmark this page.'));return}const ol=node('ol','bookmark-list');let dragId=null;items.forEach((item,index)=>{const row=node('li','bookmark-entry'),handle=node('span','bookmark-handle',icon('grip')),link=node('a','bookmark-link'),edit=node('button','bookmark-edit',icon('edit')),remove=node('button','bookmark-remove',icon('close'));row.dataset.id=item.id;row.draggable=true;handle.setAttribute('aria-hidden','true');handle.title='Drag to reorder';link.href=item.url;link.title='Alt+↑/↓ to reorder';const caption=node('span','history-title',item.title);caption.dataset.overflowTitle='';link.append(caption,node('span','history-url',historyPath(item.url)));link.onclick=event=>{event.preventDefault();openHistoryEntry(item.url)};edit.type=remove.type='button';edit.title='Rename';edit.setAttribute('aria-label',`Rename ${item.title}`);edit.onclick=()=>renameBookmark(item.id);remove.title='Remove bookmark';remove.setAttribute('aria-label',`Remove ${item.title}`);remove.onclick=()=>{saveBookmarks(loadBookmarks().filter(other=>other.id!==item.id));renderBookmarks();feedbackToast('Bookmark removed')};
    row.onkeydown=event=>{if(event.altKey&&(event.key==='ArrowUp'||event.key==='ArrowDown')){event.preventDefault();moveBookmark(item.id,index+(event.key==='ArrowUp'?-1:1))}else if(event.key==='F2'){event.preventDefault();renameBookmark(item.id)}};
    row.ondragstart=event=>{dragId=item.id;event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('text/plain',item.url);row.classList.add('dragging')};row.ondragend=()=>{dragId=null;ol.querySelectorAll('.bookmark-entry').forEach(other=>other.classList.remove('dragging','drop-before','drop-after'))};
    row.ondragover=event=>{if(!dragId||dragId===item.id)return;event.preventDefault();const after=event.clientY>row.getBoundingClientRect().top+row.offsetHeight/2;row.classList.toggle('drop-before',!after);row.classList.toggle('drop-after',after)};row.ondragleave=()=>row.classList.remove('drop-before','drop-after');
    row.ondrop=event=>{if(!dragId||dragId===item.id)return;event.preventDefault();const after=row.classList.contains('drop-after'),from=items.findIndex(other=>other.id===dragId);let to=index+(after?1:0);if(from<to)to--;moveBookmark(dragId,to)};
    row.append(handle,link,edit,remove);ol.append(row)});list.append(ol);if(renameId)renameBookmark(renameId)}
let restoringViewOverlay=false;
function setDrawer(name){if(!restoringViewOverlay)window.pharosView?.write('overlay',name==='historyDrawer'?'history':name==='bookmarksDrawer'?'bookmarks':'none');const previous=document.querySelector('.drawer.open'),returnTo=previous?.contains(document.activeElement)?$(DRAWERS[previous.id]):null;document.documentElement.style.setProperty('--drawer-top',$('body>header').getBoundingClientRect().bottom+'px');for(const [id,toggle] of Object.entries(DRAWERS)){const open=id===name;$('#'+id).classList.toggle('open',open);$(toggle).setAttribute('aria-expanded',String(open));$(toggle).classList.toggle('active',open)}if(name==='historyDrawer'){renderHistory();if(!restoringViewOverlay)$('#historyFilter').focus()}if(name==='bookmarksDrawer'){renderBookmarks();if(!restoringViewOverlay){$('#bookmarkAdd').disabled?$('#bookmarksList .bookmark-link')?.focus():$('#bookmarkAdd').focus()}}if(!restoringViewOverlay&&!name&&returnTo)(window.headerToolAnchor?.(returnTo)??returnTo).focus()}
function toggleDrawer(name){setDrawer($('#'+name).classList.contains('open')?'':name)}
$('#historyToggle').onclick=()=>toggleDrawer('historyDrawer');$('#bookmarksToggle').onclick=()=>toggleDrawer('bookmarksDrawer');$('#historyClose').onclick=$('#bookmarksClose').onclick=()=>setDrawer('');$('#bookmarkAdd').onclick=addBookmark;$('#bookmarkStar').onclick=openBookmarkPopover;$('#bookmarkPopoverForm').onsubmit=event=>{event.preventDefault();closeBookmarkPopover(true);$('#bookmarkStar').focus()};$('#bookmarkPopover').addEventListener('beforetoggle',event=>{if(event.newState==='closed'&&bookmarkPopoverId)commitBookmarkName(true)});$('#bookmarkName').onkeydown=event=>{if(event.key==='Escape'){event.preventDefault();closeBookmarkPopover(false);$('#bookmarkStar').focus()}};$('#bookmarkRemove').onclick=()=>{const id=bookmarkPopoverId;bookmarkPopoverId=null;saveBookmarks(loadBookmarks().filter(item=>item.id!==id));closeBookmarkPopover(false);$('#bookmarkStar').focus();feedbackToast('Bookmark removed')};
document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!event.defaultPrevented&&!$('#bookmarkPopover').matches(':popover-open')&&document.querySelector('.drawer.open')&&!$('#feedbackComposer').classList.contains('open'))setDrawer('')});
addEventListener('resize',()=>{if(document.querySelector('.drawer.open'))document.documentElement.style.setProperty('--drawer-top',$('body>header').getBoundingClientRect().bottom+'px')});
syncBookmarkState();
const NAV_OPEN_KEY='pharos-nav-open';
// Sticky table headers sit flush under the app header, whose height changes with the navigator bar and wrapping.
function syncConversationRailTop(){document.documentElement.style.setProperty('--conversation-rail-top',Math.max(72,Math.ceil($('body>header').getBoundingClientRect().bottom+3))+'px')}
addEventListener('resize',syncConversationRailTop);
function setNavOpen(open){$('#navigator').classList.toggle('open',open);syncConversationRailTop();dispatchEvent(new Event('resize'));const toggle=$('#navigatorToggle');toggle.setAttribute('aria-expanded',String(open));toggle.title=open?'Hide navigation bar':'Show navigation bar';try{localStorage.setItem(NAV_OPEN_KEY,String(open))}catch{}if(open)syncNavigator();else if(document.querySelector('.drawer.open'))setDrawer('');if(document.querySelector('.drawer.open'))setDrawer(document.querySelector('.drawer.open').id)}
$('#navigatorToggle').onclick=()=>{const open=!$('#navigator').classList.contains('open');setNavOpen(open);if(open){$('#navigatorURI').focus();$('#navigatorURI').select()}};
// Track this window's session independently of whether its arrows are shown.
const syncNavButtons=initHeaderHistory(syncNavigator);
$('#navBack').onclick=$('#headerBackOnly').onclick=()=>history.back();$('#navForward').onclick=()=>history.forward();
document.addEventListener('keydown',event=>{if(!(event.metaKey||event.ctrlKey)||event.altKey||event.shiftKey||(event.key!=='['&&event.key!==']'))return;const target=event.target;if(target.closest?.('input,textarea,select,[contenteditable="true"]'))return;event.preventDefault();(event.key==='['?$('#navBack'):$('#navForward')).click()});
{let open=false;try{open=localStorage.getItem(NAV_OPEN_KEY)==='true'}catch{}setNavOpen(open)}
syncNavButtons();
const headerControls=initHeaderControls({
  navigator:()=>setNavOpen(false),
  annotations:()=>setFeedbackEnabled(false),
  summary:()=>{if(!$('#usageSummary').hidden)setUsageSummaryOpen(false)},
  search:()=>{if($('#searchDialog').open)$('#searchDialog').close()},
  library:()=>window.pharosLibrary?.closePanel?.()
});
function node(tag,cls,text){const n=document.createElement(tag);if(cls)n.className=cls;if(text instanceof Node)n.append(text);else if(text!==undefined)n.textContent=text;return n}
function empty(text){return node('div','empty',text)}
async function search(){const f=new FormData($('#search')),p=new URLSearchParams();for(const [k,v]of f)if(v)p.set(k,v);const data=await api('/api/search?'+p),root=$('#results');root.replaceChildren();if(!data.items.length){root.append(empty('No matching workspaces. Check library status for source coverage.'));return}data.items.forEach(w=>{const c=node('article','card');c.onclick=()=>detail(w.id);c.append(node('h2','',w.title),node('div','meta',`${(w.source_aliases||[w.source_kind]).join(' + ')} · ${w.repository_name||'Repository not identified in source'} · ${w.activity_at?new Date(w.activity_at).toLocaleDateString():'Unknown date'}`));if(w.purpose)c.append(node('p','',w.purpose));if(w.outcome)c.append(node('p','muted',`Outcome: ${w.outcome}`));const chips=node('div','chips');chips.append(node('span','chip',`${w.conversation_count} conversation${w.conversation_count===1?'':'s'}`),node('span','chip',`${w.changed_file_count} changed file${w.changed_file_count===1?'':'s'}`),node('span','chip',w.preservation_completeness));for(const pr of workPullRequests(w)){const label=pr.title?`${pr.title} · #${pr.number}`:`PR #${pr.number}`;const link=node(pr.url?'a':'span','chip',label);if(pr.url){link.href=pr.url;link.target='_blank';link.rel='noopener noreferrer';link.onclick=event=>event.stopPropagation()}chips.append(link)}if(w.main_merge_commit){const link=mainIntegrationLink(w);link.onclick=event=>event.stopPropagation();chips.append(link)}c.append(chips);root.append(c)})}
if($('#search'))$('#search').onsubmit=e=>{e.preventDefault();search()};
// toLocaleString builds a formatter per call; transcripts label thousands of events.
let timeFormat=null;
function timeLabel(value){if(!value)return'Time unavailable';const date=new Date(value);return Number.isNaN(date.getTime())?value:(timeFormat??=new Intl.DateTimeFormat([], {dateStyle:'medium',timeStyle:'short'})).format(date)}
function relativeAgeLabel(value){const label=relativeAge(value);if(!label)return null;const date=new Date(value),time=node('time','relative-age',label);time.dateTime=date.toISOString();time.dataset.relativeTime=String(date.getTime());time.title=date.toLocaleString([], {dateStyle:'medium',timeStyle:'long'});return time}
function refreshRelativeAges(){if(document.hidden)return;const now=Date.now();document.querySelectorAll('time[data-relative-time]').forEach(time=>{const label=relativeAge(Number(time.dataset.relativeTime),now);if(time.textContent!==label)time.textContent=label})}
setInterval(refreshRelativeAges,60000);
document.addEventListener('visibilitychange',refreshRelativeAges);

// The conversation reader renders with the model in reader-model.js, which
// loads before this script: parsed bodies (messageJSON), normalized events
// (conversationMessages), token reports and context samples, tool payloads
// and results (conversationGraph), the Show filter's categories, Find, harness
// XML blocks (xmlSegments) and Markdown (markdownTree).
function jsonShape(value){if(Array.isArray(value))return`Array · ${value.length} item${value.length===1?'':'s'}`;if(value&&typeof value==='object')return`Object · ${Object.keys(value).length} field${Object.keys(value).length===1?'':'s'}`;return typeof value}
function structuredSummary(value,label){if(!value||typeof value!=='object'||Array.isArray(value))return`${label?label+' · ':''}${jsonShape(value)}`;const blocks=Array.isArray(value.message?.content)?value.message.content:Array.isArray(value.content)?value.content:[],block=blocks[0];if(block?.type==='tool_use')return`Tool call · ${block.name||'Unknown tool'}`;if(block?.type==='tool_result')return`Tool result${block.is_error?' · error':''}`;if(block?.type==='text')return`${value.message?.role==='user'?'User':'Agent'} message`;if(value.type==='result')return`Run result${value.subtype?' · '+value.subtype:''}`;if(value.type==='error')return'Run error';if(value.type==='system')return`System event${value.subtype?' · '+value.subtype:''}`;if(value.tool||value.name)return`Tool call · ${value.tool||value.name}`;return`${label?label+' · ':''}${jsonShape(value)}`}
function structuredPresentation(value){if(!value||typeof value!=='object'||Array.isArray(value))return null;const blocks=Array.isArray(value.message?.content)?value.message.content:Array.isArray(value.content)?value.content:[],block=blocks[0];if(block?.type==='tool_use')return{summary:`Tool call · ${block.name||'Unknown tool'}`,body:block.input||{}};if(block?.type==='tool_result')return{summary:`Tool result${block.is_error?' · error':''}`,body:{content:block.content,is_error:Boolean(block.is_error),...(value.tool_use_result!==undefined?{details:value.tool_use_result}:{})}};if(value.tool||value.name)return{summary:`Tool call · ${value.tool||value.name}`,body:value.input??value.arguments??value};return null}
function jsonTree(value,label,summaryOverride='',path=(label||'value')+':$'){if(value===null||typeof value!=='object'){const row=node('div','json-row');row.append(node('span','json-key',label||'value'),node('pre','json-value',typeof value==='string'?value:JSON.stringify(value)));return row}const details=node('details','json-node'),summary=node('summary','',summaryOverride||structuredSummary(value,label)),body=node('div','json-tree');details.dataset.jsonPath=path;details.append(summary,body);details.ontoggle=()=>{if(!details.open||details.dataset.rendered)return;details.dataset.rendered='true';const entries=Array.isArray(value)?value.map((item,index)=>[String(index),item]):Object.entries(value);entries.forEach(([key,item])=>body.append(jsonTree(item,key,'',path+'['+JSON.stringify(key)+']')))};return details}
function contentView(text){const structured=parsedJSON(text);if(structured!==null){const readable=commonEventText(structured);if(readable){const group=node('div','unwrapped-event');group.append(node('div','json-value',readable));const raw=node('details','raw-event'),summary=node('summary','','Raw event');raw.append(summary,jsonTree(structured,'JSON'));group.append(raw);return group}const presentation=structuredPresentation(structured);return presentation?jsonTree(presentation.body,'',presentation.summary):jsonTree(structured,'JSON')}const chunks=typeof text==='string'?text.split(/\n\n+/):[];if(chunks.length>1&&chunks.every(chunk=>parsedJSON(chunk)!==null)){const group=node('div','json-tree');chunks.forEach((chunk,index)=>group.append(jsonTree(parsedJSON(chunk),`JSON ${index+1}`)));return group}return node('div','json-value',text)}
function messageSearch(message){return`${message.role} ${message.kind} ${message.text}`.toLowerCase()}
function toolIdentity(message){const value=messageJSON(message);return value&&typeof value==='object'&&!Array.isArray(value)?String(value.tool||value.name||''):''}
function humanize(value){return String(value||'').replace(/^mcp__[^_]+__/,'').replace(/[_-]+/g,' ').replace(/\b\w/g,letter=>letter.toUpperCase())}
function tokenLevel(total){return total>=100000?'high':total>=10000?'medium':'low'}
// Continuous sea → indigo → magenta → coral scale, log-spaced from 1K to 1M tokens.
function tokenHeat(total){const t=Math.min(1,Math.max(0,Math.log10(Math.max(total,1)/1000)/3))*3,segment=Math.min(2,Math.floor(t));return `color-mix(in oklch,var(--heat-${segment}),var(--heat-${segment+1}) ${((t-segment)*100).toFixed(1)}%)`}
function compactionLabel(trigger){return trigger==='manual'?'Manual Compaction':trigger==='auto'?'Auto Compaction':'Compaction'}
const tokenNumberFormat=new Intl.NumberFormat('en',{notation:'compact',maximumFractionDigits:1});
function tokenCount(total){return tokenNumberFormat.format(total)}
function tokenText(total){return tokenCount(total)+' tokens'}
// Fixed-width trailing column so row content and Details stay put regardless of badge width.
function tokenColumn(target){let column=target.querySelector(':scope > .token-column');if(!column){column=node('span','token-column');target.append(column)}return column}
function attachTokenBadge(target,messages,column=false){
  if(column)target=tokenColumn(target);
  const reports=messages.map(message=>message?.tokenReport).filter(Boolean),known=reports.filter(report=>report.total!==null);
  if(known.length){const ledger=node('span','token-ledger');ledger.hidden=true;ledger.dataset.tokens=String(known.reduce((sum,r)=>sum+r.total,0));target.append(ledger)}
  const observed=reports.filter(r=>r.observedMinimum>0);
  if(observed.length){const badge=node('span','spend-badge','≥'+tokenCount(Math.max(...observed.map(r=>r.observedMinimum)))+' reported');badge.title='Harness-reported child usage minimum. May cover only the last request; full session usage and input/cache/output breakdown were not recorded. It may overlap aggregate usage and is not added to accounting or API cost.';target.append(badge)}
  const samples=reports.map(r=>r.contextSample).filter(Boolean);
  if(samples.length){const holder=node('span','context-evidence');holder.contextSamples=samples;target.append(holder);
    const values=samples.filter(s=>s.size!==null),latest=values.at(-1);
    if(latest){const delta=contextDelta(values);
      const label=delta===null?tokenCount(latest.size)+' base':(delta>=0?'+':'−')+tokenCount(Math.abs(delta));
      const badge=node('span','context-badge token-badge token-'+tokenLevel(Math.abs(delta??latest.size)),label+(latest.estimated?' (est.)':'')+(values.length>1?` · ${values.length} steps`:''));
      badge.style.setProperty('--heat-color',tokenHeat(Math.abs(delta??latest.size)));badge.title=`Latest input context: ${latest.size.toLocaleString()} tokens. ${delta===null?'First retained sample; addition unknown.':'Net context change between reported requests, not an exact token count for this tool or file.'} Cached input is included. Negative changes can reflect trimming or context replacement.`;holder.append(badge)}
    for(const trigger of new Set(samples.filter(s=>s.compact).map(s=>s.trigger))){const badge=node('span','compaction-badge'+(trigger?' '+trigger+'-compaction':''));badge.dataset.label=compactionLabel(trigger);badge.setAttribute('aria-label',badge.dataset.label);holder.append(badge)}
  }
  const aggregates=known.filter(r=>['run','session','task'].includes(r.scope));
  if(aggregates.length){const badge=node('span','spend-badge',tokenCount(aggregates.reduce((n,r)=>n+(r.reportedTotal??r.total),0))+' reported');badge.title='Aggregate consumption, including repeated input. Not context size; overlaps request-level accounting.\n'+aggregates.map(r=>[r.label,r.attributed!==undefined?'Already attributed: '+r.attributed.toLocaleString()+'; additional unattributed: '+r.total.toLocaleString():null,r.conflict?'Reported aggregate is lower than retained request usage; request evidence retained':null,r.breakdown?.partial?'Partial usage: some categories were not reported':null].filter(Boolean).join('\n')).join('\n');target.append(badge)}
}
function searchText(message){return message.searchText??=messageSearch(message)}
// The service shortens long outputs and source records (text_clipped,
// raw_clipped); the reader fetches the original when it shows one.
const originalRequests=new Map();
function loadOriginal(message){
  if(!message?.id||!(message.text_clipped||message.raw_clipped))return Promise.resolve(message);
  if(!originalRequests.has(message.id))originalRequests.set(message.id,api('/api/messages/'+encodeURIComponent(message.id)).finally(()=>setTimeout(()=>originalRequests.delete(message.id))));
  return originalRequests.get(message.id).then(original=>{
    // Shortened text is never split into blocks, so it normalizes to one message.
    if(message.text_clipped)message.text=normalizeMessages([{...message,text:original.text,raw_text:original.raw_text}])[0]?.text??original.text;
    message.raw_text=original.raw_text||original.text;message.text_clipped=message.raw_clipped=false;delete message.searchText;delete message.findableText;return message});
}
function clippedBytes(messages){return messages.filter(message=>message?.text_clipped).reduce((total,message)=>total+(Number(message.text_bytes)||0),0)}
function rawDisclosure(message,label='Raw event'){const details=node('details','raw-event'),pre=node('pre','raw-json');details.append(node('summary','',label),pre);let shown=false;const fill=()=>{pre.textContent=JSON.stringify(messageJSON(message,true)??message.raw_text??message.text,null,2)};const show=()=>{if(shown)return;shown=true;fill();if(message.raw_clipped||message.text_clipped)loadOriginal(message).then(fill,()=>{})};details.show=show;details.firstChild.addEventListener('click',show);details.addEventListener('toggle',()=>{if(details.open)show()});return details}
function toolPreview(message){const {input,content}=toolPayload(message);if(toolCategory(message)==='todo'){const items=todoItems(message);return shortText(items.map(item=>item.content||item.subject||item.step||item.description||('Task '+(item.taskId||item.id||''))).join(' · '),180)}if(['metadata','result','context'].includes(message.kind))return displayText(message);if(message.kind==='attachment')return attachmentSummary(message);if(toolCategory(message)==='search')return [input.pattern||input.query||input.glob,input.path||input.cwd].filter(Boolean).join(' · ')||eventDescription(messageJSON(message));const candidate=input.file_path||input.path||input.filename||input.cmd||input.command||input.query||input.pattern||input.url||input.description||input.prompt||input.value||content;if(candidate&&typeof candidate!=='object')return String(candidate);const pairs=Object.entries(input).filter(([key,value])=>!['tool','name'].includes(key)&&typeof value!=='object');return pairs.map(([key,value])=>`${key}: ${value}`).join(' · ')||eventDescription(messageJSON(message))}
function contextLabel(message){const source=String(messageJSON(message)?.source||'context').replace(/^.*\./,'').replace(/_/g,' ');return source.charAt(0).toUpperCase()+source.slice(1)}
function toolLabel(message){const payload=toolPayload(message),category=toolCategory(message),labels={todo:/create/i.test(payload.name)?'Create todo':/delete/i.test(payload.name)||payload.input.status==='deleted'?'Delete todo':/get|list/i.test(payload.name)?'Todos':'Update todos',edit:/write/i.test(payload.name)?'Write':'Edit',command:'Run',search:'Search',read:/list/i.test(payload.name)?'List':'Read',web:'Browse',agent:'Delegate',error:'Error',run:'Event',context:message.kind==='attachment'?'Attachment':contextLabel(message),reasoning:message.reasoning_format==='summary'?'Reasoning summary':'Reasoning',tool:'Tool'};if(messageJSON(message)?.type==='claude_ai_conversation')return 'Conversation summary';if(messageJSON(message)?.type==='token_budget')return 'Token budget (not usage)';if(message.kind==='tool_result')return'Unpaired output';if(message.kind==='delegation_result')return'Agent output';return labels[category]==='Tool'?(payload.name||'Tool'):labels[category]}
function categoryIcon(category){return{git:'git',python:'code',typecheck:'typecheck',test:'check',lint:'lint',todo:'todo',edit:'edit',command:'command',search:'search',read:'read',web:'web',agent:'agent',human:'human',error:'error',run:'event',context:'context',reasoning:'thought',tool:'tool'}[category]||'tool'}
function relativeTime(value,start){if(!value||!start)return'—';const time=new Date(value).getTime(),origin=new Date(start).getTime();if(!Number.isFinite(time)||!Number.isFinite(origin))return'—';const seconds=Math.max(0,Math.round((time-origin)/1000));if(seconds<60)return`+${seconds}s`;const minutes=Math.floor(seconds/60),rest=seconds%60;return`+${minutes}m${rest?String(rest).padStart(2,'0')+'s':''}`}
function timelineEntry(content,message,turnStart,kind=''){const wrapper=node('div',`timeline-entry ${kind}`),time=node('span','timeline-time'),slot=node('div','timeline-content');time.append(...relativeTime(message?.created_at,turnStart).split(/(?<=m)(?=\d)/).map(part=>node('span','',part)));if(message?.id)wrapper.dataset.messageId=message.id;if(message?.created_at)time.title=timeLabel(message.created_at);if(!content.classList.contains('action-row')&&!content.classList.contains('subagent-run'))attachTokenBadge(content.querySelector('.message-card')||content.querySelector('summary')||content,[message]);slot.append(content);wrapper.append(time,slot);return wrapper}

// Agent Markdown, from markdownTree (reader-model.js): text becomes text nodes
// and elements get only the properties the tree names.
function markdownView(text,workspace='',mask=''){return markdownNode(markdownTree(text,0,mask),workspace)}
function markdownNode(item,workspace=''){if(typeof item==='string')return document.createTextNode(item);if(item.icon)return icon(item.icon);const element=node(item.tag,item.className);for(const key of ['href','rel','target','start'])if(key in item)element[key]=item[key];if(item.language)element.dataset.language=item.language;element.append(...item.children.map(child=>markdownNode(child,workspace)));if(item.link)decorateMarkdownLink(element,item,workspace);return element}

// Every element with content can fold to `<tag>…</tag>`; folding the root
// turns the view back into its chip.
function xmlTreeView(element,onCollapse,path='$'){
  const only=element.children.length===1&&typeof element.children[0]==='string'?element.children[0].trim():null;
  if(!onCollapse&&(!element.children.length||only!==null&&!only.includes('\n')))return node('div','xml-line',element.open+(only||'')+element.close);
  const details=node('details','xml-node'),summary=node('summary','xml-line',element.open),children=node('div','xml-children');details.open=true;details.dataset.jsonPath=path;summary.dataset.close='…'+element.close;
  for(const [index,child] of element.children.entries()){if(typeof child!=='string')children.append(xmlTreeView(child,null,path+'['+index+']'));else if(child.trim())children.append(node('div','xml-text',dedentText(child)))}
  details.append(summary,children);if(element.close)details.append(node('div','xml-line',element.close));
  if(onCollapse)details.addEventListener('toggle',()=>{if(!details.open){details.open=true;onCollapse()}});
  return details;
}
const checkMarkdownLink=markdownLinkChecker((href,workspace)=>{
  const controller=new AbortController(),timer=setTimeout(()=>controller.abort(),4000);
  return fetch('/api/markdown-link?'+new URLSearchParams({href,workspace}),{signal:controller.signal})
    .then(response=>{if(!response.ok)throw Error('Link check unavailable');return response.json()})
    .finally(()=>clearTimeout(timer));
});
function decorateMarkdownLink(link,item,workspace){
  const kind=item.link,mark=node('span','markdown-link-icon');mark.setAttribute('aria-hidden','true');link.prepend(mark);
  const update=(status,detail,href,file)=>{
    link.dataset.linkStatus=status;link.title=detail+'\n'+item.href;
    // A description is announced without changing the visible link label.
    link.setAttribute('aria-description',detail);
    mark.replaceChildren(icon(status==='unavailable'?'close':status==='unknown'?'info':kind.icon));
    link.href=href||item.href;link.onclick=null;
    // The Mac app's context menu offers Open With and Show in Finder for this path.
    if(status==='file'&&file?.path){link.dataset.filePath=file.path;link.dataset.fileLaunchable=String(Boolean(file.launchable))}else{delete link.dataset.filePath;delete link.dataset.fileLaunchable}
    // The Mac opens a local file in its default app; the served copy is the fallback elsewhere.
    if(status==='file'&&href)link.onclick=event=>{
      if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;
      event.preventDefault();
      fetch('/api/markdown-link?'+new URLSearchParams({href:item.href,workspace}),{method:'POST'})
        .then(response=>response.ok?response.json():Promise.reject())
        .then(result=>feedbackToast(result.revealed?'Shown in Finder':'Opened '+result.opened.split('/').pop()),()=>window.open(href,'_blank','noopener'));
    };
    if(status==='unavailable'||status==='checking'||status==='unknown'&&kind.kind==='file'){
      link.removeAttribute('href');link.setAttribute('role','link');link.setAttribute('aria-disabled','true');link.tabIndex=0;
    }else{link.removeAttribute('aria-disabled');link.removeAttribute('role')}
  };
  if(kind.kind==='web'){
    update('web',kind.detail);
    const image=document.createElement('img');image.alt='';image.loading='lazy';image.referrerPolicy='no-referrer';image.src='/api/markdown-favicon?'+new URLSearchParams({host:kind.host});image.onerror=()=>image.replaceWith(icon('web'));mark.replaceChildren(image);
  }else if(markdownLinkProbed(kind)){
    update('checking',kind.detail);
    checkMarkdownLink(item.href,workspace).then(result=>update(result.status,result.detail,result.href,result));
  }else update(kind.kind,kind.detail);
}

function messageBody(text,markdown,workspace='',mask=''){
  const segments=xmlSegments(text);if(!segments.some(segment=>segment.tag))return markdown?markdownView(text,workspace,mask):document.createTextNode(text);
  // Consecutive chips share a row; a row followed by prose sits inline before it.
  const fragment=document.createDocumentFragment();let row=null,sourceOffset=0;
  for(const [segmentIndex,segment] of segments.entries()){
    const start=text.indexOf(segment.text,sourceOffset),flags=mask.slice(start,start+segment.text.length);sourceOffset=start+segment.text.length;
    if(!segment.tag||flags.includes('1')){const prose=markdown?markdownView(segment.text,workspace,flags):node('div','message-text',segment.text);if(row){row.classList.add('inline');prose.classList.add('after-xml-chips')}row=null;fragment.append(prose);continue}
    if(!row){const chips=row=node('div','xml-chip-row');chips.addEventListener('toggle',()=>chips.classList.toggle('chip-open',Boolean(chips.querySelector(':scope>.xml-chip[open]'))),true);fragment.append(row)}
    const chip=node('details','xml-chip'),summary=node('summary','',segment.tag);chip.dataset.jsonPath='xml:'+segmentIndex;summary.title=`<${segment.tag}> · ${segment.text.length.toLocaleString()} characters`;const body=node('div','xml-chip-body'),tree=xmlTreeView(xmlTree(segment.text),()=>{chip.open=false;summary.focus()},'xml:'+segmentIndex+':$');body.append(tree);chip.append(summary,body);row.append(chip);
    chip.addEventListener('toggle',()=>{if(chip.open&&document.activeElement===summary)tree.querySelector('summary').focus()})
  }return fragment;
}
// Name and ID buttons put a conversation's or message's reference on the clipboard, e.g. to hand an agent the IDs the Pharos MCP reads.
function copyValueButton(label,value,what){const button=node('button','copy-value');button.type='button';button.title=`Copy ${what}: ${value}`;button.setAttribute('aria-label',`Copy ${what}`);const glyph=()=>button.replaceChildren(node('span','',label),icon('copy'));glyph();let reset=0;button.onclick=async event=>{event.stopPropagation();event.preventDefault();clearTimeout(reset);try{await copyFeedbackText(value);button.replaceChildren(node('span','',label),icon('check'));feedbackToast(`Copied ${what}`)}catch{button.replaceChildren(node('span','',label),icon('error'));feedbackToast(`Could not copy ${what}`)}reset=setTimeout(glyph,1600)};return button}
function copyValues(...buttons){const group=node('span','copy-values');group.append(...buttons.filter(Boolean));return group}
// A message's prose, with XML chips, Markdown links, and a Show more
// toggle for long text. Library conversation cards render messages with it too.
function viewContentKey(message){if(message.id)return message.id;let hash=14695981039346656037n;for(const character of String(message.role)+String(message.text)){hash^=BigInt(character.codePointAt(0));hash=BigInt.asUintN(64,hash*1099511628211n)}return 'content:'+hash.toString(16)}
function saveViewContent(key,value){const state=window.pharosView?.read('reader_content',{})||{};window.pharosView?.write('reader_content',{...state,[key]:value})}
// Non-details content controls use the same keyed, bidirectional map.
function bindViewContent(toggle,key,fallback,apply){
 toggle.dataset.viewContent=key;
 toggle.restoreViewContent=()=>apply(readerContentShown(window.pharosView?.read('reader_content',{}),key,fallback));
 toggle.restoreViewContent();
}
function restoreViewContent(root=document){for(const toggle of root.querySelectorAll('[data-view-content]'))toggle.restoreViewContent?.()}
window.addEventListener('popstate',()=>restoreViewContent());window.addEventListener('pharos:view',()=>restoreViewContent());window.addEventListener('pharos:view-ready',()=>restoreViewContent());

function citationView(message){const citations=messageCitations(message);if(!citations.length)return null;const sources=node('aside','message-citations');sources.setAttribute('aria-label','Citations');sources.append(node('strong','tool-detail-label','Sources'));const list=node('ol','');for(const citation of citations){const item=node('li','');if(citation.url){const link=node('a','',citation.title);link.href=citation.url;link.target='_blank';link.rel='noopener noreferrer';item.append(link)}else item.append(document.createTextNode(citation.title));if(citation.quote)item.append(node('blockquote','',citation.quote));list.append(item)}sources.append(list);return sources}
// Measure the collapsed rendering, not source length: Markdown and XML chips can
// make a long source short, while narrow windows can clip a short message.
const messageSizeObserver=new ResizeObserver(entries=>{for(const {target} of entries)target.measureMessageOverflow?.()});
const messageRemovalObserver=new MutationObserver(records=>{for(const record of records)for(const removed of record.removedNodes){if(removed.nodeType!==1)continue;for(const prose of [removed,...removed.querySelectorAll('.message-prose')])if(prose.measureMessageOverflow&&!prose.isConnected)messageSizeObserver.unobserve(prose)}});
messageRemovalObserver.observe(document.body,{childList:true,subtree:true});
function messageCard(message,workspace='',text=displayText(message)){
  const labels=messageLabelView(message,labelTaxonomy),events=messageEventView(message,text),targets=eventTargetSpans(text,messageEventTargets(message)),mask=eventTargetMask(text,targets,eventMask(text,events,humanWritingMask(message,text))),card=node('div','message-card'),prose=node('div','message-prose');
  prose.classList.add('rendered-markdown');prose.append(messageBody(text,true,workspace,mask));
  if(message.authorship?.provisional)card.append(node('p','usage-dashboard-note','Provisional writing estimate · refined overnight'));
  card.append(prose);const sources=citationView(message);if(sources)card.append(sources);decorateEventLinks(card,message,events,targets);
  if(mask.includes('1'))card.classList.add('writing');if(labels){card.dataset.labelFamily=labels.family;card.labelMessage=message}if(events){card.dataset.agentEvents=String(events.length);card.eventView=events}
  const toggle=node('button','message-toggle','Show more');toggle.type='button';toggle.hidden=true;let measured=false;
  const apply=expanded=>{const state=String(expanded),label=expanded?'Show less':'Show more';if(toggle.getAttribute('aria-expanded')!==state)toggle.setAttribute('aria-expanded',state);if(toggle.textContent!==label)toggle.textContent=label;prose.classList.toggle('prose-truncated',!expanded&&(!measured||!toggle.hidden))};
  bindViewContent(toggle,viewContentKey(message),false,apply);
  toggle.onclick=()=>{const expanded=toggle.getAttribute('aria-expanded')!=='true';apply(expanded);saveViewContent(toggle.dataset.viewContent,expanded)};
  prose.measureMessageOverflow=()=>{
    if(!prose.isConnected){messageSizeObserver.unobserve(prose);return}
    if(!prose.getClientRects().length)return;
    const expanded=toggle.getAttribute('aria-expanded')==='true';prose.classList.add('prose-truncated');
    const hidden=prose.scrollHeight<=prose.clientHeight;measured=true;if(toggle.hidden!==hidden)toggle.hidden=hidden;
    prose.classList.toggle('prose-truncated',!expanded&&!hidden);
  };
  messageSizeObserver.observe(prose);
  prose.pharosFindExpand=()=>{prose.measureMessageOverflow();if(prose.classList.contains('prose-truncated')&&(!measured||!toggle.hidden))toggle.click()};
  prose.addEventListener('toggle',event=>{prose.measureMessageOverflow();if(event.target.open&&prose.classList.contains('prose-truncated')&&!toggle.hidden)toggle.click()},true);
  card.append(toggle);
  // Search's Browse preview shares message cards but has no transcript key.
  card.addEventListener('pointerover',event=>{if(card.closest('.conversation-panel'))return;const mark=event.target.closest?.('mark.human-written');if(!event.buttons&&mark&&card.closest('.show-writing'))showWritingTip(mark,event);else hideLabelTip()});
  card.addEventListener('pointermove',event=>{if(!card.closest('.conversation-panel')&&labelTip&&!labelTip.hidden){if(event.buttons)hideLabelTip();else placeLabelTip(event)}});
  card.addEventListener('pointerleave',hideLabelTip);card.addEventListener('pointerdown',hideLabelTip);
  const handoffs=handoffStrip(message);if(handoffs)card.append(handoffs);for(const detail of card.querySelectorAll('details'))detail.dataset.viewKey='content:'+viewContentKey(message)+':'+detail.className+(detail.dataset.jsonPath?':'+detail.dataset.jsonPath:'');return card;
}
function attachMessageRaw(card,message){if(messageJSON(message,true)){const raw=rawDisclosure(message),button=node('button','message-raw-button',icon('code'));raw.hidden=true;button.type='button';button.setAttribute('aria-label','Raw event');button.setAttribute('aria-expanded','false');const key='raw:'+viewContentKey(message),apply=open=>{raw.hidden=!open;if(open)raw.show();raw.open=open;button.setAttribute('aria-expanded',String(open))};bindViewContent(button,key,false,apply);button.onclick=()=>{const open=raw.hidden;apply(open);saveViewContent(key,open)};card.append(button,raw)}}
function narrativeEvent(message,turnStart,workspace=''){const row=node('article',`speaker-row ${message.role==='user'?'human':'agent'} conversation-event`),card=messageCard(message,workspace);row.setAttribute('aria-label',message.role==='user'?'User message':message.role==='agent'?'Parent agent message':'Assistant message');const mark=kindIcon(message.role==='user'?'human':'agent');mark.classList.add('message-kind');card.prepend(mark);if(message.id){const name=shortText(messageSummary(message),120);card.append(copyValues(name?copyValueButton('Name',name,'message name'):null,copyValueButton('ID',message.id,'message ID')))}attachMessageRaw(card,message);row.dataset.searchEntry=messageSearch(message);if(card.labelMessage){row.dataset.labelFamily=card.dataset.labelFamily;row.labelMessage=message}row.append(card);return timelineEntry(row,message,turnStart,message.role==='user'?'human-entry':'agent-entry')}
function changeChips(files){const group=node('span','change-chips');files.forEach(file=>{const chip=node('span','change-chip');chip.title=file.path;chip.append(node('span','change-path',file.path.split('/').pop()||file.path),node('span','change-directory',file.path.includes('/')?file.path.split('/').slice(0,-1).slice(-2).join('/')+'/':''));if(file.unknown)chip.append(node('span','delta-unknown','Δ unknown'));else chip.append(node('span','delta-add',`+${file.added}`),node('span','delta-remove',`−${file.removed}`));group.append(chip)});return group}
function diffView(files){const group=node('div','diff-files');files.forEach(file=>{const section=node('section','tool-detail-section');section.append(node('strong','tool-detail-label',file.path+(file.note?' · '+file.note:'')));const pre=node('div','diff-view');if(!file.diff)pre.append(node('span','diff-line','Diff was not retained by the source.'));else String(file.diff).split('\n').forEach(line=>pre.append(node('span',`diff-line ${!file.contentOnly&&line.startsWith('+')&&!line.startsWith('+++')?'add':!file.contentOnly&&line.startsWith('-')&&!line.startsWith('---')?'remove':line.startsWith('@@')||line.startsWith('***')?'hunk':''}`,line)));section.append(pre);group.append(section)});return group}
// Hand-offs to and from related work (handoffMarkers). The work page opens
// a linked conversation in its reader when it holds it; otherwise the link
// opens the related work's page.
function handoffStrip(...messages){const markers=messages.flatMap(message=>handoffMarkers(message));if(!markers.length)return null;const strip=node('span','handoff-markers');for(const marker of markers){const link=node('a',`handoff-marker${marker.certain?'':' uncertain'}`,marker.label);link.href=marker.href;link.title=marker.title;link.onclick=event=>{event.stopPropagation();if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();if(!window.pharosOpenHandoff?.(marker))window.pharosNavigate(marker.href)};strip.append(link)}return strip}
function mcpTarget(presentation){
  const target=node(presentation.href?'a':'span','mcp-target',presentation.target);
  if(presentation.href){target.href=presentation.href;target.onclick=event=>{event.stopPropagation();if(event.button||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||new URL(target.href).origin!==location.origin)return;event.preventDefault();openHistoryEntry(presentation.href)}}
  return target;
}
function mcpView(presentation){
  const view=node('section','mcp-presentation'),heading=node('div','tool-detail-label');heading.append(presentation.label+' · ',mcpTarget(presentation));view.append(heading);
  if(presentation.status!=='recorded')view.append(node('p',presentation.status==='failed'||presentation.status==='partial'?'badtext':'muted',({failed:'Tool reported failure',partial:'Only part of the request succeeded',missing:'No result retained; completion is unknown',unknown:'Outcome not confirmed'})[presentation.status]));
  if(presentation.facts.length){const facts=node('dl','tool-inputs');for(const [key,value]of presentation.facts)facts.append(node('dt','',key),node('dd','',value));view.append(facts)}
  for(const section of presentation.sections){const block=node('section','tool-detail-section');if(section.title)block.append(node('strong','tool-detail-label',section.title));if(section.rows){const table=node('table','tool-summary-table'),head=node('tr','');section.columns.forEach(column=>head.append(node('th','',column.replace(/_/g,' '))));table.append(head);section.rows.forEach(values=>{const row=node('tr','');values.forEach(value=>row.append(node('td','',value)));table.append(row)});const scroll=node('div','mcp-table');scroll.append(table);block.append(scroll)}else if(section.format==='markdown'){const content=node('div','rendered-markdown export-document');content.append(messageBody(section.text,true));block.append(content)}else block.append(node('pre',section.format==='code'?'tool-output':'tool-output mcp-prose',section.text));view.append(block)}
  return view;
}
function toolEvent(message,result=null,turnStart='',artifactLinks={}){
  const results=Array.isArray(result)?result:result?[result]:[],category=toolCategory(message),payload=toolPayload(message),failed=toolFailed(message,results);
  const rich=mcpPresentation(message,results,artifactLinks),files=rich?null:fileChanges(message,results[0]),details=node('details',`tool-event conversation-event ${failed?'error':''} ${results.length?'paired':''}`),summary=node('summary',''),body=node('div','event-body'),label=rich?.label||toolLabel(message);
  if(message.id)body.dataset.messageId=message.id;if(message.call_id)body.dataset.callId=message.call_id;
  summary.append(node('span','activity-title',label));
  const durationEstimate=toolDurationEstimatePresentation(message);if(durationEstimate){const badge=node('span','mini-count',durationEstimate.label);badge.title=durationEstimate.detail;summary.append(badge)}
  if(files)summary.append(changeChips(files));else{const preview=toolPreview(message),target=node('span',`activity-target ${category==='command'?'command-target':''}`,shortText(preview,240));target.title=preview;const path=payload.input.file_path||payload.input.path||payload.input.filename;if(path&&typeof path==='string'&&category!=='search'){const pieces=path.split('/'),filename=pieces.pop();target.replaceChildren(node('span','target-filename',filename||path),node('span','target-directory',(pieces.length>3?'…/':'')+pieces.slice(-3).join('/')+'/'))}else if(category==='search'&&payload.input.pattern){target.textContent=shortText(payload.input.pattern+(payload.input.path?' · '+payload.input.path:''),240)}summary.append(target)}
  const handoffs=handoffStrip(message,...results);if(handoffs)summary.append(handoffs);
  if(failed)summary.append(node('span','activity-status badtext','Failed'));
  else if(message.kind==='tool_call'&&!results.length)summary.append(node('span','activity-status','No result'));
  const errorText=failed?shortText(results.map(item=>readableValue(toolPayload(item).content)).join(' '),160):'';
  if(errorText)summary.append(node('span','error-preview badtext',errorText));
  if(rich)body.append(mcpView(rich));
  if(files){if(failed)body.append(node('p','badtext','Requested change · tool reported failure'));body.append(diffView(files))}
  if(message.kind==='attachment'){const value=messageJSON(message)||{};body.append(node('strong','tool-detail-label',attachmentSummary(message)));const sections=exportDocumentSections(value);if(sections.length){for(const section of sections){const document=node('section','export-document rendered-markdown');document.append(node('strong','tool-detail-label',section.title),messageBody(section.text,true));body.append(document)}}else if(value.extracted_content)body.append(node('pre','tool-output',value.extracted_content));if(value.text)body.append(node('p','muted',value.text));const facts=node('dl','tool-inputs');for(const key of ['file_type','file_size','file_uuid','sha256'])if(value[key]!==undefined)facts.append(node('dt','',key.replace(/_/g,' ')),node('dd','',String(value[key])));body.append(facts)}
  if(messageJSON(message)?.type==='claude_ai_conversation'&&payload.raw.text){const summary=node('div','rendered-markdown export-document');summary.append(messageBody(payload.raw.text,true));body.append(summary)}
  if(!rich&&!files&&message.kind!=='attachment'&&messageJSON(message)?.type!=='claude_ai_conversation'&&Object.keys(payload.input).length){const inputs=node('dl','tool-inputs');for(const [key,value]of Object.entries(payload.input)){if(['tool','name'].includes(key))continue;inputs.append(node('dt','',key.replace(/_/g,' ')),node('dd','',readableValue(value)))}body.append(inputs)}
  results.forEach(item=>{const section=node('section','tool-detail-section'),resultPayload=toolPayload(item);if(item.id)section.dataset.messageId=item.id;if(item.call_id)section.dataset.callId=item.call_id;if(!rich)section.append(node('span','tool-detail-label',`Output ${relativeTime(item.created_at,turnStart)}`),node('pre','tool-output',readableValue(resultPayload.content)||readableValue(resultPayload.raw)));section.append(rawDisclosure(item,'Raw result'));const sources=citationView(item);if(sources)section.append(sources);body.append(section)});
  if(!rich)for(const item of results){const value=toolPayload(item).raw;for(const key of ['structured_content','display_content'])if(value?.[key]!=null)body.append(node('span','tool-detail-label',key.replace(/_/g,' ')),node('pre','tool-output',readableValue(value[key])))}
  body.append(rawDisclosure(message));
  const clipped=[message,...results].filter(item=>item.text_clipped);
  if(clipped.length){const note=node('div','clipped-note'),load=node('button','',`Load full output · ${formatBytes(clippedBytes(clipped))}`);load.type='button';note.append(node('span','','Long output is shortened here.'),load);load.onclick=async()=>{load.disabled=true;load.textContent='Loading…';try{await Promise.all(clipped.map(loadOriginal));note.dispatchEvent(new Event('originals-loaded',{bubbles:true}))}catch{load.textContent='Full output unavailable'}};body.prepend(note)}
  details.dataset.searchEntry=[message,...results].map(searchText).join(' ');details.append(summary,body);return details;
}
function toolClusterData(messages){const calls=messages.filter(message=>message.kind==='tool_call'),source=calls.length?calls:messages,categories=new Map();for(const message of source){const category=toolCategory(message);categories.set(category,(categories.get(category)||0)+1)}const categoryLabels={edit:'change',command:'command',search:'search',read:'read',web:'browser action',agent:'sub-agent event',error:'error',run:'run event',context:'injected context',tool:'tool action'},parts=[...categories].map(([category,count])=>`${count} ${categoryLabels[category]||category}${count===1?'':'s'}`);return{count:source.length,title:`${source.length} activit${source.length===1?'y':'ies'}`,preview:parts.join(' · ')}}

function kindIcon(kind){const holder=node('span','kind-icon',icon(categoryIcon(kind)));holder.setAttribute('aria-hidden','true');return holder}
function todoView(message,results=[]){const list=node('div','todo-list');for(const item of todoItems(message,results)){const status=item.status||'pending',row=node('div','todo-item '+status);row.append(node('span','todo-state',icon(({completed:'check',in_progress:'theme-system',deleted:'minus',pending:'severity-low'})[status]||'severity-low')),node('span','',item.content||item.subject||item.step||item.description||('Task '+(item.taskId||item.id||''))),node('span','todo-status',status.replace(/_/g,' ')));list.append(row)}return list}
function fileChip(path,repoRoot,onOpen,file=null,viewKey='',history=''){
  const chip=node('span','file-chip'),{inside,absolute,directory,filename}=filePathParts(path,repoRoot);
  chip.title=path;
  if(directory){const prefix=node('button','file-prefix',absolute&&!inside?directory:'…/');prefix.type='button';prefix.setAttribute('aria-label','Expand path for '+filename);prefix.setAttribute('aria-expanded',String(absolute&&!inside));const key='path:'+viewKey,apply=expanded=>{prefix.setAttribute('aria-expanded',String(expanded));prefix.textContent=expanded?directory:'…/';prefix.setAttribute('aria-label',(expanded?'Collapse':'Expand')+' path for '+filename)};if(viewKey)bindViewContent(prefix,key,absolute&&!inside,apply);prefix.onclick=event=>{event.stopPropagation();event.preventDefault();const expanded=prefix.getAttribute('aria-expanded')!=='true';apply(expanded);if(viewKey)saveViewContent(key,expanded)};chip.append(prefix)}
  const target=node('button','file-target',filename||path);target.type='button';target.setAttribute('aria-expanded','false');target.setAttribute('aria-label',(file?'Show diff for ':'Show output for ')+path);target.onclick=event=>{event.stopPropagation();event.preventDefault();onOpen(target)};chip.append(target);
  if(file){if(file.unknown)chip.append(node('span','delta-unknown','Δ unknown'));else chip.append(node('span','delta-add','+'+file.added),node('span','delta-remove','−'+file.removed))}
  let copying=false;const copy=node('button','file-copy',icon('copy'));copy.type='button';copy.setAttribute('aria-label','Copy path '+path);copy.title='Copy full path';copy.onclick=async event=>{event.stopPropagation();event.preventDefault();if(copying)return;copying=true;copy.setAttribute('aria-disabled','true');copy.replaceChildren(icon('copy'));copy.title='Copying…';try{await copyFeedbackText(path);copy.replaceChildren(icon('check'));copy.title='Copied'}catch{copy.replaceChildren(icon('error'));copy.title='Copy unavailable; expand the path to select it'}finally{copying=false;copy.removeAttribute('aria-disabled')}};chip.append(copy);
  // The file's page: every session that changed it, and its commits.
  if(history){const link=node('a','file-history-link',icon('history'));link.href=history;link.title='File history: every session that changed this file, and its commits';link.setAttribute('aria-label','History of '+path);link.onclick=event=>{event.stopPropagation();if(event.button||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();openHistoryEntry(history)};chip.append(link)}
  return chip;
}
function actionRow(messages,context){
  const first=messages[0],firstMCP=mcpPresentation(first,context.toolResultsByCall.get(first.call_id)||[],context.artifactLinks),category=toolCategory(first),row=node('div','action-row'),details=node('details','tool-event conversation-event'),summary=node('summary',''),chips=node('span','action-chips');row.append(details);
  // Long transcripts have thousands of rows and few are ever opened, so the
  // chip preview and the details body are built on first use.
  let preview=null,previewBody=null,selected=null,body=null;
  const closePreview=(restore=false)=>{if(preview)preview.hidden=true;if(selected){selected.setAttribute('aria-expanded','false');if(!restore){saveViewContent(selected.dataset.viewContent,false);selected.focus()}}selected=null};
  const showPreview=(button,content,restore=false)=>{if(!preview){preview=node('section','chip-preview');previewBody=node('div','');const close=node('button','preview-close','Close');close.type='button';close.onclick=()=>closePreview();preview.append(close,previewBody);row.append(preview)}else if(selected===button&&!preview.hidden&&!restore){closePreview();return}if(selected){selected.setAttribute('aria-expanded','false');if(!restore)saveViewContent(selected.dataset.viewContent,false)}selected=button;button.setAttribute('aria-expanded','true');previewBody.replaceChildren(content);preview.hidden=false;if(!restore)saveViewContent(button.dataset.viewContent,true)};
  const registerPreview=(button,key,content)=>{bindViewContent(button,'preview:'+key,false,open=>{if(open)showPreview(button,content(),true);else if(selected===button)closePreview(true)});return button};
  row.dataset.searchKind=first.kind==='reasoning'?'thinking':['tool_result','delegation_result','error'].includes(first.kind)?'response':'tool';
  const mark=kindIcon(commandKind(first));mark.title=commandKind(first);mark.dataset.kind=commandKind(first);summary.append(mark,context.tagFilter.button(firstMCP?.label||toolLabel(first),()=>[context.tagFilter.tagOf(first)],'activity-title'));
  if(first.kind==='tool_call'){const names={read:'Read',write:'Write',run:'Run',git:'Git',network:'Network',delegate:'Delegation',wait:'Wait',poll:'Poll',other:'Other'};summary.append(context.tagFilter.button(names[readerTimelineCategory(first)],()=>context.tagFilter.group(readerTimelineCategory(first)),'action-category'))}summary.append(chips);
  // Thinking's agent events: a count on the row under Events, and the text
  // with its marks at the top of the details.
  const thinkingEvents=message=>message.kind==='reasoning'?messageEventView(message):null;
  {const found=messages.flatMap(message=>thinkingEvents(message)||[]);if(found.length){const badge=node('span','event-badge '+eventGroupClass(found[0].group));badge.append(node('i'),`${found.length} event${found.length===1?'':'s'}`);badge.title=[...new Set(found.map(item=>item.label))].join(', ');badge.eventView=found;summary.append(badge)}}
  let failures=0,missing=0;const search=[];
  messages.forEach(message=>{
    const chipStart=chips.children.length,results=context.toolResultsByCall.get(message.call_id)||[],failed=toolFailed(message,results),payload=toolPayload(message),rich=mcpPresentation(message,results,context.artifactLinks),files=rich?null:fileChanges(message,results[0]);
    failures+=Number(failed);missing+=Number(!results.length&&message.kind==='tool_call');search.push(message,...results);
    const output=()=>{const content=node('div','');content.append(node('div','tool-detail-label',toolPreview(message)));if(failed)content.append(node('div','badtext','Failed'));if(results.length)results.forEach(result=>content.append(node('pre','tool-output',readableValue(toolPayload(result).content)||readableValue(toolPayload(result).raw))));else content.append(node('div','muted','No output retained'));if(results.some(result=>result.text_clipped))Promise.all(results.map(loadOriginal)).then(()=>{if(content.isConnected)content.replaceWith(output())},()=>{});return content};
    if(rich)chips.append(mcpTarget(rich));
    else if(files)files.forEach(file=>{const key=message.id+':'+file.path,content=()=>{const value=node('div','');if(failed)value.append(node('p','badtext','Requested change · tool reported failure'));value.append(diffView([file]));return value};const chip=fileChip(file.path,context.repoRoot,button=>showPreview(button,content()),file,key,context.fileLinks?.(message.id,file.path,message.call_id));registerPreview(chip.querySelector('.file-target'),key,content);chips.append(chip)});
    else if(category==='read'&&(payload.input.file_path||payload.input.path||payload.input.filename)){const path=payload.input.file_path||payload.input.path||payload.input.filename,key=message.id+':'+path,chip=fileChip(path,context.repoRoot,button=>showPreview(button,output()),null,key,context.fileLinks?.(message.id,path,message.call_id));registerPreview(chip.querySelector('.file-target'),key,output);chips.append(chip)}
    else if(category==='todo'){const items=todoItems(message,results),completed=items.filter(item=>item.status==='completed').length;const button=node('button','query-chip',message.todoSummary||toolPreview(message));button.type='button';button.onclick=event=>{event.stopPropagation();event.preventDefault();showPreview(button,todoView(message,results))};registerPreview(button,message.id+':todo',()=>todoView(message,results));chips.append(button)}
    else if(category==='search'){const button=node('button','query-chip',toolPreview(message));button.type='button';button.onclick=event=>{event.stopPropagation();event.preventDefault();showPreview(button,output())};registerPreview(button,message.id+':search',output);chips.append(button)}
    else if(/^https?:\/\/[^\s<>]+$/i.test(toolPreview(message))&&markdownLinkKind(toolPreview(message))?.kind==='web'){
      const href=toolPreview(message),link=node('a','markdown-link action-web-link',shortText(href,240));link.target='_blank';link.rel='noopener noreferrer';decorateMarkdownLink(link,{href,link:markdownLinkKind(href)},context.workspace);link.append(node('span','external-indicator',icon('link-out')));link.onclick=event=>event.stopPropagation();chips.append(link);
    }else chips.append(node('span','activity-target '+(category==='command'?'command-target':''),shortText(toolPreview(message),240)));
    if(failed)[...chips.children].slice(chipStart).forEach(chip=>{chip.classList.add('failed');chip.title=(chip.title||'')+' · Failed'});
  });
  if(failures){details.classList.add('error');summary.append(node('span','activity-status badtext',firstMCP?.status==='partial'?'Partial':messages.length>1?failures+' failed':'Failed'))}else if(missing)summary.append(node('span','activity-status',messages.length>1?missing+' without result':'No result'));
  const more=node('span','line-details-hint','Details');summary.append(more);
  attachTokenBadge(summary,search,true);details.dataset.searchEntry=search.map(searchText).join(' ');
  // Details are built on first open; long outputs then load in full.
  const build=()=>{
    if(!body){body=node('div','event-body');body.addEventListener('originals-loaded',build);details.append(body)}
    // Loading an original replaces these sections. Keep a deep link's marker
    // and scroll target attached to the replacement until its highlight ends.
    const focused=[...body.querySelectorAll('.authorship-target')].map(target=>({id:target.dataset.messageId,call:target.dataset.callId,until:Number(target.dataset.revealUntil)}));
    body.replaceChildren();
    if(messages.length===1&&firstMCP){const updated=mcpPresentation(first,context.toolResultsByCall.get(first.call_id)||[],context.artifactLinks);context.tagFilter.refresh(first);summary.querySelector('.activity-title').setLabel(updated?.label||toolLabel(first));if(updated)chips.replaceChildren(mcpTarget(updated))}
    messages.forEach(message=>{const results=context.toolResultsByCall.get(message.call_id)||[];if(messages.length>1)body.append(node('div','tool-detail-label',relativeTime(message.created_at,context.turnStart)+' · '+toolPreview(message)));
      if(message.kind==='reasoning'){const card=messageCard(message,'',eventText(message));card.classList.add('thinking-card');card.dataset.messageId=message.id;attachMessageRaw(card,message);body.append(card)}
      else body.append(toolEvent(message,results,context.turnStart,context.artifactLinks).querySelector('.event-body'))});
    for(const focus of focused){if(!focus.id||focus.until<=Date.now())continue;const selector=`[data-message-id="${CSS.escape(focus.id)}"]`+(focus.call?`[data-call-id="${CSS.escape(focus.call)}"]`:''),target=body.querySelector(selector);if(target)markTranscriptTarget(target,focus.until)}
  };
  details.sourceMessages=search;
  let built=false;details.materialize=()=>{if(!built){built=true;details.removeAttribute('data-pharos-find-pending');build()}};findLazily(details,()=>search,()=>details.materialize());
  summary.addEventListener('click',()=>{if(!details.open)details.materialize()});
  details.addEventListener('toggle',()=>{if(!details.open)return;details.materialize();if(search.some(item=>item.text_clipped))Promise.all(search.map(loadOriginal)).then(build,()=>{})});
  details.append(summary);return row;
}

// Which kinds of events a transcript shows. Every rendered entry carries its
// category in data-show, and the panel hides categories by class, so events
// rendered later (pages, turns, sub-agents) follow the filter too. Levels are
// presets over the categories (TRANSCRIPT_CATEGORIES and TRANSCRIPT_LEVELS in
// reader-model.js); whole-key defaults combine them with highlight choices.
const TRANSCRIPT_FILTER_KEY='pharos-transcript-filter';
function loadTranscriptFilter(){return transcriptFilterState(new URLSearchParams(location.search),pharosPrefs.get(TRANSCRIPT_FILTER_KEY,{}))}
function saveTranscriptFilter(state){pharosPrefs.set(TRANSCRIPT_FILTER_KEY,state)}
// True when the filter hides the element or an event that contains it.
function transcriptHidden(panel,element){for(let entry=element?.closest('[data-show]');entry&&panel.contains(entry);entry=entry.parentElement?.closest('[data-show]'))if(!entry.classList.contains('filter-exempt')&&(entry.dataset.tagHidden==='true'||panel.classList.contains('hide-'+entry.dataset.show)))return true;return false}
// Navigation to a specific event (a link, a failure) shows it despite the filter.
function exemptFromTranscriptFilter(panel,element){for(let entry=element?.closest('[data-show]');entry&&panel.contains(entry);entry=entry.parentElement?.closest('[data-show]'))entry.classList.add('filter-exempt')}
// Page Find (web/src/page-find.ts) counts the text lazy transcript parts
// would render, in the kinds the Show filter shows, and renders the parts
// holding the match it steps to.
function transcriptFindable(panel){const shown=panel?.transcriptShown?.();return message=>(!shown||panel.transcript.pathOf(message).every(key=>shown.has(key)))&&!panel?.tagFilter?.hiddenFor(message)}
function findLazily(element,messages,materialize){element.pharosSourceMessages=messages;element.dataset.pharosFindPending='';element.pharosFindPending=()=>{if(!element.hasAttribute('data-pharos-find-pending'))return null;const findable=transcriptFindable(element.closest('.conversation-panel'));return messages().filter(findable).map(findableText)};element.pharosFindMaterialize=test=>{const findable=transcriptFindable(element.closest('.conversation-panel')),wanted=message=>findable(message)&&test(findableText(message));const loaded=messages().filter(message=>message.id&&wanted(message));if(loaded.length){const state=window.pharosView?.read('reader_content',{})||{};window.pharosView?.write('reader_content',{...state,...Object.fromEntries(loaded.map(message=>['loaded:'+message.id,true]))})}materialize(wanted)}}
function transcriptKindList(keys){const labels=keys.map(key=>TRANSCRIPT_CATEGORIES.find(([name])=>name===key)?.[1].toLowerCase()).filter(Boolean);return labels.length>1?labels.slice(0,-1).join(', ')+' and '+labels.at(-1):labels[0]||''}
// Each path is an event's category followed by those of the sub-agents holding it.
function transcriptFilterControl(panel,events){
  const paths=events.map(message=>panel.transcript.pathOf(message));
  const box=node('div','transcript-filter'),heading=node('div','transcript-key-heading'),host=node('span','transcript-key-defaults'),summary=node('span','transcript-filter-summary'),total=paths.length;
  heading.append(host,summary);box.append(heading);box.dataset.viewField='reader_filter';
  const defaults=window.pharosReaderDefaults(host,true);let state=loadTranscriptFilter(),previousShown=null;
  const configured=()=>Object.fromEntries(READER_HIGHLIGHTS.map(option=>[option.field,window.pharosView?.read(option.field,pharosPrefs.get(option.pref,option.fallback))??option.fallback]));
  const apply=()=>{
    const shown=transcriptShown(state),signature=JSON.stringify([...shown]);
    if(signature!==previousShown){previousShown=signature;panel.querySelectorAll('.filter-exempt').forEach(entry=>entry.classList.remove('filter-exempt'));
      for(const [key]of TRANSCRIPT_CATEGORIES)panel.classList.toggle('hide-'+key,!shown.has(key));
      panel.dispatchEvent(new Event('transcript-filter'));
    }
    const visible=paths.filter((path,index)=>path.every(key=>shown.has(key))&&!panel.tagFilter.hiddenFor(events[index])).length;
    summary.textContent=visible===total?`${total.toLocaleString()} events`:`${visible.toLocaleString()} of ${total.toLocaleString()} events shown`;
    const hidden=window.pharosView?.read('reader_highlight_hidden',pharosPrefs.get('pharos-reader-highlight-hidden',[]))||[];
    defaults.render({presets:READER_KEY_PRESETS,selected:panel.tagFilter.hasHidden()?null:readerKeyPresetID(state,configured(),hidden),onChoose:id=>{
      const values=readerKeyPresetValues(id);if(!values)return;
      state={...state,level:values.reader_filter_level};saveTranscriptFilter(state);panel.tagFilter.clear();
      for(const option of READER_HIGHLIGHTS)pharosPrefs.set(option.pref,values[option.field]);
      pharosPrefs.set('pharos-reader-highlight-hidden',[]);window.pharosView?.writeAll(values);apply();panel.restoreHighlights?.();
    }});
    window.pharosView?.writeAll({reader_filter:[...shown],reader_filter_level:state.level});
  };
  const choose=next=>{state={...state,...next};saveTranscriptFilter(state);apply()};
  panel.refreshTranscriptFilter=apply;
  panel.restoreTranscriptView=()=>{state=loadTranscriptFilter();apply()};
  panel.transcriptShown=()=>transcriptShown(state);
  panel.showTranscriptKinds=keys=>{const shown=transcriptShown(state);keys.forEach(key=>shown.add(key));choose(shown.size===TRANSCRIPT_CATEGORIES.length?{level:'all'}:{level:'custom',custom:TRANSCRIPT_CATEGORIES.map(([key])=>key).filter(key=>shown.has(key))})};
  const changes=['pharos:uri-changed','pharos:view','popstate'],follow=()=>{if(box.isConnected)panel.restoreTranscriptView()};
  for(const name of changes)addEventListener(name,follow);
  conversationMapCleanups.push(()=>{defaults.dispose();for(const name of changes)removeEventListener(name,follow)});
  apply();return box;
}

// Filter the labels actually shown on activity lines. These refine the broader
// transcript presets and retain their own URL state, including hidden labels
// from other conversations.
function transcriptTagControl(panel,messages,context){
  const host=node('div','transcript-tag-key');host.dataset.viewField='reader_hidden_tags';
  const keyHost=node('span','');host.append(node('span','transcript-filter-label','Hidden activity'),keyHost);const keys=window.pharosReaderKeys(keyHost,true),parents=new Map(),bound=new WeakMap();
  for(const message of messages)if(message.kind==='delegation')for(const id of [message.native_id,message.call_id])if(id&&!parents.has(id))parents.set(id,message);
  const describe=message=>message.kind==='message'?null:{category:message.kind==='tool_call'?readerTimelineCategory(message):'event:'+message.kind,label:message.kind==='delegation'?'Delegate':mcpPresentation(message,context.toolResultsByCall.get(message.call_id)||[],context.artifactLinks)?.label||toolLabel(message)};
  const index=transcriptTagIndex(messages,panel.transcript,describe),mounted=new Set(),controllers=new Set();let hidden=new Set(),previousHidden=null;
  const all=()=>index.all(),shown=()=>all().filter(id=>!transcriptTagHidden(hidden,id));
  const nameOf=id=>{const info=index.info(id),parts=transcriptTagParts(id);if(!info)return parts?.[1]||id;const duplicate=all().some(other=>other!==id&&index.info(other)?.label===info.label);const names={read:'Read',write:'Write',run:'Run',git:'Git',network:'Network',delegate:'Delegation',wait:'Wait',poll:'Poll',other:'Other'};return duplicate?info.label+' · '+(names[info.category]||info.category.replace(/^event:/,'')):info.label};
  const hiddenTagsFor=(message,seen=new Set())=>{if(!message||seen.has(message))return [];seen.add(message);const call=panel.transcript.consumed.get(message);if(call)return hiddenTagsFor(call,seen);const tag=index.tagOf(message);return [...(tag&&transcriptTagHidden(hidden,tag)?[tag]:[]),...hiddenTagsFor(parents.get(message.parent_native_id),seen)]};
  const hiddenFor=message=>hiddenTagsFor(message).length>0;
  const apply=(force=false)=>{
    const signature=JSON.stringify([...hidden].sort());if(signature===previousHidden&&!force)return;if(signature!==previousHidden)panel.querySelectorAll('.filter-exempt').forEach(entry=>entry.classList.remove('filter-exempt'));previousHidden=signature;
    panel.querySelectorAll('[data-reader-tag]').forEach(entry=>entry.dataset.tagHidden=String(transcriptTagHidden(hidden,entry.dataset.readerTag)));
    const ids=all(),selected=shown(),items=ids.filter(id=>transcriptTagHidden(hidden,id)).map(id=>({id,label:nameOf(id),values:[id],count:index.info(id).count}));
    host.hidden=!items.length;keys.render({items,all:ids,shown:selected,onChange:select});
    for(const entry of mounted)entry.root.render({...entry.props,values:entry.values(),all:ids,shown:selected,onChange:select});
    panel.refreshTranscriptFilter?.();panel.dispatchEvent(new Event('transcript-filter'));
  };
  const select=values=>{hidden=transcriptTagSelection(hidden,all(),values);window.pharosView?.write('reader_hidden_tags',[...hidden]);apply()};
  const restoreTranscriptTags=()=>{const value=window.pharosView?.read('reader_hidden_tags',[])||[];hidden=new Set(Array.isArray(value)?value.filter(item=>typeof item==='string'):[]);apply()};
  const button=(label,values,className)=>{const wrapper=node('span',className+' reader-line-tag'),trigger=node('button','',label);trigger.type='button';trigger.setAttribute('aria-haspopup','menu');wrapper.append(trigger);
    trigger.dataset.viewField='reader_hidden_tags';trigger.setAttribute('aria-expanded','false');
    wrapper.addEventListener('click',event=>event.stopPropagation());
    let mountedEntry=null;const refreshTrigger=()=>{if(!mountedEntry){trigger.textContent=label;trigger.setAttribute('aria-label',values().length>1?label+' category':label);trigger.title='Filter '+label}};controllers.add(refreshTrigger);
    wrapper.setLabel=next=>{label=next;if(mountedEntry){mountedEntry.props.label=next;mountedEntry.root.render({...mountedEntry.props,values:values(),all:all(),shown:shown()})}else refreshTrigger()};refreshTrigger();
    const open=event=>{event.preventDefault();event.stopPropagation();const props={label,values:values(),all:all(),shown:shown(),onChange:select};wrapper.replaceChildren();const root=window.pharosReaderLineTag(wrapper,props);mountedEntry={root,props,values};mounted.add(mountedEntry)};
    trigger.onclick=open;trigger.onkeydown=event=>{if(event.key==='ArrowDown'||event.key==='ArrowUp')open(event)};return wrapper};
  const bind=(entry,message)=>{const tag=index.tagOf(message);if(tag){bound.set(message,entry);entry.dataset.readerTag=tag;entry.dataset.tagHidden=String(transcriptTagHidden(hidden,tag))}};
  const refresh=message=>{if(!index.refresh(message))return;const entry=bound.get(message),tag=index.tagOf(message);if(entry&&tag)entry.dataset.readerTag=tag;for(const update of controllers)update();apply(true)};
  const changes=['pharos:uri-changed','pharos:view','pharos:route','popstate'],follow=()=>{if(host.isConnected)restoreTranscriptTags()};changes.forEach(name=>addEventListener(name,follow));
  conversationMapCleanups.push(()=>{keys.dispose();for(const entry of mounted)entry.root.dispose();changes.forEach(name=>removeEventListener(name,follow))});
  restoreTranscriptTags();return{host,tagOf:index.tagOf,group:index.group,button,bind,refresh,hiddenFor,hiddenTagsFor,nameOf,hasHidden:()=>all().some(id=>transcriptTagHidden(hidden,id)),clear:()=>select(all()),show:values=>select([...new Set([...shown(),...values])])};
}

// Long sequences render a page at a time. The rest follows when its control
// nears the viewport or is clicked, or when search, failure navigation, or a
// message link needs an event in it (pager.reveal).
const SEQUENCE_PAGE=150,SEQUENCE_INITIAL_PAGE=50;
function transcriptMaterialized(target,roots=[target]){target.dispatchEvent(new CustomEvent('transcript-materialized',{bubbles:true,detail:{roots}}))}
function renderSequence(messages,context){
  const fragment=document.createDocumentFragment(),visible=messages.filter(message=>!context.consumed.has(message.native_id)&&!context.ancestors?.has(message.native_id));
  let index=0,stop=visible.length;
  const renderNext=target=>{
    const message=visible[index];let entry;
    if(message.kind==='delegation'){
      const input=toolPayload(message).input,children=context.childrenByParent.get(message.call_id)||context.childrenByParent.get(message.native_id)||[],results=context.delegationResultsByCall.get(message.call_id)||[];
      const run=node('details','subagent-run conversation-event'),summary=node('summary',''),body=node('div','subagent-body'),failed=toolFailed(message,results),agentName=input.subagent_type||input.agent_type||input.name||input.task_name||'Sub-agent',prompt=input.description||input.prompt||input.task||toolPreview(message),assignment=input.prompt||input.task||input.description||prompt,stats=node('span','subagent-summary-stats');
      const target=node('span','activity-target',shortText([agentName,prompt].filter(Boolean).join(' · '),240));target.title=[agentName,prompt].filter(Boolean).join(' · ');
      summary.append(kindIcon('agent'),context.tagFilter.button('Delegate',()=>[context.tagFilter.tagOf(message)],'activity-title'),target,stats);
      if(failed)summary.append(node('span','activity-status badtext','Failed'));
      summary.append(node('span','line-details-hint','Details'));
      attachTokenBadge(summary,[message,...results],true);
      run.owned=ownedMessages([message],context);run.spendOwned=run.owned.filter(item=>item!==message&&!results.includes(item));
      run.materialize=()=>{if(run.classList.contains('materialized'))return;run.classList.add('materialized');run.removeAttribute('data-pharos-find-pending');if(assignment){const section=node('div','tool-detail-section');section.append(node('span','tool-detail-label','Assignment'),node('div','subagent-assignment',String(assignment)));body.append(section)}body.append(renderSequence(children,{...context,ancestors:new Set([...(context.ancestors||[]),message.native_id])}));const details=toolEvent(message,results,context.turnStart);details.classList.add('subagent-details');details.querySelector(':scope > summary').replaceChildren(node('span','','Recorded output and metadata'));body.append(details);transcriptMaterialized(body)};
      findLazily(run,()=>run.owned,()=>run.materialize());summary.addEventListener('click',()=>{if(!run.open)run.materialize()});run.addEventListener('toggle',()=>{if(run.open)run.materialize()});
      run.append(summary,body);entry=timelineEntry(run,message,context.turnStart,'subagent-entry');
    }else if(message.kind==='message'){entry=narrativeEvent(message,context.turnStart,context.workspace)}
    else{const group=[message],category=toolCategory(message);if(message.kind==='tool_call'&&!mcpIdentity(message)&&['read','search','edit'].includes(category)){while(index+1<stop&&visible[index+1].kind==='tool_call'&&!mcpIdentity(visible[index+1])&&toolCategory(visible[index+1])===category&&readerTimelineCategory(visible[index+1])===readerTimelineCategory(message)&&toolLabel(visible[index+1])===toolLabel(message)){group.push(visible[++index])}}entry=timelineEntry(actionRow(group,context),message,context.turnStart,'tool-entry')}
    entry.dataset.show=transcriptCategory(message,context);context.tagFilter.bind(entry,message);target.append(entry);index++;
  };
  const page=(target,start,end)=>{index=start;stop=end;while(index<stop)renderNext(target)};
  page(fragment,0,Math.min(SEQUENCE_INITIAL_PAGE,visible.length));
  const firstEnd=index;
  if(firstEnd<visible.length){
    const gaps=new Set();
    // Re-observe the remaining gap after a page. IntersectionObserver measures
    // it after layout, without a synchronous geometry read after rail writes.
    const follow=gap=>{if(!gaps.has(gap))return;const next=pager.expand(gap);if(next&&!next.earlier){observer.unobserve(next.button);requestAnimationFrame(()=>{if(gaps.has(next)&&next.button.isConnected)observer.observe(next.button)})}};
    const observer=new IntersectionObserver(entries=>{for(const entry of entries)if(entry.isIntersecting)follow(entry.target.sequenceGap)},{rootMargin:'600px'});
    conversationMapCleanups.push(()=>observer.disconnect());
    const label=gap=>{const left=gap.end-gap.start;gap.button.textContent=`Show ${Math.min(SEQUENCE_PAGE,left).toLocaleString()} ${gap.earlier?'earlier':'more'} · ${left.toLocaleString()} events not shown`};
    const gapControl=(start,end,earlier=false)=>{
      const button=node('button','sequence-more'),gap={start,end,earlier,button};
      button.type='button';label(gap);
      button.sequenceGap=gap;findLazily(button,()=>visible.slice(gap.start,gap.end).flatMap(message=>ownedMessages([message],context)),predicate=>pager.reveal(predicate));button.onclick=()=>{const at=gap.earlier?gap.end-1:gap.start,message=visible[at];if(message?.id)saveViewContent('loaded:'+message.id,true);pager.expand(gap)};gaps.add(gap);
      // Earlier gaps are explicit controls: automatically filling them after a
      // jump would render the history we just skipped and move the target.
      if(!earlier)observer.observe(button);
      return gap;
    };
    const materialize=(gap,ranges)=>{
      if(!gaps.has(gap))return null;
      const parent=gap.button.parentElement,next=document.createDocumentFragment();let cursor=gap.start,following=null;
      if(ranges.length===1&&ranges[0][0]===gap.start&&ranges[0][1]<gap.end){
        page(next,...ranges[0]);const roots=[...next.children];gap.start=index;gap.button.before(next);label(gap);
        if(parent)transcriptMaterialized(parent,roots);return gap;
      }
      for(const [start,end]of ranges){if(cursor<start)next.append(gapControl(cursor,start,true).button);page(next,start,end);cursor=index}
      if(cursor<gap.end){following=gapControl(cursor,gap.end,gap.earlier);next.append(following.button)}
      const roots=[...next.children];observer.unobserve(gap.button);gaps.delete(gap);gap.button.replaceWith(next);
      if(!gaps.size){observer.disconnect();context.pagers?.delete(pager)}
      if(parent)transcriptMaterialized(parent,roots);return following;
    };
    const pager={
      expand(gap){return gap.earlier?materialize(gap,[[Math.max(gap.start,gap.end-SEQUENCE_PAGE),gap.end]]):materialize(gap,[[gap.start,Math.min(gap.end,gap.start+SEQUENCE_PAGE)]])},
      reveal(predicate){
        for(const gap of [...gaps]){
          const ranges=[];
          for(let i=gap.start;i<gap.end;i++)if(ownedMessages([visible[i]],context).some(predicate)){
            const start=Math.max(gap.start,Math.floor(i/SEQUENCE_PAGE)*SEQUENCE_PAGE),end=Math.min(gap.end,start+SEQUENCE_PAGE),previous=ranges.at(-1);
            if(previous&&start<=previous[1])previous[1]=Math.max(previous[1],end);else ranges.push([start,end]);
          }
          if(ranges.length)materialize(gap,ranges);
        }
      }
    };
    context.pagers?.add(pager);fragment.append(gapControl(firstEnd,visible.length).button);
  }
  return fragment;
}
const conversationMapCleanups=[];
function conversationScroller(panel){return panel.closest('.workspace-reader-content')||document.querySelector('#detail .workspace-reader-content')||pageScroller}
function setupConversationMap(panel,list){
  if(!panel.isConnected)return;
  let dispose;
  const mount=()=>{if(!panel.isConnected||dispose||!window.pharosReaderNavigator)return;dispose=window.pharosReaderNavigator.mount(panel,panel.navigatorData)};
  mount();addEventListener('pharos:reader-navigator-ready',mount);
  conversationMapCleanups.push(()=>{removeEventListener('pharos:reader-navigator-ready',mount);dispose?.()});
}
function githubPullURL(remote,number){const match=String(remote||'').match(/github\.com(?::|\/)([^/\s]+\/[^/\s]+?)(?:\.git)?$/i);return match?`https:${'//'}github.com/${match[1].replace(/\.git$/i,'')}/pull/${number}`:''}
function workPullRequests(work){
  const found=new Map(),add=value=>{if(!value?.number)return;const host=value.host||(/github\.com/i.test(value.url||'')?'github.com':''),key=`${host}:${value.number}`,prior=found.get(key)||{};found.set(key,{...value,...prior,url:prior.url||value.url||((!host||host==='github.com')?githubPullURL(work.canonical_remote,value.number):'')})};
  (work.prs||[]).forEach(add);
  try{const details=typeof work.pr_details==='string'?JSON.parse(work.pr_details):work.pr_details;(details||[]).forEach(add)}catch{}
  // The service scans every conversation's messages for pull request links.
  for(const pr of work.discovered_prs||[])add({...pr,state:'observed',relationship:'Discovered in conversation'});
  return [...found.values()]
}
function mainIntegrationLink(work){const label=`${work.main_merge_method==='merge'?'Merged':'On main'}: ${work.main_merge_title||String(work.main_merge_commit).slice(0,12)}`,url=/^https:\/\/github\.com\//i.test(work.main_merge_url||'')?work.main_merge_url:'';const link=node(url?'a':'span','chip',label);link.title=work.main_merge_commit||'';if(url){link.href=url;link.target='_blank';link.rel='noopener noreferrer'}return link}
function toolUseSummary(messages){const calls=messages.filter(message=>message.kind==='tool_call'),results=new Map();for(const message of messages)if(['tool_result','error'].includes(message.kind)&&message.call_id){if(!results.has(message.call_id))results.set(message.call_id,[]);results.get(message.call_id).push(message)}const counts=new Map();for(const message of calls){const payload=toolPayload(message),name=payload.name||'Unknown tool',entry=counts.get(name)||{calls:0,failures:0};entry.calls++;if(payload.raw?.is_error||(results.get(message.call_id)||[]).some(result=>result.kind==='error'||toolPayload(result).raw?.is_error||Number(toolPayload(result).raw?.exit_code)>0))entry.failures++;counts.set(name,entry)}return [...counts].sort((a,b)=>a[0].localeCompare(b[0]))}
// Local labels (labels.go): taxonomy rows the loaded conversations name, kept
// for cards rendered later and for naming a hovered message's labels.
const labelTaxonomy=new Map();
function rememberLabelTaxonomy(conversation){for(const row of conversation.labels?.taxonomy||[])labelTaxonomy.set(row.id,row)}
// The same legend keys control whole highlight kinds and their individual hues.
// Boolean fields retain existing links; reader_highlight_hidden refines a kind.
function readerHighlightLinks(){return window.pharosView?{reader_writing:window.pharosView.read('reader_writing',null),reader_events:window.pharosView.read('reader_events',null),reader_labels:window.pharosView.read('reader_labels',null)}:{}}
function transcriptHighlightControl(panel,conversation,messages){
  rememberLabelTaxonomy(conversation);
  const options=readerHighlightOptions(conversation,messages,labelTaxonomy);if(!options.length)return null;
  const box=node('div','transcript-highlights'),row=node('div','transcript-highlight-options'),host=node('div','reader-highlight-keys'),notes=node('div','transcript-key-notes');
  if(!panel.classList.contains('conversation-panel')){box.classList.add('transcript-key','transcript-key-preview');const heading=node('div','transcript-key-heading');heading.append(node('h3','','Key'));box.append(heading)}
  row.setAttribute('role','group');row.setAttribute('aria-label','Highlight in transcript');row.append(host);box.append(row);
  const keyOf=(option,item)=>option.id+':'+(item.family??item.className),values=option=>option.legend.length?option.legend.map(item=>keyOf(option,item)):[option.id];
  const items=options.map(option=>{
    const children=option.legend.map(item=>({id:keyOf(option,item),label:item.name,values:[keyOf(option,item)],className:item.className,family:item.family}));
    if(option.id==='labels')for(const [prefix,label]of [['P.','Sender'],['C.','Receiver']]){
      const members=children.filter(item=>item.family?.startsWith(prefix));if(!members.length)continue;
      const at=children.indexOf(members[0]);for(const member of members)children.splice(children.indexOf(member),1);
      children.splice(at,0,{id:prefix,label,values:members.flatMap(item=>item.values),children:members});
    }
    return{id:option.id,label:option.label,title:option.title,values:values(option),available:option.available,note:option.note,...(option.id==='writing'?{className:'human-writing-legend'}:{}),...(children.length?{children}:{})};
  });
  // Human writing is a message mark too: once messages are labeled, it leads
  // the Message labels group, so that group switches every message mark.
  const writing=items.find(item=>item.id==='writing'),labelGroup=items.find(item=>item.id==='labels'&&item.available&&item.children);
  if(writing&&labelGroup){items.splice(items.indexOf(writing),1);labelGroup.children.unshift(writing);labelGroup.values=[...writing.values,...labelGroup.values]}
  for(const option of options)if(option.available&&option.note)notes.append(node('span','transcript-highlight-note',option.label+': '+option.note));if(notes.childNodes.length)box.append(notes);
  const all=options.filter(option=>option.available).flatMap(values),keys=window.pharosReaderKeys(host,panel.classList.contains('conversation-panel')),links=eventLinks(messages);let visible=new Set(),lastVisibility=null;
  panel.eventLinkVisible=id=>eventLinkVisible(eventLinkFor(links,id),visible);
  const applyMarks=root=>{
    const elements=selector=>[...(root.matches?.(selector)?[root]:[]),...root.querySelectorAll(selector)];
    for(const card of elements('[data-label-family]'))if(card.labelMessage)card.classList.toggle('reader-label-hidden',!visible.has('labels:'+card.dataset.labelFamily));
    for(const badge of elements('.event-badge')){
      const events=readerEventHighlights(badge.eventView||[],visible),text=`${events.length} event${events.length===1?'':'s'}`;
      badge.classList.toggle('reader-event-hidden',!events.length);
      if(badge.lastChild?.textContent!==text)badge.replaceChildren(node('i'),text);
      badge.title=[...new Set(events.map(event=>event.label))].join(', ');
      for(const [group]of EVENT_GROUPS)badge.classList.toggle(eventGroupClass(group),events[0]?.group===group);
    }
    for(const mark of elements('mark.agent-event,mark.event-context')){
      const events=mark.closest('[data-agent-events]')?.eventView||[];
      const shown=readerEventHighlights(eventsAtMark(events,mark),visible);mark.classList.toggle('reader-event-hidden',!shown.length);
      if(mark.classList.contains('agent-event'))for(const [group]of EVENT_GROUPS)mark.classList.toggle(eventGroupClass(group),shown[0]?.group===group);
    }
    for(const mark of elements('mark.event-target,.event-link-n')){
      const ids=(mark.dataset.eventTargets||mark.dataset.eventLink||'').split(' '),isTarget=mark.matches('mark.event-target,.is-target');
      mark.classList.toggle('reader-event-hidden',isTarget&&!visible.has('events:event-target-legend')||!ids.some(panel.eventLinkVisible));
    }
  };
  const apply=()=>{
    const shown=readerHighlightsShown(options,field=>readerHighlightLinks()[field]??null,(pref,fallback)=>pharosPrefs.get(pref,fallback));
    const hidden=window.pharosView?.read('reader_highlight_hidden',pharosPrefs.get('pharos-reader-highlight-hidden',[]))||[];
    visible=new Set(options.filter(option=>shown.has(option.id)).flatMap(values).filter(key=>!hidden.includes(key)));
    for(const option of READER_HIGHLIGHTS)panel.classList.toggle('show-'+option.id,shown.has(option.id));
    keys.render({items,all,shown:[...visible],groupToggle:panel.classList.contains('conversation-panel'),onChange:(next,allKinds=false)=>{
      const selected=new Set(next),updates={reader_highlight_hidden:readerHighlightExclusions(hidden,all,next)};
      for(const option of allKinds?READER_HIGHLIGHTS:options.filter(option=>option.available)){const on=allKinds?next.length>0:values(option).some(key=>selected.has(key));updates[option.field]=on;pharosPrefs.set(option.pref,on)}
      pharosPrefs.set('pharos-reader-highlight-hidden',updates.reader_highlight_hidden);window.pharosView?.writeAll(updates);apply();
    }});
    hideLabelTip();const signature=JSON.stringify([...visible]);if(signature!==lastVisibility){lastVisibility=signature;applyMarks(panel)}
    window.pharosView?.writeAll({...Object.fromEntries(options.filter(option=>option.available).map(option=>[option.field,shown.has(option.id)])),reader_highlight_hidden:hidden});
  };
  panel.restoreHighlights=apply;
  panel.revealEventLinkHighlights=link=>{
    const hidden=window.pharosView?.read('reader_highlight_hidden',pharosPrefs.get('pharos-reader-highlight-hidden',[]))||[];
    const required=new Set([...eventLinkHighlightKeys(link),'events:event-target-legend']);
    window.pharosView?.writeAll({reader_events:true,reader_highlight_hidden:hidden.filter(key=>!required.has(key))});apply();
  };
  const changes=['pharos:uri-changed','pharos:view','popstate'],follow=()=>{if(box.isConnected)apply()};
  for(const name of changes)addEventListener(name,follow);
  // Covers deferred turns, tool bodies, and Browse switching its selected turn.
  const observer=new MutationObserver(records=>{for(const record of records)for(const root of record.addedNodes)if(root.nodeType===1)applyMarks(root)});observer.observe(panel,{childList:true,subtree:true});
  box.dispose=()=>{keys.dispose();observer.disconnect();for(const name of changes)removeEventListener(name,follow);panel.removeEventListener('pointerover',hover);panel.removeEventListener('pointermove',move);panel.removeEventListener('pointerdown',hideLabelTip);panel.removeEventListener('pointerleave',hideLabelTip)};
  const hover=event=>{
    if(event.buttons){hideLabelTip();return}
    const events=panel.classList.contains('show-events'),labels=panel.classList.contains('show-labels');
    const eventMark=events&&event.target.closest?.('mark.agent-event:not(.reader-event-hidden),mark.event-context:not(.reader-event-hidden)'),eventCard=eventMark&&eventMark.closest('[data-agent-events]');
    if(eventCard?.eventView){showEventTip(readerEventHighlights(eventsAtMark(eventCard.eventView,eventMark),visible),event);return}
    const writingMark=visible.has('writing')&&event.target.closest?.('mark.human-written');
    if(writingMark){showWritingTip(writingMark,event);return}
    // Only the tinted background shows message labels, leaving text and controls unobscured.
    const card=labels&&event.target;
    if(card?.classList?.contains('reader-label-hidden')){hideLabelTip();return}
    if(card?.labelMessage)showLabelTip(messageLabelView(card.labelMessage,labelTaxonomy),event);else hideLabelTip();
  },move=event=>{if(event.buttons)hideLabelTip();else if(labelTip&&!labelTip.hidden)placeLabelTip(event)};
  panel.addEventListener('pointerdown',hideLabelTip);panel.addEventListener('pointerover',hover);panel.addEventListener('pointermove',move);panel.addEventListener('pointerleave',hideLabelTip);
  apply();return box;
}
/** The verifier's response span, only when it is a clause of its own: not inside what was said or what it was about, and not a fragment. */
function eventFollowUp(item){const text=(item.responseText||'').trim(),said=item.sentenceText||'',about=item.subjectText||'';if(text.split(/\s+/).length<3||said.includes(text)&&text.length<said.length*.6||about.includes(text)||text.includes(said))return '';return text}
function showEventTip(events,event){if(!events.length){hideLabelTip();return}
  if(!labelTip){labelTip=node('div','label-tip');labelTip.setAttribute('role','tooltip');document.body.append(labelTip)}
  labelTip.replaceChildren();
  // Every label of the mark, then its text once (eventTipView), however many events share it.
  const view=eventTipView(events,eventFollowUp),details=node('dl','event-tip-details');
  for(const item of view.labels){const row=node('div','event-tip-row '+eventGroupClass(item.group));row.append(node('i'),node('strong','',item.label),node('span','',item.probability==null?'':`${Math.round(item.probability*100)}%`));labelTip.append(row)}
  for(const [name,values] of view.fields){details.append(node('dt','',name));for(const value of values)details.append(node('dd','',shortText(value,180)))}
  labelTip.append(details);
  labelTip.hidden=false;placeLabelTip(event);
}
let labelTip=null;
function showWritingTip(mark,event){
  if(!labelTip){labelTip=node('div','label-tip');labelTip.setAttribute('role','tooltip');document.body.append(labelTip)}
  labelTip.replaceChildren(node('strong','','Human writing'),node('p','label-tip-segment','Estimated as text typed or dictated by a person, after checking for generated, pasted, quoted and re-sent text.'),node('div','',shortText(mark.textContent,180)));
  labelTip.hidden=false;placeLabelTip(event);
}
function showLabelTip(view,event){
  if(!labelTip){labelTip=node('div','label-tip');labelTip.setAttribute('role','tooltip');document.body.append(labelTip)}
  labelTip.replaceChildren();if(view.segmentName)labelTip.append(node('div','label-tip-segment',view.segmentName));
  for(const label of view.labels){const row=node('div','label-tip-row');row.dataset.labelFamily=label.family;row.append(node('i'),node('strong','',label.title),node('span','',label.prob==null?'':`${Math.round(label.prob*100)}%`));if(label.definition)row.append(node('p','',label.definition));labelTip.append(row)}
  labelTip.hidden=false;placeLabelTip(event);
}
function placeLabelTip(event){const box=labelTip.getBoundingClientRect(),gap=14;let left=event.clientX+gap,top=event.clientY+gap;if(left+box.width>innerWidth-8)left=Math.max(8,event.clientX-gap-box.width);if(top+box.height>innerHeight-8)top=Math.max(8,event.clientY-gap-box.height);labelTip.style.left=left+'px';labelTip.style.top=top+'px'}
function hideLabelTip(){if(labelTip)labelTip.hidden=true}
// Two-sided events (eventLinks in reader-model.js), shown with the Agent
// events highlight: the observation keeps its event highlight, its target
// gets a dashed underline in link ink, and both ends carry the pair's number.
// A message's notes list the other end of each pair. Hovering either end
// lights both and draws a gutter arc between them; clicking selects the pair
// (reader_event_link) and reveals the other end: scroll and highlight, never
// keyboard focus. A copied link restores the selection.
const eventLinkContext=new WeakMap();
function rememberEventLinks(messages){
  const links=eventLinks(messages),byTarget=new Map();if(!links.length)return links;
  for(const link of links){const list=byTarget.get(link.target.message_id)||[];list.push(link);byTarget.set(link.target.message_id,list)}
  for(const message of messages)eventLinkContext.set(message,{links,targets:byTarget.get(message.id)||[]});
  return links;
}
const messageEventTargets=message=>eventLinkContext.get(message)?.targets||[];
function decorateEventLinks(container,message,events,spans){
  const context=eventLinkContext.get(message);if(!context)return;
  // A pair may hold several events (eventLinks): any of their ids finds it, and marks name the pair's own id.
  const byId=new Map(context.links.flatMap(link=>(link.ids||[link.id]).map(id=>[id,link])));
  if(events?.length)for(const mark of container.querySelectorAll('mark.agent-event')){const ids=[...new Set(eventsAtMark(events,mark).flatMap(event=>byId.has(event.id)?[byId.get(event.id).id]:[]))];if(ids.length){mark.dataset.eventLinks=ids.join(' ');mark.classList.add('is-linked')}}
  for(const mark of container.querySelectorAll('mark.event-target')){const at=/event-target-at-(\d+)/.exec(mark.className);const ids=(spans[Number(at?.[1])]?.links||[]).filter(id=>byId.has(id));if(ids.length)mark.dataset.eventTargets=ids.join(' ')}
  // One number per pair, after the last mark of its span.
  const last=new Map();for(const mark of container.querySelectorAll('mark[data-event-links],mark[data-event-targets]'))last.set(mark.dataset.eventLinks?'observation '+mark.dataset.eventLinks:'target '+mark.dataset.eventTargets,mark);
  for(const [key,mark] of last){const [side,...ids]=key.split(' ');let after=mark;for(const id of ids){const link=byId.get(id),badge=node('button',`event-link-n ${side==='target'?'is-target':'is-observation'} ${eventGroupClass(link.group)}`,String(link.n));badge.type='button';badge.dataset.eventLink=id;badge.dataset.side=side;badge.dataset.viewField='reader_event_link';
    badge.title=eventLinkTitle(link,side);badge.setAttribute('aria-label',badge.title);after.after(badge);after=badge}}
}
/** What a pair's badge says about the other end: the event's kind, where its target is and how it was found. */
function eventLinkTitle(link,side){
  const where=link.target.role==='user'?'your prompt':['thinking','reasoning'].includes(link.target.kind)?'earlier thinking':'an earlier reply';
  const how=link.target.method==='quote'?(link.weak?'weak quote':'quote'):link.target.method==='file'?'named file':'shared words';
  const label=(link.labels||[link.label]).join(' · ');
  return side==='target'?`${link.n} · ${label} later points back at this (${how}): ${shortText(link.sentenceText,160)}`:`${link.n} · ${label}, about ${where} (${how}): “${shortText(link.target.text,120)}”`;
}
let eventLinkArrows=0;
function bindEventLinks(panel,links){
  if(!links.length)return null;
  const ends=id=>panel.querySelectorAll(`[data-event-links~="${CSS.escape(id)}"],[data-event-targets~="${CSS.escape(id)}"],[data-event-link="${CSS.escape(id)}"]`);
  const gutter=document.createElementNS('http://www.w3.org/2000/svg','svg');gutter.classList.add('event-link-gutter');gutter.setAttribute('aria-hidden','true');panel.append(gutter);
  let hovered=null,selected=null;
  const arc=id=>{const o=panel.querySelector(`mark[data-event-links~="${CSS.escape(id)}"]`),t=panel.querySelector(`mark[data-event-targets~="${CSS.escape(id)}"]`);if(panel.eventLinkVisible?.(id)===false||!o||!t||o.classList.contains('reader-event-hidden')||t.classList.contains('reader-event-hidden')||!o.getClientRects().length||!t.getClientRects().length)return '';
    const box=panel.getBoundingClientRect(),a=t.getClientRects()[0],b=o.getClientRects()[0],yt=a.top-box.top+a.height/2,yo=b.top-box.top+b.height/2,x=6,lead=(el,r)=>Math.max(x+8,Math.min(r.left,(el.closest('p,li,blockquote,pre,td,.message-prose,.markdown-content')||el).getBoundingClientRect().left)-box.left-4),xt=lead(t,a),xo=lead(o,b),r=Math.min(6,Math.abs(yo-yt)/2),dir=Math.sign(yo-yt)||1;
    return `M${xo} ${yo}H${x+r}Q${x} ${yo} ${x} ${yo-dir*r}V${yt+dir*r}Q${x} ${yt} ${x+r} ${yt}H${xt}`};
  const arrow=`event-link-arrow-${++eventLinkArrows}`;
  const draw=()=>{const box=panel.getBoundingClientRect();gutter.setAttribute('width',String(box.width));gutter.setAttribute('height',String(panel.scrollHeight));
    gutter.replaceChildren();gutter.insertAdjacentHTML('afterbegin',`<defs><marker id="${arrow}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" markerUnits="userSpaceOnUse" orient="auto"><path d="M0 0L10 5L0 10z"/></marker></defs>`);
    for(const [id,kind] of [[selected,'is-selected'],[hovered,'is-hover']]){if(!id||(kind==='is-hover'&&id===selected))continue;const d=arc(id);if(!d)continue;const link=links.find(item=>item.id===id),path=document.createElementNS('http://www.w3.org/2000/svg','path');path.setAttribute('d',d);path.setAttribute('marker-end',`url(#${arrow})`);path.setAttribute('class',`${kind}${link?.weak?' is-weak':''}`);gutter.append(path)}};
  const focus=id=>{if(hovered===id)return;hovered=id;panel.classList.toggle('has-link-focus',!!id);panel.querySelectorAll('.link-focus').forEach(el=>el.classList.remove('link-focus'));if(id)ends(id).forEach(el=>el.classList.add('link-focus'));draw()};
  const mark=id=>{selected=id;panel.querySelectorAll('.link-selected').forEach(el=>el.classList.remove('link-selected'));if(id)ends(id).forEach(el=>el.classList.add('link-selected'));draw()};
  // Reveal one end: render its turn, open what hides it, scroll and highlight.
  const reveal=(id,side,tries=0,smooth=true)=>{const end=eventLinkEnd(links,id,side);if(!end)return;const want=end.side;
    if(!tries)panel.reveal?.(message=>message.id===end.message);
    const selector=want==='target'?`mark[data-event-targets~="${CSS.escape(id)}"]`:`mark[data-event-links~="${CSS.escape(id)}"]`,found=panel.querySelector(selector)||(tries>3?panel.querySelector(`[data-message-id="${CSS.escape(end.message)}"]`):null);
    if(!found){if(tries<8){const host=panel.querySelector(`[data-message-id="${CSS.escape(end.message)}"]`);for(let el=host;el&&el!==panel;el=el.parentElement)if(el.tagName==='DETAILS')el.open=true;requestAnimationFrame(()=>reveal(id,side,tries+1,smooth))}return}
    // Select first: expanding prose writes the URL, which re-enters restore.
    // Both ends are opened so the gutter arc reaches text, not a fold.
    mark(id);for(const end of new Set([found,...ends(id)]))for(let el=end;el&&el!==panel;el=el.parentElement){if(el.tagName==='DETAILS')el.open=true;if(el.classList?.contains('prose-truncated'))el.pharosFindExpand?.()}
    exemptFromTranscriptFilter(panel,found);found.classList.add('link-reveal');setTimeout(()=>found.classList.remove('link-reveal'),2400);
    requestAnimationFrame(()=>{if(found.isConnected){found.scrollIntoView({block:'center',behavior:smooth?'smooth':'auto'});setTimeout(draw,400)}})};
  const idAt=target=>{const el=target.closest?.('[data-event-link],mark[data-event-links],mark[data-event-targets]');if(!el||el.classList.contains('reader-event-hidden'))return null;const id=(el.dataset.eventLink||el.dataset.eventLinks||el.dataset.eventTargets).split(' ').find(id=>panel.eventLinkVisible?.(id)!==false);return id?{id,side:el.dataset.side||(el.dataset.eventTargets!==undefined&&!el.dataset.eventLinks?'target':'observation')}:null};
  panel.addEventListener('pointerover',event=>{if(!panel.classList.contains('show-events'))return;focus(idAt(event.target)?.id||null)});
  panel.addEventListener('pointerleave',()=>focus(null));
  panel.addEventListener('click',event=>{if(!panel.classList.contains('show-events'))return;const hit=idAt(event.target);if(!hit)return;event.preventDefault();mark(hit.id);window.pharosView?.write('reader_event_link',hit.id);reveal(hit.id,hit.side)});
  // The URL owns the selection: restore it on load, Back/Forward and links.
  // Any merged event's id selects its pair.
  const restore=()=>{const id=window.pharosView?.read('reader_event_link',null)||null,link=id?eventLinkFor(links,id):null;if((link?.id??id)===selected)return;if(!link){mark(null);return}
    // Record the selection before writing its required highlights: URL observers
    // re-enter restore, and later manual exclusions must remain deliberate.
    selected=link.id;panel.revealEventLinkHighlights?.(link);reveal(link.id,'target',0,false)};
  const changes=['pharos:uri-changed','pharos:view','popstate'],follow=()=>{if(panel.isConnected){restore();redraw()}};
  for(const name of changes)addEventListener(name,follow);
  // Turns render and expand after the selection is drawn; redraw when the transcript moves.
  let frame=0;const redraw=()=>{if(frame||(!selected&&!hovered))return;frame=requestAnimationFrame(()=>{frame=0;draw()})},sizes=new ResizeObserver(redraw);sizes.observe(panel);addEventListener('resize',redraw);
  requestAnimationFrame(()=>requestAnimationFrame(restore));
  return ()=>{for(const name of changes)removeEventListener(name,follow);removeEventListener('resize',redraw);sizes.disconnect();cancelAnimationFrame(frame)};
}
function claudeSelectedSettingsFacts(settings){
  const groups=new Map(),facts=[];
  for(const observation of settings?.observations||[]){
    const org=observation.organization_uuid,at=Date.parse(observation.observed_at),group=groups.get(org);
    if(group&&at<group.at)continue;
    if(!group||at>group.at)groups.set(org,{at,rows:[]});
    const rows=groups.get(org).rows,selection=JSON.stringify([observation.model,observation.effort_level,observation.thinking_mode]);
    if(!rows.some(row=>row.selection===selection))rows.push({...observation,selection});
  }
  for(const [org,{rows}]of groups){
    for(const observation of rows)facts.push([rows.length>1?'Conflicting observed selection':'Observed conversation selection',`${observation.model??'Unknown model'} · effort ${observation.effort_level??'unknown'} · thinking ${observation.thinking_mode??'unknown'}`]);
    facts.push(['Selection observed',`${rows[0].observed_at} (${rows[0].time_basis}) · organization ${org}`]);
  }
  if(facts.length)facts.push(['Selection scope','Saved conversation settings; individual messages may have used different settings.']);
  return facts;
}
function renderConversation(conversation,repoRoot=''){rememberLabelTaxonomy(conversation);const panel=node('section','panel conversation-panel'),head=node('div','conversation-head'),titleRow=node('div','conversation-title-row'),title=node('div','conversation-title-content'),messages=conversationMessages(conversation),eventLinkList=rememberEventLinks(messages),graph=conversationGraph(messages),turnCount=graph.turns.length,toolCount=messages.filter(m=>m.kind==='tool_call').length,delegationCount=messages.filter(m=>m.kind==='delegation').length,provider=conversation.provider||'agent';{const line=node('div','conversation-title-line'),name=conversation.title||shortText(displayText({role:'user',text:conversation.first_prompt||''}),120);line.append(node('h2','',`${provider} conversation`),copyValues(name?copyValueButton('Name',name,'conversation name'):null,conversation.id?copyValueButton('ID',conversation.id,'conversation ID'):null));title.append(line)}const chips=node('div','chips'),toolTrigger=node('button','chip tool-summary-trigger',`${toolCount} tool call${toolCount===1?'':'s'}`),toolSummary=node('section','tool-summary');toolTrigger.type='button';toolTrigger.setAttribute('aria-expanded','false');toolTrigger.setAttribute('aria-controls','tool-use-summary');toolSummary.id='tool-use-summary';toolSummary.hidden=true;toolSummary.setAttribute('aria-label','Tool use summary');toolSummary.append(node('h3','','Tool use summary'));const toolTable=node('table','tool-summary-table'),toolHead=node('thead',''),toolHeader=node('tr','');for(const label of ['Tool','Calls','Failures'])toolHeader.append(node('th','',label));toolHead.append(toolHeader);const toolBody=node('tbody','');for(const [name,data]of toolUseSummary(messages)){const row=node('tr','');row.append(node('td','',name),node('td','',String(data.calls)),node('td','',String(data.failures)));toolBody.append(row)}toolTable.append(toolHead,toolBody);toolSummary.append(toolTable);const applySummary=open=>{toolSummary.hidden=!open;toolTrigger.setAttribute('aria-expanded',String(open))};bindViewContent(toolTrigger,'summary:'+conversation.id,false,applySummary);toolTrigger.onclick=()=>{const open=toolSummary.hidden;applySummary(open);saveViewContent(toolTrigger.dataset.viewContent,open)};if(conversation.harness_version_last){const version=conversation.harness_version_first&&conversation.harness_version_first!==conversation.harness_version_last?`${conversation.harness_version_first} → ${conversation.harness_version_last}`:conversation.harness_version_last;chips.append(node('span','chip',`${conversation.harness||'Agent app'} ${version}`))}for(const [label,value,status] of [['Channel',({conductor:'Conductor',tl1:'TL1',cli:'CLI',desktop:'Desktop app',web:'Web',ide:'IDE',api:'API / integration'})[conversation.channel]||conversation.channel,conversation.channel_status],['Machine',conversation.machine||conversation.machine_id,conversation.machine_status],['Execution machine',conversation.execution_machine||conversation.execution_machine_id,conversation.execution_machine_status]]){if(label!=='Channel'&&(!value||String(value).trim().toLowerCase()==='unknown'))continue;const chip=node('span','chip',`${label}: ${value||'Unknown'}`);chip.title=`${status||'unknown'}${conversation.origin_evidence?' · '+conversation.origin_evidence:''}`;chips.append(chip)}chips.append(node('span','chip',`${turnCount} turn${turnCount===1?'':'s'}`),toolTrigger);if(delegationCount)chips.append(node('span','chip',`${delegationCount} sub-agent${delegationCount===1?'':'s'}`));title.append(chips,toolSummary);titleRow.append(node('span','provider-mark',provider.slice(0,2)),title);const source=node('details','source-details'),sourceSummary=node('summary','','Source details'),facts=node('dl','');for(const [key,value]of[['Agent app',conversation.harness],['First recorded agent app version',conversation.harness_version_first],['Last recorded agent app version',conversation.harness_version_last],['Version source',conversation.harness_version_source],['Effort',conversation.effort],['Coverage',conversation.coverage],['Native ID',conversation.native_id],['Origin',conversation.origin],...claudeSelectedSettingsFacts(conversation.selected_settings)])if(value)facts.append(node('dt','',key),node('dd','',value));source.dataset.pharosDisclosure='section:conversation:'+conversation.id+':source';source.append(sourceSummary,facts);chips.append(source);const toolbar=node('div','conversation-toolbar'),filter=node('input','');filter.type='search';filter.placeholder='Search prompts, replies, tools, and files…';filter.setAttribute('aria-label','Search this conversation');filter.hidden=true;const find=node('button','button-with-icon','Find'),expand=node('button','button-with-icon','Open All Turns'),collapse=node('button','button-with-icon','Close All Turns');find.type='button';find.title='Find on this page (Ctrl+F)';find.onclick=()=>window.pharosFind?.open();toolbar.append(find,filter,expand,collapse);titleRow.append(toolbar);head.append(titleRow);const list=node('div','turn-list'),{childrenByParent,delegationResultsByCall,toolResultsByCall,consumed,turns}=graph,baseContext={workspace:conversation.workspace_id||'',repoRoot:conversationRoots(conversation,repoRoot),fileLinks:fileLinkIndex(conversation.file_links),artifactLinks:conversation.artifact_links||{},childrenByParent,delegationResultsByCall,toolResultsByCall,consumed};describeTodoChanges(messages,baseContext);panel.transcript=transcriptStructure(messages);baseContext.finalReplies=panel.transcript.finalReplies;panel.tagFilter=baseContext.tagFilter=transcriptTagControl(panel,messages,baseContext);const lazy=turns.length>40||messages.length>300;panel.dataset.deferred=String(lazy);{const visited=new Set();turns.forEach(turn=>{turn.owned=ownedMessages([turn.human,...turn.events],baseContext,visited)})}panel.tokenModel={messages,turns,childrenByParent,delegationResultsByCall,toolResultsByCall};const renderers=[],turnPagers=[];turns.forEach((turn,index)=>{const details=node('details','turn');details.dataset.pharosDisclosure='message:'+(turn.human?.id||turn.owned[0]?.id||conversation.id);details.open=turns.length===1||!lazy||index<2&&turn.events.length<=100;const summary=node('summary',''),summaryText=node('div','turn-summary-text'),stats=node('div','turn-summary-stats'),prompt=shortText(messageSummary(turn.human)||'Agent setup',130),agentMessages=turn.events.filter(m=>m.kind==='message'),activities=turn.events.filter(m=>m.kind!=='message'),outcome=agentMessages.length?shortText(messageSummary(agentMessages[agentMessages.length-1]),120):'',turnStart=turn.human?.created_at||turn.events[0]?.created_at;details.dataset.human=String(Boolean(turn.human));details.dataset.prompt=prompt;const turnNumber=node('span','turn-number',`Turn ${index+1}${turn.human?' · You':''}`),age=relativeAgeLabel(turnStart);if(age)turnNumber.append(' · ',age);summaryText.append(turnNumber,node('div','turn-summary-prompt',prompt),node('div','turn-summary-outcome',outcome||timeLabel(turnStart)));for(const effort of [...new Set(turn.owned.map(message=>message.effort).filter(Boolean))])stats.append(node('span','mini-count',`Effort: ${effort}`));if(agentMessages.length)stats.append(node('span','mini-count',`${agentMessages.length} repl${agentMessages.length===1?'y':'ies'}`));if(activities.length)stats.append(node('span','mini-count',`${activities.length} activit${activities.length===1?'y':'ies'}`));summary.append(summaryText,stats);tokenColumn(summary);const body=node('div','turn-body'),pagers=new Set(),context={...baseContext,turnStart,pagers};turnPagers.push(pagers);let rendered=false;const renderBody=()=>{if(rendered)return;rendered=true;details.removeAttribute('data-pharos-find-pending');if(turn.human){const human=narrativeEvent(turn.human,turnStart,context.workspace);human.dataset.show='prompts';body.append(human)}body.append(renderSequence(turn.events,context));transcriptMaterialized(body)};renderers.push(renderBody);if(details.open)renderBody();else{findLazily(details,()=>turn.owned,predicate=>revealTurn(index,predicate));summary.addEventListener('click',()=>{if(!details.open)renderBody()});details.addEventListener('toggle',()=>{if(details.open)renderBody()},{once:true})}details.append(summary,body);list.append(details)});const turnMatches=(index,test)=>turns[index].owned.some(test);const revealTurn=(index,predicate)=>{renderers[index]();const element=list.children[index],done=new Set();let changed=true;while(changed){changed=false;for(const pager of [...turnPagers[index]])if(!done.has(pager)){done.add(pager);pager.reveal(predicate);changed=true}for(const run of element.querySelectorAll('.subagent-run:not(.materialized)'))if(run.owned.some(predicate)){run.materialize();changed=true}}for(const detail of element.querySelectorAll('.tool-event'))if(detail.sourceMessages?.some(predicate))detail.materialize?.()};panel.reveal=predicate=>{const found=[];turns.forEach((turn,index)=>{if(turnMatches(index,predicate)){revealTurn(index,predicate);found.push(index)}});return found};panel.materializeTurn=index=>renderers[index]?.();panel.materializeAllTurns=()=>renderers.forEach(render=>render());filter.oninput=()=>{const query=filter.value.trim().toLowerCase(),test=message=>searchText(message).includes(query);list.querySelectorAll('.turn').forEach((turn,index)=>{turn.hidden=Boolean(query&&!turnMatches(index,test));if(query&&!turn.hidden){revealTurn(index,test);turn.open=true;turn.querySelectorAll('.subagent-run').forEach(run=>{if(run.owned?.some(test)){run.materialize();run.open=true}});turn.querySelectorAll('.tool-event').forEach(event=>{const open=Boolean(event.dataset.searchEntry?.includes(query));if(open)event.materialize?.();event.open=open});turn.querySelectorAll('.prose-truncated').forEach(prose=>{if(prose.textContent.toLowerCase().includes(query)){prose.classList.remove('prose-truncated');const toggle=prose.parentElement.querySelector('.message-toggle');if(toggle){toggle.textContent='Show less';toggle.setAttribute('aria-expanded','true')}}})}})};expand.onclick=()=>list.querySelectorAll('.turn').forEach((item,index)=>{renderers[index]();item.open=true});collapse.onclick=()=>list.querySelectorAll('.turn').forEach(item=>item.open=false);const key=node('section','transcript-key');key.setAttribute('aria-label','Conversation key');key.append(transcriptFilterControl(panel,messages.filter(message=>!panel.transcript.consumed.has(message))),panel.tagFilter.host);title.append(key);{const highlights=transcriptHighlightControl(panel,conversation,messages);if(highlights){key.append(highlights);conversationMapCleanups.push(highlights.dispose)}const links=bindEventLinks(panel,eventLinkList);if(links)conversationMapCleanups.push(links)}if(['claude-ai-export','claude-code-cloud-export'].includes(conversation.harness)&&!messages.some(message=>typeof message.tokenReport?.total==='number'))head.append(node('p','usage-dashboard-note claude-export-usage-note','This Claude export does not report token consumption for this conversation. API cost is unavailable without reported token usage and model information. Token budgets are not consumption, and API cost is not your subscription bill.'));const usageEstimate=usageEstimatePresentation(conversation);if(usageEstimate){const note=node('p','usage-dashboard-note usage-estimate-note',usageEstimate.label+'. '+usageEstimate.detail+' '),assumptions=node('a','', 'View published assumptions');assumptions.href=usageEstimate.assumptions;assumptions.target='_blank';assumptions.rel='noopener';note.append(assumptions);head.append(note)}panel.append(head,list);requestAnimationFrame(()=>setupConversationMap(panel,list));return panel}
function enhanceTurnSummaries(panel,conversation){
  const summaries=turnSummaryData(conversation);
  panel.querySelectorAll('.turn').forEach((turn,index)=>{
    const data=summaries[index];if(!data)return;
    const summaryText=turn.querySelector('.turn-summary-text'),stats=turn.querySelector('.turn-summary-stats');
    const addExpandableRow=(element,text,label)=>{
      if(!element)return;element.textContent=text;
      const row=node('div','turn-summary-row'),toggle=node('button','turn-summary-expand','More');
      toggle.type='button';toggle.setAttribute('aria-label',`Expand ${label}`);toggle.setAttribute('aria-expanded','false');
      const key=turn.dataset.pharosDisclosure+':'+element.className;
      const apply=expanded=>{element.classList.toggle('summary-expanded',expanded);toggle.textContent=expanded?'Less':'More';toggle.setAttribute('aria-label',`${expanded?'Collapse':'Expand'} ${label}`);toggle.setAttribute('aria-expanded',String(expanded))};
      bindViewContent(toggle,key,false,apply);
      toggle.onclick=event=>{event.preventDefault();event.stopPropagation();const expanded=toggle.getAttribute('aria-expanded')!=='true';apply(expanded);saveViewContent(key,expanded)};
      element.replaceWith(row);row.append(element,toggle)
    };
    addExpandableRow(summaryText.querySelector('.turn-summary-prompt'),data.prompt,'turn ask');
    addExpandableRow(summaryText.querySelector('.turn-summary-outcome'),data.outcome||'No response retained','turn response');
    stats.replaceChildren();
    for(const [count,singular,plural] of [[data.replies,'reply','replies'],[data.activities,'activity','activities'],[data.tools,'tool call','tool calls'],[data.subagents,'sub-agent','sub-agents']])stats.append(node('span','mini-count',`${count} ${count===1?singular:plural}`));
  });
  return panel
}
const renderConversationBase=renderConversation;
renderConversation=(conversation,repoRoot='')=>{
  const panel=enhanceTurnSummaries(renderConversationBase(conversation,repoRoot),conversation);
  setupTokenOverview(panel,conversation);setupConversationSearch(panel);return panel;
};
function setupConversationSearch(panel){
  // Page Find searches the transcript. Message and tool-call targets reveal
  // their owning content; matches hidden by the Show filter are counted.
  const resolveTarget=(reference,initial)=>{const at=reference.indexOf(':'),kind=reference.slice(0,at),id=reference.slice(at+1);if(!['message','call'].includes(kind)||!panel.isConnected||!panel.closest('#detail.active'))return null;if(initial)panel.reveal?.(message=>kind==='message'?message.id===id:message.call_id===id);const target=panel.querySelector(`[data-${kind==='message'?'message':'call'}-id="${CSS.escape(id)}"]`);if(!target)return null;if(initial){exemptFromTranscriptFilter(panel,target);for(let el=target;el&&el!==panel;el=el.parentElement)if(el.tagName==='DETAILS'){el.materialize?.();el.open=true}}return target};
  const hiddenMatches=pattern=>{if(!panel.isConnected||!panel.closest('#detail.active'))return null;const found=transcriptFind(panel.tokenModel.messages,{pattern,shown:panel.transcriptShown(),output:true,hiddenTagOf:panel.tagFilter.hiddenTagsFor});if(!found.hidden)return null;const kinds=found.hidden_kinds,tags=found.hidden_tags,labels=[transcriptKindList(kinds),...tags.map(panel.tagFilter.nameOf)].filter(Boolean).join(', ');return{text:`${found.hidden} more matching event${found.hidden===1?'':'s'} in hidden ${labels}`,action:{label:'Show',run:()=>{if(kinds.length)panel.showTranscriptKinds?.(kinds);if(tags.length)panel.tagFilter.show(tags)}}}};
  const cleanups=[];const register=()=>{if(cleanups.length||!window.pharosView||!window.pharosFind)return;cleanups.push(window.pharosView.register(resolveTarget),window.pharosFind.registerNote(hiddenMatches))};register();
  if(!cleanups.length){window.addEventListener('pharos:view-ready',register,{once:true});cleanups.push(()=>window.removeEventListener('pharos:view-ready',register))}
  conversationMapCleanups.push(()=>cleanups.splice(0).forEach(cleanup=>cleanup()));
}

function setupTokenOverview(panel,conversation={}){
  const turns=[...panel.querySelectorAll('.turn')],entries=[],turnEntries=[];
  // Use the same normalized objects and ownership graph as the transcript.
  // Closed, unmaterialized turns still have complete accounting and profiles.
  const model=panel.tokenModel,order=new Map(model.messages.map((message,index)=>[message,index]));
  const spend=messages=>{const known=messages.map(message=>message.tokenReport).filter(report=>report&&report.total!==null);return known.length?known.reduce((n,report)=>n+report.total,0):null};
  const estimated=messages=>messages.some(message=>message.tokenReport?.isEstimate);
  const spendBadge=(target,total,label,isEstimate=false)=>{if(total===null)return;target=tokenColumn(target);let badge=target.querySelector(':scope > .overview-spend');if(!badge){badge=node('span','token-badge spend-badge overview-spend');target.append(badge)}badge.textContent=tokenCount(total)+(isEstimate?'*':'');badge.dataset.tokens=String(total);badge.title=isEstimate?'* Includes estimated generated output; input and cache usage are unavailable.':label+' consumption, including repeated and cached input. Not context size.'};
  turns.forEach((turn,index)=>{
    const owned=model.turns[index].owned,total=spend(owned);
    spendBadge(turn.querySelector(':scope > summary'),total,'Turn (including subagents)',estimated(owned));turnEntries.push({target:turn,turn,index,total});
    for(const message of owned){const sample=message.tokenReport?.contextSample;if(sample)entries.push({sample,message,target:turn,turn,index,order:order.get(message),label:`Turn ${index+1}`})}
  });
  entries.sort((a,b)=>a.index-b.index||a.order-b.order);
  const entryBySample=new Map();
  for(const entry of entries){if(!entryBySample.has(entry.sample))entryBySample.set(entry.sample,[]);entryBySample.get(entry.sample).push(entry)}
  const refreshTargets=(roots=[panel])=>{
    for(const root of roots){
      const holders=[...(root.matches('.context-evidence')?[root]:[]),...root.querySelectorAll('.context-evidence')];
      for(const holder of holders)for(const sample of holder.contextSamples)for(const entry of entryBySample.get(sample)||[])entry.target=holder.closest('.timeline-entry')||holder;
      // Only new sub-agents need accounting; earlier pages keep their badges.
      const agents=[...(root.matches('.subagent-run:not(.spend-shown)')?[root]:[]),...root.querySelectorAll('.subagent-run:not(.spend-shown)')];
      for(const agent of agents){agent.classList.add('spend-shown');spendBadge(agent.querySelector(':scope > summary'),spend(agent.spendOwned||[]),'Subagent (including nested agents)',estimated(agent.spendOwned||[]))}
    }
  };
  refreshTargets();
  const total=conversation.token_usage?.total_tokens??(turnEntries.some(e=>e.total!==null)?turnEntries.reduce((n,e)=>n+(e.total||0),0):null);
  panel.classList.toggle('has-token-usage',total!==null||entries.length>0);
  const legend=node('div','token-legend');
  legend.append(node('span','',total===null?'Conversation token usage unavailable':'Conversation total: '+tokenText(total)+((conversation.token_usage_is_estimate||estimated(model.messages))?'*':'')));
  legend.title=(conversation.token_usage_is_estimate||estimated(model.messages))?'* Estimated generated output only; input and cache usage are unavailable.':'Reported token usage and context changes across the conversation.';
  const jump=entry=>{const filter=panel.querySelector('input[type=search]');if(filter.value){filter.value='';filter.oninput()}panel.reveal(message=>message===entry.message);refreshTargets();exemptFromTranscriptFilter(panel,entry.target);for(let e=entry.target;e&&e!==panel;e=e.parentElement)if(e.tagName==='DETAILS')e.open=true;entry.target.scrollIntoView({behavior:'smooth',block:'center'});entry.target.classList.remove('token-highlight');requestAnimationFrame(()=>entry.target.classList.add('token-highlight'))};
  panel.querySelector('.conversation-head .chips').append(legend);
  const materialized=event=>{refreshTargets(event.detail?.roots||[event.target]);panel.dispatchEvent(new Event('input'))};
  panel.addEventListener('transcript-materialized',materialized);
  conversationMapCleanups.push(()=>panel.removeEventListener('transcript-materialized',materialized));
  const labels=model.messages.map(message=>({id:message.id,role:message.role,kind:message.kind,at:message.created_at||null,text:message.text||'',text_source:'Retained message',truncated:false,segment:message.labels?.segment||'',chunks:message.labels?1:0,labels:(message.labels?.ids||[]).map(id=>[id,message.labels.probs?.[id]||0]),authorship:message.authorship}));
  const marks=readerNavigatorMarks(model,message=>({name:toolPayload(message).name||toolLabel(message),preview:toolPreview(message)}));
  const events=model.messages.flatMap(message=>(messageEventView(message)||[]).map(event=>({id:event.id,message_id:message.id,kind:event.kind,label:event.label,group:event.group,probability:event.probability||0,source:message.kind==='reasoning'?'thinking':'message',sentence:event.sentenceText,target:event.target,span:event.ownSentence||event.sentence,position:event.sentence[0]/Math.max(1,String(message.text||'').length)})));
  const {streams,windows,scale:profileScale}=contextScale(entries.map(entry=>entry.sample)),contextColors=['#43877e','#b07a18','#557ed0','#c06c91','#8a79be'];
  const contextMetadata=entry=>{const sample=entry.sample,window=windows.get(sample.stream);return{color:sample.compact?sample.trigger==='manual'?'var(--heat-1)':sample.trigger==='auto'?'var(--heat-3)':'var(--ink)':sample.stream==='main'?contextColors[0]:contextColors[1+Math.max(0,streams.filter(stream=>stream!=='main').indexOf(sample.stream))%(contextColors.length-1)],title:`${entry.label} · ${sample.stream==='main'?'Main context':'Subagent '+sample.stream} · ${sample.compact?compactionLabel(sample.trigger):sample.size.toLocaleString()+' input tokens'+(sample.estimated?' (estimated)':'')+(window?` · ${Math.round(sample.size/window*100)}% of ${tokenCount(window)} window`:' · context window unknown')}`}};

  panel.navigatorData={sessionID:conversation.id,messages:labels,marks,events,taxonomy:conversation.labels?.taxonomy||[],turns:turns.map((target,index)=>({target,messages:model.turns[index].owned.map(message=>message.id),label:target.dataset.prompt||`Turn ${index+1}`})),contexts:entries.map(entry=>({...entry,...contextProfile(entry.sample,profileScale),...contextMetadata(entry)})),jumpContext:entry=>jump(entries.find(candidate=>candidate.message===entry.message&&candidate.sample===entry.sample)||entry),jumpMessage:id=>revealMessage(panel.parentElement,id,true)};

}

async function detail(id,fromLocation=false){if(!fromLocation)setRoute('/work/'+encodeURIComponent(id));try{if(workspaceInbox?.workspace===id&&document.querySelector('#detail.active'))return await workspaceLocation?.();return await pageLoad(()=>renderDetail(id))}catch(error){if(error.name!=='AbortError')throw error}}
let delegationExplorer=null,workspaceInbox=null,workspaceLocation=null,workspaceLoad=null,workFilesSection=null;
async function renderDetail(id){
workspaceLoad?.abort();const load=new AbortController();workspaceLoad=load;const signal=load.signal;
workspaceInbox?.destroy();workspaceInbox=null;workspaceLocation=null;
delegationExplorer?.destroy();delegationExplorer=null;workFilesSection?.destroy();workFilesSection=null;
const includeUncertain=pharosPrefs.get('pharos-uncertain-related-work',false)===true;
const w=await api('/api/work/'+encodeURIComponent(id)+(includeUncertain?'?include_uncertain=1':''),{signal});if(signal.aborted)return;
const routeConversation=()=>{const parts=location.pathname.split('/');return parts.length===5&&parts[3]==='conversation'?decodeURIComponent(parts[4]):new URLSearchParams(location.search).get('conversation')||''};
const requestedConversation=routeConversation(),missingConversation=requestedConversation&&![...(w.conversations||[]),...(w.peer_conversations||[])].some(c=>c.id===requestedConversation);
// Saved links can pair a conversation with an outdated workspace. Resolve its stable
// identity before mounting a merged reader, which otherwise hides the overview
// without ever loading the requested conversation.
if(missingConversation){
 const resolutionPath=location.pathname;
 const references=await api('/api/query-object-references',{method:'POST',body:JSON.stringify({references:[{objectType:'pharos_conversations',key:{id:requestedConversation}}]}),signal});
 // A completed lookup may only redirect or mount the route that requested it.
 if(signal.aborted||workspaceLoad!==load||location.pathname!==resolutionPath||routeConversation()!==requestedConversation)return;
 const target=references[0]?.href&&new URL(references[0].href,location.href),workspace=target?.pathname.match(/^\/work\/([^/]+)\/conversation\//)?.[1];
 if(workspace&&decodeURIComponent(workspace)!==id){target.search=location.search;history.replaceState(null,'',target.pathname+target.search);return renderDetail(decodeURIComponent(workspace))}
}
show('detail',true);recordVisit(false,w.title||'Untitled workspace');conversationMapCleanups.splice(0).forEach(cleanup=>cleanup());
const root=$('#detailBody');root.replaceChildren();
for(const key of ['prs','attempts','handoffs','changes','metrics','conversations','identity_links'])w[key] ||= [];
const disclosure=(label,className)=>{const d=node('details',className);d.append(node('summary','',label));return d};
const header=node('header','work-heading'),context=node('div','meta'),appendContext=value=>{if(!value)return;if(context.childNodes.length)context.append(document.createTextNode(' · '));context.append(value instanceof Node?value:document.createTextNode(value))};
if(w.repository_name){const repository=node('button','work-repository-link',w.repository_name);repository.type='button';repository.title=`Search work for ${w.repository_name}`;repository.onclick=()=>{window.pharosQueryTables?.filterRepository?.(w.repository_name);show('library')};appendContext(repository)}else appendContext('Repository not identified');if(w.source_kind)appendContext(`Source: ${w.source_kind}`);appendContext(w.branch);appendContext(w.activity_at?timeLabel(w.activity_at):null);
const workspaceStats=node('div','delegation-workspace-stats');header.append(node('h1','',w.title),context);
w.peers ||= [];w.peer_conversations ||= [];
// A heading section remembers, across work pages, whether it was last open;
// a link's own disclosures state still wins.
const workSection=(label,name,className)=>{const pref=`pharos-work-section-${name}`,open=pharosPrefs.get(pref,true)!==false,d=node('details',`work-section ${className}`),summary=node('summary','work-section-summary',label);d.dataset.viewKey=`work:${id}:${name}`;d.dataset.viewDefault=String(open);d.open=open;summary.addEventListener('click',()=>pharosPrefs.set(pref,!d.open));d.append(summary);return d};
let strip=null;
if(w.peers.length){const peers=peersByAge(w.peers),row=node('div','peer-chips'),titles=Object.fromEntries(peers.map(peer=>[peer.workspace_id,peer.title])),chips=peers.map(peer=>({peer,chip:peerChip(peer,{page:id,titles})}));strip=workSection(`Related work · ${peers.length}`,'related','peer-strip');strip.append(row);
for(const {chip} of chips){const link=node('a',`chip peer-chip${chip.certain?'':' uncertain'}`,chip.label);link.href=chip.href;link.title=chip.title;link.onclick=event=>{if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();window.pharosNavigate(chip.href)};row.append(link)}
const why=disclosure('Why related','peer-reasons');why.dataset.viewKey='work:'+id+':peers';const list=node('ul','');for(const {peer,chip} of chips){const item=node('li',chip.certain?'':'uncertain');item.append(node('strong','',`${peerRoleLabel(peer.role)} · ${peer.title||'Untitled work'}${chip.certain?'':' · uncertain'}`));const reasons=node('ul','');chip.reasons.forEach(reason=>reasons.append(node('li','',reason)));item.append(reasons);list.append(item)}why.append(list,node('p','meta',includeUncertain?'Related work is inferred from what conversations read, wrote and quoted. Certain and uncertain work appears among this workspace’s agent sessions; uncertain work is shown dashed.':'Related work is inferred from what conversations read, wrote and quoted. Certain work also appears among this workspace’s agent sessions; uncertain work is shown dashed.'));strip.append(why)}
header.append(workspaceStats);
const activity=workSection('Code activity','code','work-activity'),controls=node('div','work-controls');activity.append(controls);
for(const pr of workPullRequests(w)){const link=node(pr.url?'a':'span','chip',pr.title?`${pr.title} · #${pr.number}`:`PR #${pr.number}`);if(pr.relationship)link.title=pr.relationship;if(pr.url){link.href=pr.url;link.target='_blank';link.rel='noreferrer'}controls.append(link)}
if(w.main_merge_commit)controls.append(mainIntegrationLink(w));
const archive=disclosure('Archive details','archive-details'),archiveBody=node('div','archive-body');archive.dataset.viewKey='work:'+id+':archive';archive.dataset.pharosTarget='section:archive';archive.append(archiveBody);
if(w.location)archiveBody.append(node('div','file',w.location));
if(w.attempts.length||w.handoffs.length){const p=node('section','panel');p.append(node('h2','','Attempts, checkpoints, and handoffs'));w.attempts.forEach(a=>{const item=node('div','message');item.append(node('div','role',`Attempt ${a.attempt_no||'?'}${a.validation?' · '+a.validation:''}`),contentView(a.result||a.failure||'Outcome unavailable'));p.append(item)});w.handoffs.forEach(h=>p.append(node('div','message',`Candidate ${h.candidate_id||'unknown'} · custody ${h.custody_holder||'released'} · version ${h.custody_version||'unknown'}`)));archiveBody.append(p)}

if(w.identity_links.length){const p=node('section','panel');p.append(node('h2','','Related source identities'));w.identity_links.forEach(x=>p.append(node('div','meta',`${x.related_provider} · ${x.relationship} · confidence ${x.confidence}`)));archiveBody.append(p)}
if(w.receipt){const p=node('section','panel');p.append(node('h2','','Preservation receipt'),node('div','meta',`${w.receipt.outcome} · ${formatBytes(w.receipt.package_bytes)} logical size · ${w.receipt.package_hash}`));archiveBody.append(p)}

// Selected related work’s conversations read in place beside this workspace’s own.
const conversations=[...w.conversations,...w.peer_conversations],roots=[w.location,...(parsedJSON(w.repository_locations_json)||[])],host=node('div','conversation-host');
archive.hidden=!archiveBody.childNodes.length&&!(!conversations.length&&w.summary);
let activeIndex=-1,mounting=0;
const readerStates=new Map();
const rememberReader=()=>{const panel=host.querySelector('.conversation-panel:not(.conversation-loading)');if(activeIndex<0||!panel)return;readerStates.set(conversations[activeIndex].id,{scroll:host.scrollTop,turns:[...panel.querySelectorAll('.turn-list > .turn')].map(turn=>turn.open)})};
// Conversations arrive one at a time; only the mounted one keeps its messages.
const conversationPath=c=>`/api/work/${encodeURIComponent(c.peer?c.workspace_id:id)}/conversations/${encodeURIComponent(c.id)}`;
const loadConversation=async(index,ticket)=>{const c=conversations[index];if(!c.messages){const full=await api(conversationPath(c),{signal});if(signal.aborted||ticket!==mounting)return null;Object.assign(c,full)}for(const other of conversations)if(other!==c&&other.id)delete other.messages;return c};
// Finding text across conversations runs on the service; the page holds one.
// Each result counts a conversation's matching events that the Show filter
// shows and hides; without show every kind counts as shown, and an empty
// show (Custom with nothing checked) shows none.
const searchConversations=async({term,output=true,regex=false,caseSensitive=false,show=null})=>{if(!term)return[];const params=new URLSearchParams({q:term});if(output)params.set('output','1');if(regex)params.set('regex','1');if(caseSensitive)params.set('case','1');if(Array.isArray(show))params.set('show',show.join(','));const workspaces=[...new Set([id,...conversations.filter(c=>c.peer).map(c=>c.workspace_id)])];const results=await Promise.all(workspaces.map(workspace=>api(`/api/work/${encodeURIComponent(workspace)}/find?${params}`,{signal}).then(result=>result.conversations||[],()=>[])));return results.flat().map(item=>({...item,index:conversations.findIndex(c=>c.id===item.id)})).filter(item=>item.index>=0)};
const findConversations=async term=>(await searchConversations({term})).map(item=>item.index);
const conversationLabel=i=>{const other=conversations[i];return[`${i+1}. ${other.peer?peerRoleLabel(other.peer_role)+' · ':''}${other.provider||'Agent'}`,other.started_at?timeLabel(other.started_at):null,shortText(displayText({role:'user',text:other.first_prompt||''})||other.native_id||'Conversation',80)].filter(Boolean).join(' · ')};
const matchingLibraryConversation=async query=>{const term=query.trim(),words=term.split(/\s+/).filter(Boolean);if(!term)return-1;const hits=await findConversations(term);if(hits.length||words.length<2)return hits[0]??-1;const each=await Promise.all(words.map(word=>findConversations(word)));return each[0].find(i=>each.every(list=>list.includes(i)))??-1};

const updateConversationURL=(index,mode='push',message='',session='',at='',call='')=>{const params=new URLSearchParams(location.search);if(activeIndex>=0&&params.get('conversation')!==conversations[index].id){params.delete('highlight');params.delete('reveal');params.delete('reader_event_link')}params.set('conversation',conversations[index].id);if(message)params.set('message',message);else params.delete('message');if(session)params.set('delegation_session',session);else params.delete('delegation_session');if(at&&!message)params.set('at',at);else params.delete('at');if(call&&message)params.set('call',call);else params.delete('call');const path=`/work/${encodeURIComponent(id)}/conversation/${encodeURIComponent(conversations[index].id)}`,url=path+'?'+params;if(mode==='push'&&url!==location.pathname+location.search)history.pushState(null,'',url);else history.replaceState(null,'',url);syncNavigator()};
let selector;
// A merged workspace's overview leads the reader; it stays as conversations load.
// Only this workspace's inbox reads into this host.
const showReader=content=>host.replaceChildren(...workspaceInbox?.reader===host&&workspaceInbox.merged?[workspaceInbox.lead]:[],content);
const mount=async index=>{
if(signal.aborted)return;
const ticket=++mounting;activeIndex=index;conversationMapCleanups.splice(0).forEach(cleanup=>cleanup());
const c=conversations[index];if(!c.messages)showReader(node('section','panel conversation-panel conversation-loading',`Loading conversation${c.message_count?` · ${Number(c.message_count).toLocaleString()} messages`:''}…`));
// A load that outlives its workspace page must not touch the one now shown.
try{
 if(!await loadConversation(index,ticket))return;
 if(!window.pharosReaderKeys)await new Promise(resolve=>{const ready=()=>{removeEventListener('pharos:reader-keys-ready',ready);signal.removeEventListener('abort',ready);resolve()};addEventListener('pharos:reader-keys-ready',ready,{once:true});signal.addEventListener('abort',ready,{once:true});if(signal.aborted)ready()});
}catch(error){if(signal.aborted)return;if(ticket===mounting&&host.isConnected)showReader(node('section','panel conversation-panel badtext',`Conversation unavailable: ${error.message||error}`));return}
if(signal.aborted||ticket!==mounting||!host.isConnected)return;
c.title||=w.title;showReader(renderConversation(c,roots));
const panel=host.querySelector('.conversation-panel');bindReaderView(panel);

if(c.peer){const note=node('p','peer-reader-note'),link=node('a','',c.title||'Untitled work');link.href='/work/'+encodeURIComponent(c.workspace_id);link.onclick=event=>{if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();window.pharosNavigate(link.getAttribute('href'))};note.append(document.createTextNode(`${peerRoleLabel(c.peer_role)}${c.peer_confidence==='low'?' · uncertain':''} · related work: `),link);panel.querySelector('.conversation-head').prepend(note)}
if(delegationExplorer){const context=node('div');panel.querySelector('.conversation-title-content > .chips').after(context);delegationExplorer.conversation(c.id,context)}
if(conversations.length>1&&!window.pharosDelegation){const choice=node('div','conversation-choice'),label=node('label','',`Showing conversation #${index+1} from this workspace`);selector=node('select','conversation-selector');selector.id='workspaceConversation';selector.setAttribute('aria-label','Choose conversation from this workspace');label.htmlFor=selector.id;conversations.forEach((other,i)=>{const option=node('option','',conversationLabel(i));option.value=String(i);selector.append(option)});selector.value=String(index);selector.onchange=()=>selectConversation(Number(selector.value));choice.append(label,selector);panel.querySelector('.conversation-head').prepend(choice)}
};
const selectConversation=async(index,{message='',session='',at='',call='',historyMode='push',focusReader=true}={})=>{
if(signal.aborted||index<0||index>=conversations.length)return;
const focused=document.activeElement;
const same=index===activeIndex&&host.querySelector('.conversation-panel:not(.conversation-loading)');
if(!same)rememberReader();
updateConversationURL(index,historyMode,message,session,at,call);workspaceInbox?.setViewer(true);
if(!same)await mount(index);
if(signal.aborted||index!==activeIndex||!host.isConnected||workspaceInbox&&!workspaceInbox.viewing)return;
await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
if(signal.aborted||index!==activeIndex||!host.isConnected||workspaceInbox&&!workspaceInbox.viewing)return;
const panel=host.querySelector('.conversation-panel:not(.conversation-loading)');if(!panel)return;
panel.querySelector('.linked-message-unavailable')?.remove();
if(focusReader&&!new URLSearchParams(location.search).has('reveal')&&(document.activeElement===focused||document.activeElement===document.body)){panel.tabIndex=-1;panel.focus({preventScroll:true});}
const anchor=message||transcriptAnchor(conversationMessages(conversations[index]),{at});
if(anchor)revealMessage(host,anchor,true,call);
// A linked reveal (reveal=message:<id>) lands where the attention
// runtime scrolls it; the reader's own landing would scroll it away.
else if(new URLSearchParams(location.search).has('reveal'))window.pharosView?.refresh();
else if(workspaceInbox){const state=readerStates.get(conversations[index].id);if(state){if(!same)[...panel.querySelectorAll('.turn-list > .turn')].forEach((turn,i)=>{if(state.turns[i])panel.materializeTurn?.(i);turn.open=Boolean(state.turns[i])});if(!same||historyMode==='replace')host.scrollTop=state.scroll}else if(!same)host.scrollTop=0}
else panel.scrollIntoView({behavior:'smooth',block:'start'});
};
window.pharosOpenHandoff=marker=>{const index=conversations.findIndex(c=>c.id===marker.conversation);if(index<0||!host.isConnected)return false;void selectConversation(index,{message:marker.message,focusReader:false});return true};
const paths=new Map(),pathEntry=path=>{if(!paths.has(path))paths.set(path,{changes:[],conversation:''});return paths.get(path)};
for(const change of w.changes.filter(c=>c.path))pathEntry(change.path).changes.push(change);
for(const file of w.files||[]){const entry=pathEntry(file.path);entry.conversation||=file.conversation_id}
if(paths.size||w.linked_commits){const files=disclosure('Files and commits','work-disclosure file-index');files.dataset.viewKey='work:'+id+':files';
// The session's files, each edit's call and each file's page, and its
// commits (work-files.tsx); before the bundle, its recorded file actions.
const filesHost=node('div','work-files-host'),mountFiles=()=>{if(workFilesSection||!files.open||!files.isConnected||!window.pharosWorkFiles)return;filesHost.replaceChildren();workFilesSection=window.pharosWorkFiles.mount(filesHost,{workspace:id})};files.append(filesHost);files.addEventListener('toggle',mountFiles);if(!window.pharosWorkFiles)addEventListener('pharos:delegation-ready',mountFiles,{once:true});
filesHost.append(node('p','meta','Recorded file actions; not necessarily the final working-tree state.'));
for(const [path,{changes,conversation}] of paths){const row=node('div','file-index-row'),button=node('button','file-index-link',path);button.title='Find actions for this file in the conversation';button.disabled=!conversations.length;
button.onclick=async()=>{let index=conversations.findIndex(c=>c.id===conversation);if(index<0)index=(await findConversations(path))[0]??-1;if(index<0){button.disabled=true;button.title='No retained conversation mentions this file';return}files.open=false;await selectConversation(index);window.pharosFind?.open(path);host.scrollIntoView({behavior:'smooth',block:'start'})};row.append(button);if(changes.length)row.append(node('span','meta',changes.map(c=>[c.classification,c.status,c.complete?'':'excerpt/incomplete'].filter(Boolean).join(' · ')).join('; ')));filesHost.append(row)}controls.append(files);requestAnimationFrame(mountFiles)}
if(controls.childNodes.length)header.append(activity);if(strip)header.append(strip);root.append(header);
if(missingConversation){const notice=node('p','coverage-warning','The linked conversation is no longer available in this workspace.'+(conversations.length===1&&!conversations[0].parent_id?' Showing the retained conversation.':conversations.length?' Choose a retained agent session below.':''));notice.setAttribute('role','status');root.append(notice)}
const warnings=conversations.filter(c=>/partial|incomplete|missing|truncat|failed/i.test(c.coverage||''));
if(warnings.length)root.append(node('div','coverage-warning',`Transcript coverage: ${warnings.map(c=>`${c.provider||'Agent'} · ${c.coverage}`).join('; ')}. Some activity may be unavailable.`));
if(/partial|incomplete|failed/i.test(w.preservation_completeness||''))root.append(node('div','coverage-warning',`Preservation · ${w.preservation_completeness}.${archive.hidden?'':' See Archive details.'}`));
const focus=new URLSearchParams(location.search),focusIndex=conversations.findIndex(c=>c.id===routeConversation()),matchedIndex=focusIndex<0?await matchingLibraryConversation(focus.get('search')||''):-1,initialIndex=Math.max(0,focusIndex,matchedIndex);
if(signal.aborted)return;const explorer=node('div','delegation-host');
root.append(explorer,host,archive);
const mountExplorer=()=>{
if(signal.aborted||delegationExplorer||!explorer.isConnected||!window.pharosDelegation||!window.pharosInbox)return;
host.querySelector('.conversation-choice')?.remove();
const openOverview=()=>{rememberReader();const params=new URLSearchParams(location.search);params.delete('conversation');params.delete('message');params.delete('at');params.delete('call');history.pushState(null,'',`/work/${encodeURIComponent(id)}${params.size?'?'+params:''}`);workspaceInbox.setViewer(false);syncNavigator()};
workspaceInbox=window.pharosInbox.mount({root,workspace:id,overview:[...root.children].filter(el=>el!==host),reader:host,onOverview:openOverview,onLayout:(compact,splitReader)=>delegationExplorer?.layout(compact,splitReader)});
// One conversation with no parent reads as one page. Its sessions arrive
// later; if they nest after all, the reader keeps the conversation open
// beside the workspace sessions.
const inbox=workspaceInbox;inbox.setMerged(conversations.length===1&&!conversations[0].parent_id);
const readerIndex=activeIndex>=0?activeIndex:focusIndex>=0||matchedIndex>=0||inbox.merged?initialIndex:-1;
delegationExplorer=window.pharosDelegation.mount(explorer,{workspace:id,includeUncertain,conversation:conversations[readerIndex]?.id||'',contextHost:null,statsHost:workspaceStats,readerHost:host,splitReader:inbox.viewing&&!inbox.merged,openOverview,showSessions:()=>inbox.showSessions(),openConversation:async(conversation,message,session)=>selectConversation(conversations.findIndex(c=>c.id===conversation),{message,session}),onHierarchy:hierarchical=>{if(hierarchical&&inbox===workspaceInbox&&inbox.merged){inbox.setMerged(false)}}});
// The bundle can arrive after an initial deep-linked reader. Rebuild that
// reader inside the new scroll container so its rails and search use the pane.
if(activeIndex>=0&&routeConversation()){
workspaceInbox.setViewer(true);
const index=activeIndex,params=new URLSearchParams(location.search);
if(host.querySelector('.conversation-panel:not(.conversation-loading)'))void mount(index).then(()=>{if(index===activeIndex&&routeConversation()===conversations[index].id)void selectConversation(index,{message:params.get('message')||'',at:params.get('at')||'',call:params.get('call')||'',session:params.get('delegation_session')||'',historyMode:'replace',focusReader:false})});
}else if(workspaceInbox.merged&&(!routeConversation()||missingConversation)&&matchedIndex<0)void selectConversation(0,{historyMode:'replace',focusReader:false});
};
mountExplorer();if(!window.pharosDelegation)addEventListener('pharos:delegation-ready',mountExplorer,{once:true});
workspaceLocation=async()=>{if(routeConversation()&&!conversations.some(c=>c.id===routeConversation()))return renderDetail(id);window.dispatchEvent(new Event('pharos:workspace-history'));const routed=conversations.findIndex(c=>c.id===routeConversation()),index=routed<0&&workspaceInbox?.merged?0:routed;if(index>=0)await selectConversation(index,{message:new URLSearchParams(location.search).get('message')||'',at:new URLSearchParams(location.search).get('at')||'',call:new URLSearchParams(location.search).get('call')||'',session:new URLSearchParams(location.search).get('delegation_session')||'',historyMode:'replace',focusReader:false});else{rememberReader();workspaceInbox?.setViewer(false)}};
if(conversations.length){if(focusIndex>=0||matchedIndex>=0)await selectConversation(initialIndex,{message:focus.get('message')||'',at:focus.get('at')||'',call:focus.get('call')||'',session:focus.get('delegation_session')||'',historyMode:'replace',focusReader:false})}else{const notice=empty('No conversation was retained for this workspace.');archive.before(notice)};
if(!conversations.length&&w.summary){const summary=node('section','panel');summary.append(node('h2','','Retained account · transcript unavailable'));for(const key of ['initiation','outcome','failures'])if(w.summary[key]){summary.append(node('div','meta',key),contentView(w.summary[key]))}archiveBody.prepend(summary)}
}
function formatBytes(value){if(value==null||!Number.isFinite(Number(value)))return'Unavailable';const amount=Math.max(0,Number(value)),units=['B','KB','MB','GB','TB','PB'];if(amount===0)return'0 B';const power=Math.min(Math.floor(Math.log(amount)/Math.log(1000)),units.length-1),scaled=amount/(1000**power),digits=scaled<10&&power>0?1:0;return`${scaled.toFixed(digits)} ${units[power]}`}
function sourceTime(value){if(!value)return'Never';const date=new Date(value),seconds=Math.max(0,Math.round((Date.now()-date.getTime())/1000));let relative=seconds<60?'just now':seconds<3600?`${Math.floor(seconds/60)}m ago`:seconds<86400?`${Math.floor(seconds/3600)}h ago`:`${Math.floor(seconds/86400)}d ago`;return`${relative} · ${date.toLocaleString()}`}
// A harness database Pharos could not read in place (openSourceSQLite).
function sqliteReadNote(read){const file=read.path.split('/').pop(),why=read.cause==='locked'?'another app held it locked':'it could not be read in place';return{copy:`Read from a copy of ${file}: ${why}.`,immutable:`Read ${file} without locking it: its volume is read-only.`,waiting:`Waiting for another app to finish writing ${file}.`,failed:`Could not read ${file}: ${read.reason}`}[read.via]||`Read ${file} another way.`}
function sourceSeverity(source){if(source.error||source.path_status!=='available')return'bad';if(source.pending_count||!source.last_success_at||source.stale)return'warn';return''}
function sourceFact(list,label,value){list.append(node('dt','',label),node('dd','',value))}
async function loadSources(){
  window.pharosClaudeDownloads?.render($('#claudeDownloadsWatchSection'));
  const data=await api('/api/sources'),summary=$('#sourceSummary'),root=$('#sourceGrid');
  summary.replaceChildren(node('span','chip',`${data.configured} configured on this Mac`),node('span','chip',`${data.enabled} enabled on this Mac`));
  root.querySelectorAll('.source-card:not(.remote),.empty').forEach(card=>card.remove());
  if(!data.items.length){root.prepend(empty('No sources on this Mac yet. Use Look for sources to check the usual places, or Add a folder to name one.'));return}
  // A tool's sources sit together, in the order each tool first appears.
  const tools=[...new Set(data.items.map(source=>source.tool||source.kind))];
  root.prepend(...tools.flatMap(tool=>data.items.filter(source=>(source.tool||source.kind)===tool).map(sourceCard)));
}
function sourceCard(source){
  const severity=sourceSeverity(source),card=node('article','source-card '+(severity?'attention':'')),head=node('div','source-card-head'),title=node('div');
  card.dataset.source=source.name;
  title.append(node('h3','',source.name));
  title.append(node('span','source-host',[source.tool,source.scope==='host'?'this Mac':'every Mac (library.toml)'].filter(Boolean).join(' · ')));
  const switchWrap=node('div','source-switch'),toggle=node('button','toggle '+(source.enabled?'on':''));
  toggle.setAttribute('role','switch');
  toggle.setAttribute('aria-checked',String(source.enabled));
  toggle.setAttribute('aria-label',`${source.enabled?'Disable':'Enable'} ${source.name}`);
  toggle.onclick=()=>toggleSource(source,toggle);
  switchWrap.append(node('span','',source.enabled?'Enabled':'Paused'),toggle);
  head.append(title,switchWrap);
  const path=node('div','source-path',source.path||'No path configured'),facts=node('dl','source-facts');
  sourceFact(facts,'Last indexed',sourceTime(source.last_success_at));
  sourceFact(facts,'Last index attempt',sourceTime(source.last_attempt_at));
  sourceFact(facts,'Coverage',source.coverage);
  sourceFact(facts,'Account',source.account);
  // What this kind of source never records, or only sometimes (its provider's capabilities).
  if(source.not_recorded?.length)sourceFact(facts,'Not recorded',source.not_recorded.join(', '));
  if(source.sometimes_recorded?.length)sourceFact(facts,'Sometimes recorded',source.sometimes_recorded.join(', '));
  card.append(head,path,facts);
  if(source.kind==='claude-ai-settings')card.append(claudeSettingsRecordingControl(source));
  if(source.error)card.append(node('div','sync-note badtext',source.error));
  else if(source.path_status==='missing')card.append(node('div','sync-note badtext','The configured path is unavailable.'));
  const read=source.sqlite_reads?.[0];
  if(read)card.append(node('div',read.via==='failed'?'sync-note badtext':'sync-note',sqliteReadNote(read)));
  if(source.scope==='host')card.append(sourceRemoval(source));
  return card;
}
// Recording is a bounded action; it does not change the source's automatic schedule.
function claudeSettingsRecordingControl(source){
  const box=node('section','sync-note'),description=node('p','','Record me: page through Claude’s history/sidebar and open older conversations while Pharos collects their cached model, effort and thinking selections. Only conversations Claude caches can be collected. Automatic updates also collect these settings.'),status=node('p',''),button=node('button','source-remove','Record me for 30 minutes');
  button.type='button';button.disabled=!source.enabled;
  let active=false,busy=false,revision=0;
  const render=data=>{
    active=data.active;
    status.textContent=`${data.known_conversations} conversations with settings${data.expires_at?` · ${data.new_conversations} new this session`:''}${active?` · Recording until ${new Date(data.expires_at).toLocaleTimeString()}`:''}${data.error?` · ${data.error}`:''}`;
    button.textContent=active?'Stop recording':'Record me for 30 minutes';button.disabled=!source.enabled;
  };
  const refresh=async()=>{
    if(document.hidden||busy)return;
    const requested=revision;
    try{const data=await api(`/api/claude-chat-settings/recording?source=${encodeURIComponent(source.name)}`);if(!busy&&requested===revision)render(data)}catch(error){if(!busy&&requested===revision)status.textContent=error.message}
  };
  button.onclick=async()=>{
    if(busy||!source.enabled)return;
    busy=true;revision++;button.setAttribute('aria-busy','true');
    try{render(await api('/api/claude-chat-settings/recording',{method:'POST',body:JSON.stringify({source:source.name,active:!active})}))}catch(error){status.textContent=error.message}
    finally{busy=false;button.removeAttribute('aria-busy');button.disabled=!source.enabled}
  };
  const poll=async()=>{if(!box.isConnected)return;if(!busy)await refresh();if(box.isConnected)setTimeout(poll,5000)};
  box.append(description,status,button);void refresh();setTimeout(poll,5000);return box;
}
// Removing stops Pharos reading the source; what it already captured and
// indexed stays in the library. It asks in place (the app's web view has no
// confirm dialog).
function sourceRemoval(source){
  const box=node('div','source-removal'),start=node('button','source-remove','Remove…');
  start.type='button';start.setAttribute('aria-label',`Remove ${source.name}`);
  start.onclick=()=>{
    // Removing a source that replaces one in library.toml brings that one back.
    const shared=source.replaces,ask=shared?`Remove this Mac’s ${source.name}? Pharos then ${shared.enabled?'reads':'keeps paused'} the ${source.name} set up for every Mac in library.toml${shared.path?' ('+shared.path+')':''}. Conversations already in the library stay.`:`Stop reading ${source.path||source.name} on this Mac? Conversations already in the library stay.`;
    const question=node('p','sync-note',ask),yes=node('button','source-remove danger','Remove'),no=node('button','source-remove','Cancel');
    yes.type=no.type='button';
    no.onclick=()=>{box.replaceChildren(start);start.focus()};
    yes.onclick=async()=>{
      yes.disabled=no.disabled=true;
      try{await api(`/api/sources/${encodeURIComponent(source.name)}/remove`,{method:'POST',body:'{}'});feedbackToast(`${source.name} removed`)}
      catch(error){feedbackToast(error.message)}
      finally{await loadSources();void window.pharosLibrary?.refreshSettings?.()}
    };
    box.replaceChildren(question,yes,no);no.focus();
  };
  box.append(start);
  return box;
}
async function toggleSource(source,button){button.disabled=true;try{await api(`/api/sources/${encodeURIComponent(source.name)}/enabled`,{method:'POST',body:JSON.stringify({enabled:!source.enabled})});feedbackToast(`${source.name} ${source.enabled?'paused':'enabled'}`)}catch(error){feedbackToast(error.message)}finally{await loadSources();void window.pharosLibrary?.refreshSettings?.()}}
// The lighthouse turns while sources are indexed (4s a turn) or a page loads (1.5s a turn). It changes pace or stops only between turns, when the beam is back at rest, so it never jumps.
let brandSyncing=false,brandLoads=0;
// Both lighthouse behaviors are on unless switched off in Settings → App preferences.
const BRAND_OPTIONS=[{toggle:'#lighthouseSpinOnClick',key:'pharos-lighthouse-spin-on-click',on:true},{toggle:'#lighthouseLoadingIndicator',key:'pharos-lighthouse-loading-indicator',on:true}];
function brandOption(toggle){return BRAND_OPTIONS.find(option=>option.toggle===toggle).on}
function setBrandOption(option,on,save=true){option.on=on;const toggle=$(option.toggle);toggle.classList.toggle('on',on);toggle.setAttribute('aria-checked',String(on));if(save)pharosPrefs.set(option.key,on);renderBrand()}
function brandPace(){return brandOption('#lighthouseLoadingIndicator')?brandLoads?'loading':brandSyncing?'syncing':'':''}
function startBrandTurn(fast){const brand=$('.brand');brand.classList.toggle('loading',fast);brand.classList.add('sweeping')}
function settleBrandTurn(){const brand=$('.brand'),pace=brandPace(),fast=pace==='loading';if(!pace)brand.classList.remove('sweeping','loading');else if(brand.classList.contains('loading')!==fast){brand.classList.remove('sweeping');void brand.offsetWidth;startBrandTurn(fast)}}
function renderBrand(){const brand=$('.brand'),pace=brandPace();brand.title=brandSyncing?'Indexing sources. Open library':'Open library';if(pace&&!brand.classList.contains('sweeping'))startBrandTurn(pace==='loading');else if(matchMedia('(prefers-reduced-motion: reduce)').matches)settleBrandTurn()}
$('.brand').addEventListener('animationiteration',event=>{if(event.target===event.currentTarget)settleBrandTurn()});
function setBrandSyncing(on){brandSyncing=on;renderBrand()}
for(const option of BRAND_OPTIONS){setBrandOption(option,pharosPrefs.get(option.key,option.on)===true,false);$(option.toggle).onclick=()=>setBrandOption(option,$(option.toggle).getAttribute('aria-checked')!=='true')}
// Page loads mark the document busy at once and turn the lighthouse once they outlast 150ms, so instant loads don't flash it.
let pageLoads=0;
async function pageLoad(load){pageLoads++;document.documentElement.classList.add('page-loading');let turning=false;const timer=setTimeout(()=>{turning=true;brandLoads++;renderBrand()},150);try{return await load()}finally{clearTimeout(timer);if(turning){brandLoads--;renderBrand()}if(!--pageLoads)document.documentElement.classList.remove('page-loading')}}
let indexWatchTimer;
async function watchIndexing(){clearTimeout(indexWatchTimer);let active=false;try{active=(await api('/api/activity')).active>0}catch{}setBrandSyncing(active);if(!document.hidden)indexWatchTimer=setTimeout(watchIndexing,active?3000:15000)}
document.addEventListener('visibilitychange',()=>{if(document.hidden)clearTimeout(indexWatchTimer);else watchIndexing()});
watchIndexing();
const USAGE_SUMMARY_ROWS=quickStatsMetrics(document.documentElement.hasAttribute('data-concurrency-prototype'));
const USAGE_SUMMARY_COLUMNS=Object.fromEntries(QUICK_STATS_WINDOWS.map(([key,label])=>[key,label]));
let quickStatsLayout=normalizeQuickStatsLayout(pharosPrefs.get(QUICK_STATS_KEY,null),USAGE_SUMMARY_ROWS);
const usageSummaryCache=createQuickStatsCache({scope:window.pharosPreviewScope||''});
let usageSummaryData=null,usageSummaryError='',usageSummaryLoading=false,usageSummaryUpdatedAt=null,usageSummaryTimer=null,usageSummaryEpoch=0;
const USAGE_SUMMARY_LINKS_KEY='pharos-usage-summary-links';
let usageSummaryLinks=pharosPrefs.get(USAGE_SUMMARY_LINKS_KEY,false)===true;
function setUsageSummaryLinks(on,save=true){usageSummaryLinks=on;const toggle=$('#usageSummaryLinks');toggle.classList.toggle('on',on);toggle.setAttribute('aria-checked',String(on));if(save)pharosPrefs.set(USAGE_SUMMARY_LINKS_KEY,on);renderUsageSummary()}
// Auto update refetches the open summary every 30 seconds; the choice is remembered.
const USAGE_SUMMARY_AUTO_KEY='pharos-usage-summary-auto',USAGE_SUMMARY_AUTO_MS=30000;
let usageSummaryAuto=pharosPrefs.get(USAGE_SUMMARY_AUTO_KEY,false)===true;
// Built once, so re-rendering the table keeps the buttons and their focus.
const usageSummaryControls=comboButton([{id:'usageSummaryRefresh',icon:'refresh',ariaLabel:'Refresh usage summary',onClick:()=>void loadUsageSummary(true)},{id:'usageSummaryAuto',label:'Auto',title:`Auto update every ${USAGE_SUMMARY_AUTO_MS/1000} seconds`,pressed:usageSummaryAuto,onClick:()=>setUsageSummaryAuto(!usageSummaryAuto)},{id:'usageSummarySettings',icon:'settings',ariaLabel:'Configure Quick Stats',title:'Configure Quick Stats',onClick:()=>{setUsageSummaryOpen(false);openHistoryEntry(quickStatsSettingsURL())}}],{label:'Quick Stats controls',className:'usage-summary-controls'});
setUsageSummaryLinks(usageSummaryLinks,false);
$('#usageSummaryLinks').onclick=()=>setUsageSummaryLinks(!usageSummaryLinks);
const quickStatsEditor=initQuickStatsEditor(USAGE_SUMMARY_ROWS,quickStatsLayout,next=>{quickStatsLayout=next;renderUsageSummary()},layout=>buildUsageSummaryTable(layout,true));
$('#quickStatsExpand').addEventListener('pharos:disclosure',()=>{if($('#quickStatsExpand').getAttribute('aria-expanded')==='true'){usageSummaryEpoch++;void loadUsageSummary()}});
const PAUSE_INDEXING_FOR_QUERIES_KEY='pharos-pause-indexing-for-queries-v1';
function setPauseIndexingForQueries(on,save=true){const toggle=$('#pauseIndexingForQueries');toggle.classList.toggle('on',on);toggle.setAttribute('aria-checked',String(on));if(!save)return;pharosPrefs.set(PAUSE_INDEXING_FOR_QUERIES_KEY,on);pharosPrefs.flush?.()}
setPauseIndexingForQueries(pharosPrefs.get(PAUSE_INDEXING_FOR_QUERIES_KEY,false)===true,false);
$('#pauseIndexingForQueries').onclick=()=>setPauseIndexingForQueries(pharosPrefs.get(PAUSE_INDEXING_FOR_QUERIES_KEY,false)!==true);
const ESCAPE_CLEARS_HIGHLIGHTS_KEY='pharos-escape-clears-highlights';
function setEscapeClearsHighlights(on,save=true){const toggle=$('#escapeClearsHighlights');toggle.classList.toggle('on',on);toggle.setAttribute('aria-checked',String(on));if(save)pharosPrefs.set(ESCAPE_CLEARS_HIGHLIGHTS_KEY,on)}
setEscapeClearsHighlights(pharosPrefs.get(ESCAPE_CLEARS_HIGHLIGHTS_KEY,true)!==false,false);
$('#escapeClearsHighlights').onclick=()=>setEscapeClearsHighlights(pharosPrefs.get(ESCAPE_CLEARS_HIGHLIGHTS_KEY,true)===false);
function setUncertainRelatedWork(on,save=true){const toggle=$('#uncertainRelatedWork');toggle.classList.toggle('on',on);toggle.setAttribute('aria-checked',String(on));if(save)pharosPrefs.set('pharos-uncertain-related-work',on)}
setUncertainRelatedWork(pharosPrefs.get('pharos-uncertain-related-work',false)===true,false);
$('#uncertainRelatedWork').onclick=()=>setUncertainRelatedWork(pharosPrefs.get('pharos-uncertain-related-work',false)!==true);
// Three significant figures with a unit: 22.3B, 5.
function sigFigs(value,prefix=''){if(value==null||!Number.isFinite(Number(value)))return'—';const units=['','k','M','B','T'];let amount=Number(Number(value).toPrecision(3)),power=0;while(Math.abs(amount)>=1000&&power<units.length-1){amount=Number((amount/1000).toPrecision(3));power++}return prefix+String(amount)+units[power]}
function carbonSummaryValue(bucket){const factors=usageSummaryData?.carbon_factors;if(!factors)return null;const stored=pharosPrefs.get('pharos-carbon-v1',{}),saved=stored&&typeof stored==='object'?stored:{},scenario=['low','central','high'].includes(saved.scenario)?saved.scenario:'central',pueScenario=['low','central','high'].includes(saved.pueScenario)?saved.pueScenario:'central',inputScale=saved.inputSensitivity==='third'?5/3:1,grid=factors.grids.find(item=>item.key===saved.grid),gridValue=saved.grid==='custom'?Math.max(0,Number(saved.customGrid)||0):(grid||factors.grids.find(item=>item.key===factors.default_grid))?.g_co2e_per_kwh||0;let wh=0;for(const [tierKey,counts] of Object.entries(bucket.carbon_tokens||{})){const tier=factors.tiers.find(item=>item.key===tierKey);if(!tier)continue;for(const [category,count] of Object.entries(counts)){const ratio=category==='output'?1:(factors.ratios[category]?.[scenario]??1)*inputScale;wh+=Number(count||0)/1e6*(tier.output_wh_per_mtok?.[scenario]||0)*ratio}}const grams=wh*(factors.pue?.[pueScenario]||1)*gridValue/1000;const amount=grams>=1e6?`${sigFigs(grams/1e6)} t`:grams>=1e3?`${sigFigs(grams/1e3)} kg`:`${sigFigs(grams)} g`;return{amount,grams}}
function buildUsageSummaryTable(layout,preview=false){
  const table=node('table'),head=node('tr'),body=node('tbody'),windows=layout.windows.map(key=>usageSummaryData?.windows?.find(bucket=>bucket.key===key)||{key});
  const corner=node('th');renderUsageSummaryControls();const controls=preview?usageSummaryControls.cloneNode(true):usageSummaryControls;if(preview){controls.inert=true;controls.setAttribute('aria-hidden','true');for(const item of [controls,...controls.querySelectorAll('[id]')])item.removeAttribute('id')}corner.append(controls);head.append(corner);
  windows.forEach(bucket=>{const cell=node('th','',USAGE_SUMMARY_COLUMNS[bucket.key]||bucket.key);cell.scope='col';cell.dataset.quickStatsWindow=bucket.key;if(['5m','15m'].includes(bucket.key))cell.title='Token, API cost and CO₂e totals are estimates from hourly usage buckets.';if(['today','yesterday','week','month'].includes(bucket.key))cell.title=`${bucket.from||'Local calendar start'} – ${bucket.until||'now'} · ${usageSummaryData?.timezone||Intl.DateTimeFormat().resolvedOptions().timeZone}`;head.append(cell)});
  const thead=node('thead');thead.append(head);
  layout.metrics.map(field=>USAGE_SUMMARY_ROWS.find(row=>row[1]===field)).forEach(([label,field,description,prefix='',view,format])=>{
    const parallelism=field==='parallelism',row=node('tr'),name=node('th'),link=node('a','',label);row.dataset.quickStatsMetric=field;name.scope='row';name.title=description;
    if(parallelism){const target=new URL(location.href);target.searchParams.set('parallelism','1');link.href=target;link.onclick=event=>{if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||event.button||!window.pharosConcurrencySummary)return;event.preventDefault();setUsageSummaryOpen(false);window.pharosConcurrencySummary.open()}}else link.href=view==='conversations'?'/analyze?analyze_mode=query&analyze_topic=human&analyze_data=conversation_activity':`/analyze?analyze_topic=${view==='writing'?'human':view}`;
    if(preview){link.removeAttribute('href');link.onclick=null;link.tabIndex=-1;link.draggable=false}link.dataset.summaryUsageLink=view;name.append(link);row.append(name);
    windows.forEach(bucket=>{
      const timing=['agent_time_ms','active_time_ms'].includes(field)?window.pharosConcurrencySummary?.runtime?.(field,bucket.key,bucket.until||usageSummaryData?.generated_at||null,bucket.from):null,threaded=parallelism?window.pharosConcurrencySummary?.value(bucket.key,bucket.until||usageSummaryData?.generated_at||null,bucket.from):null,carbon=field==='co2e_g'?carbonSummaryValue(bucket):null,value=timing?timing.value:carbon?carbon.grams:bucket[field];
      const cell=node('td','',parallelism?(threaded?.amount||'…'):usageSummaryData?(carbon?.amount||(format?formatQuickStatsValue(value,format):sigFigs(value,prefix))):'…');
      cell.classList.toggle('usage-summary-pending',cell.textContent==='…');
      cell.dataset.quickStatsMetric=field;cell.dataset.quickStatsWindow=bucket.key;
      if(timing)cell.title=timing.title;else if(threaded)cell.title=threaded.title;
      else if(field==='tool_time_ms')cell.title=`${bucket.timed_tool_calls||0} of ${bucket.tool_calls||0} calls have recorded durations. Durations can overlap and include approval waits.`;
      else if(format==='percent'&&value==null)cell.title='No calls in this window';
      else if(value!=null&&format!=='percent'){const exact=carbon?`${Number(value).toLocaleString(undefined,{maximumFractionDigits:3})} g CO₂e`:prefix?pharosCurrency.formatUSD(Number(value)):Number(value).toLocaleString();if(exact!==cell.textContent)cell.title=exact;}
      const occurrence=value!=null?(quickStatsOccurrenceCoverage(field,bucket)||quickStatsAncestryCoverage(field,bucket)):null;if(occurrence){cell.textContent=occurrence.mark+cell.textContent;cell.title=[cell.title,occurrence.note].filter(Boolean).join('\n')}
      const caveat=value!=null?quickStatsMetricCaveat(field,bucket):null;if(caveat){cell.textContent+=caveat.suffix;cell.title=[cell.title,caveat.note].filter(Boolean).join('\n')}
      const href=usageSummaryLinks&&usageSummaryData?window.pharosQueryTables?.summaryURL(field,bucket.key,bucket.until||usageSummaryData.generated_at||new Date().toISOString(),undefined,bucket.from):null;
      if(href){const target=node('a','',cell.textContent);target.href=href;target.dataset.summaryMetric=field;target.dataset.summaryWindow=bucket.key;target.setAttribute('aria-label',`${label}, ${USAGE_SUMMARY_COLUMNS[bucket.key]||bucket.key}: ${cell.textContent}`);target.onclick=event=>{if(event.defaultPrevented||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||event.button)return;event.preventDefault();setUsageSummaryOpen(false);openHistoryEntry(href)};if(preview){target.removeAttribute('href');target.onclick=null;target.tabIndex=-1;target.draggable=false}cell.replaceChildren(target)}
      row.append(cell);
    });
    body.append(row);
  });
  table.append(thead,body);return table;
}
function renderUsageSummary(){
  const root=$('#usageSummary');if(root.hidden)return;
  const focused=usageSummaryControls.contains(document.activeElement)?document.activeElement:null,focusedCell=document.activeElement?.closest('[data-summary-window]')?.dataset;
  const scroller=node('div','usage-summary-scroll');scroller.append(buildUsageSummaryTable(quickStatsLayout));
  root.replaceChildren(scroller,node('p','usage-summary-note',quickStatsLayout.windows.some(key=>['5m','15m'].includes(key))?'Token, cost and CO₂e totals are estimated from hourly usage.':''),node('p','usage-summary-note',usageSummaryError),node('p','usage-summary-note',quickStatsLayout.metrics.includes('parallelism')?(window.pharosConcurrencySummary?.note()||''):''));focused?.focus({preventScroll:true});if(focusedCell)root.querySelector(`[data-summary-metric="${focusedCell.summaryMetric}"][data-summary-window="${focusedCell.summaryWindow}"]`)?.focus({preventScroll:true});placeUsageSummary();
}
function renderUsageSummaryControls(){const [refresh,auto]=usageSummaryControls.buttons;refresh.setAttribute('aria-busy',String(usageSummaryLoading));refresh.title=usageSummaryUpdatedAt?`Refresh · updated ${usageSummaryUpdatedAt.toLocaleTimeString()}`:'Refresh';auto.setAttribute('aria-pressed',String(usageSummaryAuto))}
function scheduleUsageSummary(){clearTimeout(usageSummaryTimer);usageSummaryTimer=null;if(usageSummaryAuto&&!$('#usageSummary').hidden)usageSummaryTimer=setTimeout(()=>void loadUsageSummary(true),USAGE_SUMMARY_AUTO_MS)}
function setUsageSummaryAuto(on){usageSummaryAuto=on;pharosPrefs.set(USAGE_SUMMARY_AUTO_KEY,on);renderUsageSummaryControls();if(on)void loadUsageSummary(true);else scheduleUsageSummary()}
function placeUsageSummary(){const root=$('#usageSummary'),toggle=headerToolAnchor($('#usageSummaryToggle'));if(root.hidden)return;const box=toggle.getBoundingClientRect(),header=document.querySelector('body>header').getBoundingClientRect();root.style.maxHeight=`${Math.max(0,innerHeight-header.bottom-20)}px`;root.style.top=`${Math.round(header.bottom+8)}px`;root.style.left=`${Math.round(Math.max(12,Math.min(box.right-root.offsetWidth,innerWidth-root.offsetWidth-12)))}px`}
async function loadUsageSummary(refresh=false){
  if(usageSummaryLoading)return;
  usageSummaryLoading=true;
  const epoch=usageSummaryEpoch;
  const editing=!$('#layoutPreferences').hidden&&!$('#quickStatsOptions').hidden,details=editing||quickStatsLayout.metrics.some(id=>QUICK_STATS_DETAIL_METRICS.includes(id)),needsTimings=editing||quickStatsLayout.metrics.some(id=>['parallelism','agent_time_ms','active_time_ms'].includes(id));
  const reload=refresh||!usageSummaryCache.get(details)?.fresh;
  renderUsageSummaryControls();
  const timings=needsTimings?window.pharosConcurrencySummary?.load(reload).catch(()=>{}):null;
  try{
    await usageSummaryCache.load(details,()=>api('/api/usage/summary'+(details?'?details=1':'')),entry=>{
      if(epoch!==usageSummaryEpoch)return;
      usageSummaryData=entry.data;usageSummaryUpdatedAt=new Date(entry.at);usageSummaryError='';quickStatsEditor?.refresh();renderUsageSummary();
    },refresh);
  }catch(error){if(epoch===usageSummaryEpoch)usageSummaryError=`Could not load usage: ${error.message}`}
  await timings;usageSummaryLoading=false;renderUsageSummaryControls();quickStatsEditor?.refresh(usageSummaryError);renderUsageSummary();
  if(epoch!==usageSummaryEpoch&&(!$('#usageSummary').hidden||!$('#quickStatsOptions').hidden))void loadUsageSummary();else scheduleUsageSummary();
}
function setUsageSummaryOpen(open){if(!restoringViewOverlay)window.pharosView?.write('overlay',open?'usage':'none');const root=$('#usageSummary'),toggle=$('#usageSummaryToggle');root.hidden=!open;toggle.setAttribute('aria-expanded',String(open));toggle.classList.toggle('active',open);if(open){renderUsageSummary();void loadUsageSummary()}else scheduleUsageSummary()}
const queueUsageSummaryRefresh=createQuickStatsRefreshQueue(()=>{if(!document.hidden&&(!$('#usageSummary').hidden||!$('#quickStatsOptions').hidden))void loadUsageSummary()});
function invalidateUsageSummary(clear=false){usageSummaryEpoch++;usageSummaryCache.invalidate();if(clear){usageSummaryData=null;usageSummaryUpdatedAt=null;quickStatsEditor?.refresh();renderUsageSummary()}queueUsageSummaryRefresh()}
addEventListener('pharos:day-change',()=>invalidateUsageSummary(true));
addEventListener('pharos:usage-refresh',()=>invalidateUsageSummary());
addEventListener('pharos:data-refresh',()=>invalidateUsageSummary());
// startDayClock owns wake/focus revalidation through pharos:usage-refresh.
$('#usageSummaryToggle').onclick=()=>setUsageSummaryOpen($('#usageSummary').hidden);
addEventListener('pharos:carbon-settings',()=>{quickStatsEditor?.refresh();if(!$('#usageSummary').hidden)renderUsageSummary()});
addEventListener('pharos:concurrency-settings',()=>{quickStatsEditor?.refresh();if(!$('#usageSummary').hidden)renderUsageSummary()});
document.addEventListener('mousedown',event=>{if(!$('#usageSummary').hidden&&!event.target.closest('#usageSummary,#usageSummaryToggle'))setUsageSummaryOpen(false)});
document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!$('#usageSummary').hidden){setUsageSummaryOpen(false);headerToolAnchor($('#usageSummaryToggle'))?.focus()}});
addEventListener('resize',placeUsageSummary);
$('.brand-mark .door-hit').addEventListener('click',event=>{if(!brandOption('#lighthouseSpinOnClick'))return;event.preventDefault();event.stopPropagation();if(!$('.brand').classList.contains('sweeping')){startBrandTurn(false);renderBrand()}});
function healthCard(root,name,value,detail){const card=node('div','panel');fillHealthCard(card,name,value,detail);root.append(card);return card}
function fillHealthCard(card,name,value,detail){card.replaceChildren(node('div','meta',name),node('div','big',value));if(detail)card.append(node('div','meta health-detail',detail))}
function pricingCard(p){if(!p)return[];const assumed=Number(p.assumed_tokens||0),priced=Number(p.priced_tokens||0)+assumed+Number(p.partial_tokens||0),total=priced+Number(p.unpriced_tokens||0);if(!total)return[];const pct=n=>`${Math.floor(n/total*1000)/10}%`,top=(p.unpriced_models||[]).slice(0,3).map(m=>m.model).join(', ');return[['Known Pricing',pct(priced),`${Number(p.confirmed_changes||0)} confirmed prices · ${Number(p.proposed_changes||0)} awaiting review`+(assumed?` · ${pct(assumed)} by assumption`:'')+(top?` · unpriced: ${top}`:'')]]}
function compactCount(value){const n=Number(value)||0,abs=Math.abs(n);if(abs>=1e6){const units=[[1e6,'M'],[1e9,'B'],[1e12,'T']];let index=abs>=1e12?2:abs>=1e9?1:0,scaled=n/units[index][0],digits=Math.abs(scaled)<10?1:0;if(index<2&&Math.abs(Number(scaled.toFixed(digits)))>=1000){index++;scaled=n/units[index][0];digits=1}return`${scaled.toFixed(digits)}${units[index][1]}`}if(abs>=1e4)return`${Math.round(n/1e3)}k`;return n.toLocaleString()}

function bindReaderView(panel){
 if(!panel||!window.pharosView)return;
 panel.restoreTranscriptView?.();panel.restoreHighlights?.();
 const api=window.pharosView;
 const restoreContent=()=>{const state=api.read('reader_content',{});panel.reveal?.(message=>state['loaded:'+message.id]===true);restoreViewContent(panel)};restoreContent();
 const key=element=>{if(element.dataset.viewKey)return '';if(element.dataset.pharosDisclosure)return element.dataset.pharosDisclosure;const message=element.dataset.messageId||element.sourceMessages?.[0]?.id||element.closest('[data-message-id]')?.dataset.messageId,call=element.dataset.callId||element.sourceMessages?.[0]?.call_id;return call?'call:'+call+':'+(element.classList[0]||'content')+(element.dataset.jsonPath?':'+element.dataset.jsonPath:''):message?'message:'+message+':'+(element.classList[0]||'content')+(element.dataset.jsonPath?':'+element.dataset.jsonPath:''):element.id?'section:'+element.id:''};
 // Keep each native default before applying URL overrides. Restoring an
 // absent/empty override must agree with reopening that URL in a fresh reader.
 const known=new WeakMap();const restore=(force=false,roots=[panel])=>{const opened=new Set(api.read('reader_expanded',[])),closed=new Set(api.read('reader_collapsed',[])),queue=roots.flatMap(root=>[...root.matches('details')?[root]:[],...root.querySelectorAll('details')]),visited=new Set();for(let i=0;i<queue.length;i++){const detail=queue[i];if(visited.has(detail))continue;visited.add(detail);const id=key(detail);if(!id||!force&&known.has(detail))continue;const state=known.get(detail)||{initial:detail.open,current:detail.open};known.set(detail,state);detail.dataset.pharosDisclosure=id;const open=readerDisclosureOpen(id,opened,closed,state.initial);if(open){detail.materialize?.();queue.push(...detail.querySelectorAll('details'))}detail.open=open;state.current=open}};
 restore();
 // Bulk turn changes deliver a toggle event per disclosure. Save one batch
 // per frame: WebKit rejects more than 100 history writes in ten seconds.
 const pending=new Map();let saveFrame=0;
 const flush=()=>{cancelAnimationFrame(saveFrame);saveFrame=0;if(!pending.size)return;let next={expanded:api.read('reader_expanded',[]),collapsed:api.read('reader_collapsed',[])};for(const[id,open]of pending)next=readerDisclosureToggle(id,open,next.expanded,next.collapsed);pending.clear();api.writeAll({reader_expanded:next.expanded,reader_collapsed:next.collapsed})};
 const toggled=event=>{const detail=event.target;if(!(detail instanceof HTMLDetailsElement))return;const id=key(detail),state=known.get(detail);if(!id||state?.current===detail.open)return;if(state)state.current=detail.open;pending.set(id,detail.open);if(!saveFrame)saveFrame=requestAnimationFrame(flush)};
 panel.addEventListener('toggle',toggled,true);
 // New content brings its own details; earlier ones are already known.
 const observer=new MutationObserver(records=>{const roots=records.flatMap(record=>[...record.addedNodes].filter(node=>node instanceof Element));if(roots.length)restore(false,roots)});observer.observe(panel,{childList:true,subtree:true});
 const navigate=()=>{cancelAnimationFrame(saveFrame);saveFrame=0;pending.clear();if(!panel.isConnected||!panel.closest('#detail.active'))return;panel.restoreTranscriptView?.();panel.restoreHighlights?.();restoreContent();restore(true)};
 const events=['popstate','pharos:view','pharos:workspace-history','pharos:route'];for(const name of events)window.addEventListener(name,navigate);
 conversationMapCleanups.push(()=>{flush();panel.removeEventListener('toggle',toggled,true);observer.disconnect();for(const name of events)window.removeEventListener(name,navigate)})
}
function restoreViewOverlay(){const overlay=window.pharosView?.read('overlay','none');restoringViewOverlay=true;try{$('#historyFilter').value=window.pharosView?.read('history_search','')||'';setDrawer(overlay==='history'?'historyDrawer':overlay==='bookmarks'?'bookmarksDrawer':'');setUsageSummaryOpen(overlay==='usage');window.pharosLibrary?.setPanelOpen(overlay==='drive')}finally{restoringViewOverlay=false}}
window.addEventListener('pharos:drive-view-ready',restoreViewOverlay);window.addEventListener('popstate',restoreViewOverlay);window.addEventListener('pharos:view',restoreViewOverlay);
window.addEventListener('pharos:invalid-view',event=>feedbackToast('Some linked view controls could not be restored: '+event.detail.name));
window.addEventListener('pharos:view-ready',()=>{restoreViewOverlay();const panel=document.querySelector('#detail.active .conversation-panel');if(panel)bindReaderView(panel)});

function markTranscriptTarget(target,until=Date.now()+2600){
if(until<=Date.now())return;target.dataset.revealUntil=String(until);target.classList.add('authorship-target');
requestAnimationFrame(()=>{if(target.isConnected)target.scrollIntoView({block:'center'})});
setTimeout(()=>{if(Number(target.dataset.revealUntil)===until){target.classList.remove('authorship-target');delete target.dataset.revealUntil}},Math.max(0,until-Date.now()))
}
function revealMessage(host,id,keepMessage=false,call=''){
if(!keepMessage){const params=new URLSearchParams(location.search);params.delete('message');params.delete('call');history.replaceState(null,'',location.pathname+(params.size?`?${params}`:''))}
const panel=host.querySelector('.conversation-panel');panel?.reveal?.(message=>message.id===id&&(!call||message.call_id===call));
const selector=`[data-message-id="${CSS.escape(id)}"]`+(call?`[data-call-id="${CSS.escape(call)}"]`:''),target=id&&(host.querySelector('.event-body'+selector)||host.querySelector(selector));
panel?.querySelector('.linked-message-unavailable')?.remove();
if(!target){if(panel&&id){const notice=node('p','coverage-warning linked-message-unavailable','The linked message or tool call is not retained in this conversation.');notice.setAttribute('role','status');panel.querySelector('.conversation-head')?.append(notice)}host.scrollIntoView({block:'start'});return}
exemptFromTranscriptFilter(panel,target);for(let el=target;el&&el!==host;el=el.parentElement){if(el.tagName==='DETAILS')el.open=true}
markTranscriptTarget(target)
}
// Each health section loads on its own request, so fast cards render while slow ones (message counts, pricing) still show a placeholder.
let healthLoad=0,healthWriting=null;
// The query-table bundle loads after this script; work that reads through it waits for it.
function queryTablesReady(){return window.pharosQueryTables?Promise.resolve(window.pharosQueryTables):new Promise(resolve=>window.addEventListener('pharos:search-ready',()=>resolve(window.pharosQueryTables),{once:true}))}
function usageLink(card,view){card.classList.add('health-link');card.setAttribute('role','link');card.tabIndex=0;card.title=view==='writing'?'Open Human Words in Analyze':view==='carbon'?'Open Carbon Impact in Analyze':'Open Machine Tokens in Analyze';card.querySelector('.health-open')||card.append(node('div','meta health-open','Open in Analyze →'));card.onclick=()=>{history.pushState(null,'','/analyze?analyze_topic='+(view==='writing'?'human':view));routeFromLocation()};card.onkeydown=event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();card.onclick()}}}
function formatCost(value){return pharosCurrency.formatUSD(Number(value)||0)}
function carbonMass(value){const grams=Number(value)||0;if(grams>=1e6)return`${Number((grams/1e6).toPrecision(3)).toLocaleString()} t`;if(grams>=1e3)return`${Number((grams/1e3).toPrecision(3)).toLocaleString()} kg`;return`${Number(grams.toPrecision(3)).toLocaleString()} g`}
function syncAge(value){const seconds=Math.max(0,Math.floor((Date.now()-Date.parse(value))/1000));if(!Number.isFinite(seconds))return'Time unavailable';if(seconds<60)return`${seconds}s ago`;const minutes=Math.floor(seconds/60);if(minutes<60)return`${minutes}m ago`;const hours=Math.floor(minutes/60);if(hours<24)return`${hours}h ago`;return`${Math.floor(hours/24)}d ago`}
setInterval(()=>{if(!$('#settings').classList.contains('active'))return;for(const age of document.querySelectorAll('#healthCards [data-last-data-at]'))age.textContent=syncAge(age.dataset.lastDataAt)},1000);
function loadHealth(){const load=++healthLoad,root=$('#healthCards'),cards={};let messageTotal='…',typedMessageDetail='Loading typed messages…',countsReady=false;root.replaceChildren();for(const [key,name] of [['writing','Words you wrote'],['tokens','Tokens used'],['carbon','CO₂ totals'],['pricing','Known Pricing'],['workspaces','Workspaces'],['messages','Messages'],['index','Index size'],['sync','Latest captured data by Mac']]){cards[key]=healthCard(root,name,'…');cards[key].setAttribute('aria-busy','true')}usageLink(cards.carbon,'carbon');const fill=(key,name,value,detail)=>{fillHealthCard(cards[key],name,value,detail);cards[key].removeAttribute('aria-busy')},refreshMessageCard=()=>{if(countsReady)fill('messages','Messages',messageTotal,typedMessageDetail)},section=(name,keys,render)=>api('/api/health/'+name).then(h=>{if(load===healthLoad)render(h)},()=>{if(load===healthLoad)for(const key of keys)fill(key,cards[key].firstChild.textContent,'Unavailable')});
section('counts',['workspaces','messages'],h=>{const workspaces=Number(h.workspaces),conversations=Number(h.conversations),messages=Number(h.messages);fill('workspaces','Workspaces',Number.isFinite(workspaces)?workspaces.toLocaleString():'Unavailable',Number.isFinite(conversations)?`${conversations.toLocaleString()} conversations`:'Conversation count unavailable');if(Number.isFinite(conversations))cards.workspaces.dataset.conversationCount=String(conversations);messageTotal=Number.isFinite(messages)?messages.toLocaleString():'Unavailable';countsReady=true;refreshMessageCard()});
window.pharosStorageBreakdown?.load($('#storageBreakdown'),()=>api('/api/health/storage/breakdown'),()=>load===healthLoad&&$('#settings').classList.contains('active'));
section('storage',['index'],s=>{fill('index','Index size',formatBytes(s.index_bytes),`${s.free_bytes==null?'Unavailable':formatBytes(s.free_bytes)} free on ${s.volume_name||'archive drive'}`)});
section('carbon',['carbon'],data=>{const windows=data.windows||[],all=windows.find(window=>window.key==='all'),recent=windows.find(window=>window.key==='30d'),total=Number(all?.co2e_g?.central),last30=Number(recent?.co2e_g?.central);fill('carbon','CO₂ totals',Number.isFinite(total)?`${carbonMass(total)} CO₂e`:'Unavailable',Number.isFinite(last30)?`${carbonMass(last30)} in the last 30 days · central estimate`:'Central estimate · all time');usageLink(cards.carbon,'carbon')});
api('/api/index').then(index=>{if(load!==healthLoad)return;const hosts=index.hosts||[];fill('sync','Latest captured data by Mac',hosts.length?'No captured data':'No captures yet');const list=node('div','health-host-list');let oldest=null;for(const host of hosts){const sources=(host.sources||[]).map(source=>({...source,last_data_at:source.last_data_at||source.captured_at})),latest=sources.filter(source=>Number.isFinite(Date.parse(source.last_data_at))).sort((a,b)=>Date.parse(b.last_data_at)-Date.parse(a.last_data_at))[0],row=node('div','meta');row.append(node('span','',host.label||host.id));const age=node('span','',latest?syncAge(latest.last_data_at):'No captured data');if(latest){age.title=timeLabel(latest.last_data_at);age.dataset.lastDataAt=latest.last_data_at;if(!oldest||Date.parse(latest.last_data_at)<Date.parse(oldest))oldest=latest.last_data_at}row.append(age);list.append(row)}if(oldest){const summary=cards.sync.querySelector('.big');summary.textContent=syncAge(oldest);summary.title=timeLabel(oldest);summary.dataset.lastDataAt=oldest}cards.sync.append(list)},()=>{if(load===healthLoad)fill('sync','Latest captured data by Mac','Unavailable')});
section('pricing',['pricing','tokens'],p=>{const card=pricingCard(p)[0];if(card){fill('pricing',...card);usageLink(cards.pricing,'tokens')}else cards.pricing.remove();fill('tokens','Tokens used',compactCount(p.total_tokens)+(p.is_estimate?'*':''),`${p.is_estimate?'* Includes estimated output; cost is partial. ':''}${p.cost_usd==null?'No priced usage':`${formatCost(p.cost_usd)}${p.is_estimate?'*':''} API cost`} · ${compactCount(p.recent_tokens)} in the last 30 days`);usageLink(cards.tokens,'tokens')});
healthWriting?.abort();const writing=healthWriting=new AbortController();api('/api/authorship').then(a=>{if(load!==healthLoad)return;if(!a.built_at){fill('writing','Words you wrote','Building…','Estimating new and changed user messages');typedMessageDetail='Estimating new and changed user messages';refreshMessageCard();usageLink(cards.writing,'writing');return}return queryTablesReady().then(tables=>tables.writingTotals(t=>{if(load!==healthLoad)return;fill('writing','Words you wrote',compactCount(t.typed),`${compactCount(t.recentTyped)} in the last 30 days`);typedMessageDetail=`${t.typedMessages.toLocaleString()} messages with typed text`;refreshMessageCard();usageLink(cards.writing,'writing')},writing.signal))}).catch(()=>{if(load===healthLoad&&!writing.signal.aborted){fill('writing','Words you wrote','Unavailable');typedMessageDetail='Typed-message count unavailable';refreshMessageCard()}});
}
window.pharosOpenDetail=detail;
// React pages navigate through the shell so history, bookmarks, and the navigator stay in step.
window.pharosNavigate=openHistoryEntry;
// The Mac app's context menu (PharosApp.swift) runs these.
window.pharosLighthouseLoadingIndicator=()=>brandOption('#lighthouseLoadingIndicator');
window.pharosPageAction=action=>{if(action==='logs')openHistoryEntry('/settings/logs');else if(action==='feedback')window.pharosSendFeedback?.();else if(action==='annotate')window.pharosFeedback?.annotate();else if(action==='lighthouse-loading'){const option=BRAND_OPTIONS.find(option=>option.toggle==='#lighthouseLoadingIndicator');setBrandOption(option,!option.on)}};
window.addEventListener('pharos:bookmarks',()=>{syncBookmarkState();if($('#bookmarksDrawer').classList.contains('open'))renderBookmarks()});
document.querySelector('button[data-view=tools]')?.remove();window.pharosNameVisit=page=>recordVisit(true,page);
// Query Table renders its own SVG controls. Keep its React-owned nodes intact:
// replacing their children after render breaks grouped column changes.
window.pharosMessageCard=messageCard;
window.pharosLoadOriginal=loadOriginal;
window.pharosMessageSummary=compactMessageSummary;
window.pharosTurnSummaries=turnSummaryData;
window.pharosCopyText=copyFeedbackText;
window.pharosFeedback={annotations:()=>feedbackAnnotations.map(item=>({...item})),clear:ids=>{feedbackAnnotations=feedbackAnnotations.filter(item=>!ids.includes(item.id));saveFeedback()},toast:feedbackToast,annotate:()=>{headerControls?.show('annotations');setFeedbackEnabled(true);setFeedbackMode(true)}};
renderFeedback();if($('#search'))search();
routeFromLocation();
</script><script src="/assets/ask-model.js"></script><script src="/assets/ask-glow.js"></script><script src="/assets/ask-prompt.js"></script><script src="/assets/ask-pharos.js"></script><script src="/assets/query-tables.js"></script></body></html>'''
