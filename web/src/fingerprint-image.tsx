import React, {useMemo, useSyncExternalStore} from 'react';
import {fingerprintImageCache, type FingerprintRequest} from './fingerprint-image-model';
import {preferences} from './preferences';
import {PageLink} from './nested-pages';

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
const subscribeEvents = (fn: () => void) => {window.addEventListener('pharos:fingerprint-events-changed', fn); return () => window.removeEventListener('pharos:fingerprint-events-changed', fn);};
const showEvents = () => preferences().get<boolean>('pharos-fingerprint-events', true) !== false;

export function FingerprintImage({id, workspace = false, profile = 'wide'}: {id: string; workspace?: boolean; profile?: 'wide' | 'card'}) {
 const events = useSyncExternalStore(subscribeEvents, showEvents, () => true), theme = useSyncExternalStore<'light' | 'dark'>(subscribeTheme, resolvedTheme, () => 'light');
 const request = useMemo<FingerprintRequest>(() => ({id, entity: workspace ? 'workspace' : 'conversation', profile, events}), [id, workspace, profile, events]);
 const subscribe = useMemo(() => (fn: () => void) => id ? cache.subscribe(request, fn) : () => {}, [request, id]);
 const snapshot = useSyncExternalStore(subscribe, () => cache.snapshot(request));
 if (!id) return <span className="muted">—</span>;
 const image = snapshot.image, src = image?.images[theme], height = profile === 'wide' ? 128 : 256;
 const label = `${workspace ? 'Workspace' : 'Conversation'} fingerprint`;
 const picture = src ? <img src={src} width={1024} height={height} alt={label} decoding="async" loading="lazy" onError={() => cache.failed(request)}/> : <span className="muted">{snapshot.error || (image?.state === 'not_found' ? 'No retained conversation' : image?.state === 'retrying' ? 'Fingerprint unavailable' : 'Preparing fingerprint…')}</span>;
 return <div className={'message-label-fingerprint fingerprint-image is-' + profile} title={image?.stale ? 'Refreshing fingerprint' : snapshot.error}>
  {src && image?.href ? <PageLink href={image.href} aria-label={`Open ${workspace ? 'workspace' : 'conversation'}`}>{picture}</PageLink> : picture}
 </div>;
}
