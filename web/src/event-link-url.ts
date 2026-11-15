import {conversationRowURL} from './usage-row-model';
import type {AgentEventLink} from './event-link-layout';
/** The conversation reader at an event, with agent events highlighted and the
 * event's link to its target selected (reader_event_link). */
export function eventLinkURL(conversation:{id:string;work_id?:string|null;messages?:{id:string;conversation_id?:string;work_id?:string|null}[]},e:Pick<AgentEventLink,'id'|'message_id'>):string|null{
 const message=conversation.messages?.find(message=>message.id===e.message_id),source=message?.conversation_id??conversation.id;
 // A workspace fingerprint deliberately contains several source conversations;
 // a conversation fingerprint can contain inherited text from another workspace.
 const workspace=message?.work_id??(source===conversation.id||conversation.id===conversation.work_id?conversation.work_id:undefined);
 const base=conversationRowURL({conversation_id:source,workspace_id:workspace},e.message_id);if(!base)return null;
 const url=new URL(base,'http://pharos.invalid');url.searchParams.set('reader_events','1');url.searchParams.set('reader_event_link',e.id);
 return url.pathname+url.search;
}
/** A reader URL (ribbonSourceURL) with agent events highlighted and the event's link selected. */
export function withEventLink(href:string|null,e:Pick<AgentEventLink,'id'>):string|null{
 if(!href)return null;const url=new URL(href,'http://pharos.invalid');url.searchParams.set('reader_events','1');url.searchParams.set('reader_event_link',e.id);
 return url.pathname+url.search;
}
