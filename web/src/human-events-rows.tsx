// The pieces of a Human events row that the conversation reader's timeline
// (reader-navigator.tsx) and the workspace Timeline's session lanes
// (label-session-ribbon.tsx) share: a marker per engagement annotation, the
// dimension's letter in its colour, and the card it opens.
import React from 'react';
import type {useRibbonInspector} from './label-ribbon-inspector';
import {PageLink} from './nested-pages';
import {humanEventLetter,humanEventPlacement,humanEventTitle,type HumanEvent,type HumanEventSlot} from './human-events-model';

/** The hover card of a Human events marker: the annotation's label, score and the text it covers, laid out as an agent event's card. */
export function HumanEventCard({event}:{event:HumanEvent}){
  return <div className="event-link-cards"><div className="event-link-card human-event-card">
    <div className="event-link-card-head"><i className={`eng-glyph ${event.className}`}>{humanEventLetter(event.name)}</i><strong>{event.title}</strong><small>{event.score==null?'':`${Math.round(event.score*100)}% · `}in what the person typed</small></div>
    <blockquote className={event.className}>{event.text}</blockquote>
  </div></div>;
}

/** One row's markers, each at its annotation's place and stacked where they would touch (humanEventPlacement). A marker is a link when `href` gives it somewhere to go (a PageLink, so it opens inside the app like the Timeline's other source links), and otherwise a button that calls `jump`. */
export function HumanEventMarkers({events,slots,trackPx,height,inspector,jump,href}:{events:HumanEvent[];slots:Map<string,HumanEventSlot>;trackPx:number;height:number;inspector:ReturnType<typeof useRibbonInspector>;jump?:(event:HumanEvent)=>void;href?:(event:HumanEvent)=>string|null}){
  const placed=humanEventPlacement(events,slots,trackPx,height);
  return <>{events.map(event=>{
    const at=placed.get(event.id);if(!at)return null;const card=<HumanEventCard event={event}/>,link=href?.(event);
    const shared={className:`reader-human-event ${event.className}`,style:{left:`${at.x/trackPx*100}%`,top:at.y},'data-human-event':event.id,'aria-label':humanEventTitle(event),
      onPointerEnter:(pointer:React.PointerEvent<HTMLElement>)=>inspector.show(pointer.currentTarget,card,pointer.clientX),onPointerLeave:inspector.leave,onFocus:(focus:React.FocusEvent<HTMLElement>)=>inspector.show(focus.currentTarget,card),onBlur:inspector.leave};
    const glyph=<i className="eng-glyph">{humanEventLetter(event.name)}</i>;
    return link?<PageLink key={event.id} {...shared} href={link} onClick={click=>{click.stopPropagation();inspector.close();}}>{glyph}</PageLink>
      :<button key={event.id} type="button" {...shared} onClick={click=>{click.preventDefault();click.stopPropagation();inspector.close();jump?.(event);}}>{glyph}</button>;
  })}</>;
}
