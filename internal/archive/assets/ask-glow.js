// Shared ribbon, contour, palette and working motion for any Ask Pharos surface.
// Attach with createPharosGlow(element, {radius}) or {outline} for a 24×24 SVG
// glyph, call setWorking(boolean), and
// destroy it with its owning UI. Text and surface fills never receive hue filters.
let pharosGlowSerial=0;

function askGlowMotion(host,layers) {
  const reduced=matchMedia('(prefers-reduced-motion: reduce)');
  // Animate compositor filters on decoration only. An inherited custom property
  // on the host restyles and repaints its SVG/gradients every frame in WebKit.
  const hues=layers.map(layer=>layer.animate({filter:['hue-rotate(0deg)','hue-rotate(360deg)']},{duration:120000,iterations:Infinity}));
  const syncMotion=()=>hues.forEach(hue=>reduced.matches?hue.pause():hue.play());
  syncMotion();
  reduced.addEventListener('change',syncMotion);
  let working=false;
  return {
    setWorking(value) {
      const next=Boolean(value);
      if(host.dataset.glowWorking!==String(next))host.dataset.glowWorking=String(next);
      if(next===working)return;
      working=next;
      // Preserve the current hue when speed changes, including on completion.
      hues.forEach(hue=>hue.updatePlaybackRate(working?6:1));
    },
    destroy() {
      reduced.removeEventListener('change',syncMotion);
      hues.forEach(hue=>hue.cancel());
      delete host.dataset.glowWorking;
    },
  };
}

function askGlowTracePath(contour) {
  const path=contour.trim();
  const start=path.match(/^M\s*([-\d.e+]+)[,\s]+([-\d.e+]+)/i);
  if(!start||!/[zZ]$/.test(path))throw new Error('Glow trace needs one closed contour');
  // Traverse two laps as one open path. The moving dash stays away from its
  // ends, so crossing the contour's origin never splits it into rounded caps.
  const lap=path.replace(/[zZ]$/,`L ${start[1]} ${start[2]}`);
  return lap+' '+lap.slice(start[0].length);
}

function askWrapRegions(art,key,w,h,r,contour) {
  const ns = 'http://www.w3.org/2000/svg';
  const add = (tag,attrs,parent) => {
    const element=document.createElementNS(ns,tag);
    Object.entries(attrs).forEach(([name,value])=>element.setAttribute(name,value));parent.append(element);return element;
  };
  let defs=art.querySelector('defs');
  if(!defs){defs=document.createElementNS(ns,'defs');art.prepend(defs);}
  const boxId=key+'-box',contourId=key+'-contour',outsideId=key+'-outside';
  if(!art.querySelector('#'+boxId)){
    add('path',{fill:'white',stroke:'none'},add('clipPath',{id:boxId,clipPathUnits:'userSpaceOnUse'},defs));
    add('path',{fill:'white',stroke:'none'},add('clipPath',{id:contourId,clipPathUnits:'userSpaceOnUse'},defs));
    const mask=add('mask',{id:outsideId,maskUnits:'userSpaceOnUse',maskContentUnits:'userSpaceOnUse',style:'mask-type:luminance'},defs);
    add('rect',{fill:'white',stroke:'none'},mask);
    add('path',{fill:'black',stroke:'none'},add('g',{'clip-path':'url(#'+boxId+')'},mask));
  }
  const box=`M ${1+r} 1 H ${w-1-r} Q ${w-1} 1 ${w-1} ${1+r} V ${h-1-r} Q ${w-1} ${h-1} ${w-1-r} ${h-1} H ${1+r} Q 1 ${h-1} 1 ${h-1-r} V ${1+r} Q 1 1 ${1+r} 1 Z`;
  art.querySelector('#'+boxId+' path').setAttribute('d',box);
  art.querySelector('#'+contourId+' path').setAttribute('d',contour);
  const outside=art.querySelector('#'+outsideId);
  Object.entries({x:-16,y:-16,width:w+32,height:h+32}).forEach(([name,value])=>{
    outside.setAttribute(name,value);outside.querySelector('rect').setAttribute(name,value);
  });
  outside.querySelector('path').setAttribute('d',contour);
  return {box,boxClip:'url(#'+boxId+')',contourClip:'url(#'+contourId+')',outsideMask:'url(#'+outsideId+')'};
}

function createPharosGlow(host,{radius=17,outline=null}={}) {
  const key='pharos-glow-'+(++pharosGlowSerial);
  const paint=document.createElement('span');
  paint.className='pharos-glow-paint';
  paint.setAttribute('aria-hidden','true');
  paint.innerHTML=`<span class="pharos-glow-aura"></span><svg class="pharos-glow-surface" aria-hidden="true" focusable="false"><path/></svg>
  <span class="pharos-glow-edge"><svg class="pharos-edge-art" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id="KEY-edge-cool" x1="0" y1="1" x2="1" y2="0" gradientUnits="objectBoundingBox">
        <stop offset="0" stop-color="var(--pharos-aqua)" stop-opacity="0"/>
        <stop offset=".24" stop-color="var(--pharos-aqua)" stop-opacity=".72"/>
        <stop offset=".53" stop-color="var(--pharos-violet)" stop-opacity=".95"/>
        <stop offset=".77" stop-color="var(--pharos-aqua)" stop-opacity=".7"/>
        <stop offset="1" stop-color="var(--pharos-aqua)" stop-opacity="0"/>
      </linearGradient>
      <linearGradient id="KEY-edge-warm" x1="0" y1="1" x2="1" y2="0" gradientUnits="objectBoundingBox">
        <stop offset="0" stop-color="var(--pharos-gold)" stop-opacity="0"/>
        <stop offset=".27" stop-color="var(--pharos-gold)" stop-opacity=".85"/>
        <stop offset=".58" stop-color="var(--pharos-violet)" stop-opacity=".8"/>
        <stop offset=".81" stop-color="var(--pharos-gold)" stop-opacity=".6"/>
        <stop offset="1" stop-color="var(--pharos-violet)" stop-opacity="0"/>
      </linearGradient>
      <filter id="KEY-edge-halo" x="-30%" y="-50%" width="160%" height="200%" color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="3"/></filter>
      <filter id="KEY-edge-trace-glow" x="-10%" y="-30%" width="120%" height="160%" color-interpolation-filters="sRGB"><feGaussianBlur stdDeviation="1.6"/></filter>
    </defs>
    <path class="pharos-edge-hairline"/>
    <g class="pharos-edge-halo" filter="url(#KEY-edge-halo)"><path data-contour="cool" stroke="url(#KEY-edge-cool)"/><path data-contour="warm" stroke="url(#KEY-edge-warm)"/></g>
    <g class="pharos-edge-lines pharos-edge-cool" stroke="url(#KEY-edge-cool)"><path/><path/><path/><path/><path/></g>
    <g class="pharos-edge-lines pharos-edge-warm" stroke="url(#KEY-edge-warm)"><path/><path/><path/><path/></g>
    <path class="pharos-edge-trace pharos-edge-trace-halo" pathLength="2000" filter="url(#KEY-edge-trace-glow)"/>
    <path class="pharos-edge-trace" pathLength="2000"/>
  </svg></span>`.replaceAll('KEY',key);
  host.classList.add('pharos-glow');
  if(outline){
    host.classList.add('pharos-glow-icon');
    // Light mode can tighten the halo while dark mode keeps the SVG filters.
    paint.querySelector('.pharos-edge-halo').style.filter=`var(--pharos-icon-halo-filter,url(#${key}-edge-halo))`;
    paint.querySelector('.pharos-edge-trace-halo').style.filter=`var(--pharos-icon-halo-filter,url(#${key}-edge-trace-glow))`;
  }
  host.prepend(paint);
  const motion=askGlowMotion(host,[paint.querySelector('.pharos-glow-aura'),paint.querySelector('.pharos-glow-edge')]);
  let edgeSize='';
  function drawEdge() {
    const {width:w,height:h} = host.getBoundingClientRect();
    if (!w || !h || edgeSize === w + ':' + h) return;
    edgeSize = w + ':' + h;
    if(outline){
      // Follow the glyph itself, sharing its source path with the visible icon.
      paint.querySelectorAll('svg').forEach(svg=>svg.setAttribute('viewBox','0 0 24 24'));
      paint.querySelectorAll('[data-contour]').forEach(path=>path.setAttribute('d',outline));
      paint.querySelectorAll('.pharos-edge-trace').forEach(path=>path.setAttribute('d',askGlowTracePath(outline)));
      paint.querySelectorAll('.pharos-edge-lines').forEach(group=>group.firstElementChild.setAttribute('d',outline));
      return;
    }
    const r=Math.max(0,Math.min(radius,w/2-1,h/2-1)),scale=Math.min(1,r/17);
    const viewBox = [-16,-16,w+32,h+32].join(' ');
    paint.querySelector('.pharos-edge-art').setAttribute('viewBox',viewBox);
    paint.querySelector('.pharos-glow-surface').setAttribute('viewBox',viewBox);
    const hairline = paint.querySelector('.pharos-edge-hairline');
    const contour = (i,opposite=false) => {
      const p = (x,y) => opposite ? `${w-x} ${h-y}` : `${x} ${y}`;
      const cornerX=Math.min(27*scale,w*.22),cornerY=Math.min(27*scale,h*.22),edgeX=(-1.5-i*.65)*scale,topY=(-1.6-i*1.1)*scale;
      const arc=.5522847498,rx=cornerX-edgeX,ry=cornerY-topY;
      // A local quarter ellipse keeps a round corner at any conversation height.
      // Horizontal and vertical handles meet the long edges without a kink.
      return `M ${p(w*.68,1.2+i*.15)}
        C ${p(w*.5,.8-i*.65)}, ${p(w*.32,topY)}, ${p(cornerX,topY)}
        C ${p(cornerX-arc*rx,topY)}, ${p(edgeX,cornerY-arc*ry)}, ${p(edgeX,cornerY)}
        C ${p(edgeX,Math.min(cornerY+24,h*.48))}, ${p(1-i*.15,h*.48)}, ${p(1-i*.15,h*.7)}`;
    };
    // A small phase shift for each adjacent strand, retaining the same palette.
    const defs = paint.querySelector('.pharos-edge-art defs');
    ['cool','warm'].forEach(kind => paint.querySelectorAll('.pharos-edge-'+kind+' path').forEach((path,i) => {
      const id = key+'-strand-'+kind+'-'+i;
      let gradient = paint.querySelector('#'+id);
      if (!gradient) {
        gradient = document.createElementNS('http://www.w3.org/2000/svg','linearGradient');
        gradient.id = id; gradient.setAttribute('href','#'+key+'-edge-'+kind);
        gradient.setAttribute('gradientTransform',`translate(${i/36} ${-i/36})`);
        defs.append(gradient);
      }
      path.setAttribute('stroke','url(#'+id+')');
      path.setAttribute('d',contour(i+(kind==='warm'?.35:0)));
    }));
    paint.querySelector('.pharos-edge-warm').setAttribute('transform',`translate(${w} ${h}) rotate(180)`);
    paint.querySelector('[data-contour="cool"]').setAttribute('d',contour(1.5));
    const warmHalo = paint.querySelector('[data-contour="warm"]');
    warmHalo.setAttribute('d',contour(1.5));
    warmHalo.setAttribute('transform',`translate(${w} ${h}) rotate(180)`);
    // The field is the intersection of the ordinary box and ribbon interior.
    // Its border continues to that intersection; decoration is masked outside it.
    const surface = `${contour(0)} V ${h-1-r} Q 1 ${h-1} ${1+r} ${h-1}
      ${contour(.35,true).replace(/^M/,'L')} V ${1+r} Q ${w-1} 1 ${w-1-r} 1 Z`;
    const regions=askWrapRegions(paint.querySelector('.pharos-edge-art'),key,w,h,r,surface);
    const fill=paint.querySelector('.pharos-glow-surface path');fill.setAttribute('d',surface);fill.setAttribute('clip-path',regions.boxClip);
    hairline.setAttribute('d',regions.box);hairline.setAttribute('clip-path',regions.contourClip);
    paint.querySelectorAll('.pharos-edge-halo,.pharos-edge-lines,.pharos-edge-trace').forEach(element=>element.setAttribute('mask',regions.outsideMask));
    paint.querySelectorAll('.pharos-edge-trace').forEach(path => path.setAttribute('d',askGlowTracePath(surface)));
  }
  const observer=new ResizeObserver(drawEdge);
  observer.observe(host);
  drawEdge();
  return {
    draw:drawEdge,
    setWorking:motion.setWorking,
    destroy() {
      observer.disconnect();motion.destroy();paint.remove();
      host.classList.remove('pharos-glow','pharos-glow-icon');
    },
  };
}
