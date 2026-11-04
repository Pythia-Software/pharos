/** Display known content envelopes without rewriting retained source text.
 * Unknown structured data stays visible, including fields beside a text field. */
export function timelinePreviewText(text:string,{input=false,unwrap=true}:{input?:boolean;unwrap?:boolean}={}):string{
 if(!unwrap)return text;
 const envelope=new Set(['content','message','text','output_text','output','result','thinking','type','role','tool','name','id','details','is_error','status','metadata','usage','model','created_at','timestamp','parent_tool_use_id','session_id','agent_id','tool_use_id','call_id','payload','event','data','response']);
 const textKinds=new Set(['text','input_text','output_text','thinking','reasoning','tool_result']);
 const parse=(value:string):unknown=>{if(!/^[\s]*[\[{\"]/.test(value))return value;try{return JSON.parse(value);}catch{return value;}};
 const readable=(value:unknown,depth:number,args=false):string|null=>{
  if(depth>6)return null;
  if(typeof value==='string'){const decoded=parse(value);if(args&&(typeof decoded!=='object'||decoded===null||Array.isArray(decoded)))return value;return typeof decoded==='string'&&decoded===value?value:readable(decoded,depth+1,args)??value;}
  if(Array.isArray(value)){
   const parts=value.map(part=>typeof part==='string'?readable(part,depth+1):part&&typeof part==='object'&&('text' in part||'content' in part||'thinking' in part||'type' in part)?readable(part,depth+1):null);
   return parts.length&&parts.every(part=>part!==null)?parts.filter(Boolean).join('\n\n'):null;
  }
  if(!value||typeof value!=='object')return null;
  const record=value as Record<string,unknown>,kind=String(record.type??'');
  if(['image','image_url','input_image'].includes(kind))return '[Image]';
  if(['audio','input_audio'].includes(kind))return '[Audio]';
  if(args){
   for(const key of ['input','arguments'])if(key in record){const body=readable(record[key],depth+1,true);if(body!==null)return body;}
   for(const key of ['prompt','message','command','cmd','code'])if(typeof record[key]==='string')return ['command','cmd','code'].includes(key)?String(record[key]):readable(record[key],depth+1)??String(record[key]);
   if(typeof record.description==='string'&&Object.keys(record).every(key=>['description','subagent_type','agent_type','task_name','name','run_in_background'].includes(key)))return record.description;
   const paths=['file_path','path','url','query','pattern'].filter(key=>typeof record[key]==='string');
   const ancillary=new Set(['offset','limit','start_line','end_line','timeout','timeout_ms','workdir','cwd']);
   if(paths.length===1&&Object.keys(record).every(key=>paths.includes(key)||ancillary.has(key)))return String(record[paths[0]]);
  }
  if(('stdout' in record||'stderr' in record)&&Object.keys(record).every(key=>['stdout','stderr','exit_code','status','metadata'].includes(key))){
   const out=typeof record.stdout==='string'?record.stdout:'',err=typeof record.stderr==='string'?record.stderr:'';
   return [out,err?(out?'stderr:\n':'')+err:''].filter(Boolean).join('\n\n');
  }
  if(!Object.keys(record).every(key=>envelope.has(key)))return null;
  if(kind&&!textKinds.has(kind)&&!['message','agent_message','user_message','assistant','user','response','result'].includes(kind))return null;
  for(const key of ['content','message','text','output_text','thinking','output','result','payload','event','data','response'])if(key in record){const body=readable(record[key],depth+1);if(body!==null&&body!=='')return body;}
  return null;
 };
 const parsed=parse(text),body=readable(text,0,input);
 return body??(typeof parsed==='object'&&parsed!==null?JSON.stringify(parsed,null,2):text);
}
