import React, {useMemo, useSyncExternalStore} from 'react';
import {fingerprintImageCache, type FingerprintRequest} from './fingerprint-image-model';
import {PageLink} from './nested-pages';
import {messageLabelRibbonNotes} from './message-label-results-model';

const cache = fingerprintImageCache(async (items, events) => {
 const response = await fetch('/api/fingerprints', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({items: items.map(({entity, id, profile}) => ({entity, id, profile})), events})});
 if (!response.ok) throw new Error('Fingerprint unavailable');
 return (await response.json()).items;
});
let observing = false;
const themeListeners = new Set<() => void>();
function observe() {
 if (observing) return; observing = true;
 window.addEventListener('pharos:usage-refresh', () => cache.invalidate());
 window.addEventListener('pharos:data-refresh', () => cache.invalidate());
 new MutationObserver(() => themeListeners.forEach(fn => fn())).observe(document.documentElement, {attributes: true, attributeFilter: ['data-theme']});
 window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => themeListeners.forEach(fn => fn()));
}
const subscribeTheme = (fn: () => void) => {observe(); themeListeners.add(fn); return () => {themeListeners.delete(fn);};};
const resolvedTheme = (): 'light' | 'dark' => document.documentElement.dataset.theme === 'dark' ? 'dark' : document.documentElement.dataset.theme === 'light' ? 'light' : window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';

export function FingerprintImage({id, workspace = false, profile = 'wide', linkToSource = true}: {id: string; workspace?: boolean; profile?: 'wide' | 'card'; linkToSource?: boolean}) {
 const theme = useSyncExternalStore<'light' | 'dark'>(subscribeTheme, resolvedTheme, () => 'light');
 const request = useMemo<FingerprintRequest>(() => ({id, entity: workspace ? 'workspace' : 'conversation', profile, events: true}), [id, workspace, profile]);
 const subscribe = useMemo(() => (fn: () => void) => id ? cache.subscribe(request, fn) : () => {}, [request, id]);
 const snapshot = useSyncExternalStore(subscribe, () => cache.snapshot(request));
 if (!id) return <span className="muted">—</span>;
 const image = snapshot.image, src = image?.images[theme], height = profile === 'wide' ? 128 : 256;
 const label = `${workspace ? 'Workspace' : 'Conversation'} fingerprint`;
 const picture = src ? <img src={src} width={1024} height={height} alt={label} decoding="async" loading="lazy" onError={() => cache.failed(request)}/> : <span className="muted">{snapshot.error || (image?.state === 'not_found' ? 'No retained conversation' : image?.state === 'retrying' ? 'Fingerprint unavailable' : 'Preparing fingerprint…')}</span>;
 // A bounded sample, labels in progress and withheld events say so beside the image.
 const notes = messageLabelRibbonNotes(image), note = notes.map(n => n.note).join('. ');
 return <div className={'message-label-fingerprint fingerprint-image is-' + profile} title={[image?.stale ? 'Refreshing fingerprint' : snapshot.error, note].filter(Boolean).join('. ') || undefined}>
  {src && image?.href && linkToSource ? <PageLink href={image.href} aria-label={`Open ${workspace ? 'workspace' : 'conversation'}`}>{picture}</PageLink> : picture}
  {notes.length ? <span className="fingerprint-sampled" aria-label={note}>{notes.map(n => n.badge).join(' · ')}</span> : null}
 </div>;
}
