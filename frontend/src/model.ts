export type Identity = {id:string;email:string;full_name:string;role:string};
export type Session = {access_token:string;refresh_token:string;expires_in:number};
export type FieldDef = {key:string;label:string;type:'string'|'text'|'number'|'boolean'|'date'|'select';required:boolean;options?:string[]};
export type DocType = {id:string;code:string;name:string;fields:FieldDef[];active:boolean};
export type User = Identity & {active:boolean;department_id:string;position_id:string};
export type Department = {id:string;name:string;parent_id:string};
export type Position = {id:string;name:string};
export type Document = {id:string;type_id:string;type_name:string;title:string;summary:string;status:string;registration_number:string;author_id:string;author_name:string;assignee_id?:string;department_id?:string;due_at:string|null;registered_at?:string|null;fields?:Record<string,unknown>;created_at:string;updated_at:string};
export type Task = {id:string;document_id:string;document_title:string;step_index:number;title:string;status:string;created_at:string};
export type WorkflowButton = {code:string;label:string;effect:'complete'|'start'|'return'|'reject';style:'primary'|'subtle'|'danger-outline';comment_required:boolean;active:boolean};
export type RouteTask = {actions:WorkflowButton[];kind:string;instructions:string;action_label:string;id:string;step_index:number;step_id:string;title:string;status:string;assignee_name:string;assignee_id:string;comment:string;created_at:string;decided_at:string|null};
export type Event = {id:number;kind:string;actor_name:string;occurred_at:string;detail:Record<string,unknown>};
export type FileEntry = {id:string;filename:string;mime_type:string;version:number;size:number;uploaded_by:string;created_at:string};
export type Template = {id:string;name:string;type_id:string;filename:string};
export type Condition = {field:string;operator:'eq'|'neq'|'gt'|'gte'|'lt'|'lte'|'contains';value:unknown};
export type Step = {kind?:'approval'|'task'|'acknowledgement'|'registration';instructions?:string;button_codes?:string[];actions?:WorkflowButton[];id:string;title:string;mode:'all'|'any';assignee_ids:string[];condition?:Condition};
export type Workflow = {id:string;workflow_key:string;version:number;name:string;doc_type_id:string;active?:boolean;definition?:{steps:Step[];completion_status?:'approved'|'completed'}};
export const date = (s?:string|null) => s ? new Date(s).toLocaleString('ru-RU',{day:'2-digit',month:'2-digit',year:'numeric',hour:'2-digit',minute:'2-digit'}):'—';
export const statusName:Record<string,string> = {draft:'Черновик',in_progress:'В работе',completed:'Исполнен',in_review:'На согласовании',approved:'Согласован',rejected:'Отклонён',returned:'На доработке',pending:'Ожидает',cancelled:'Отменён'};
export function readSession():Session|null{try {return JSON.parse(sessionStorage.getItem('docflow_session')||'null')}catch{return null}}
export function writeSession(s:Session|null){if(s)sessionStorage.setItem('docflow_session',JSON.stringify(s));else sessionStorage.removeItem('docflow_session')}
let refreshing:Promise<Session>|null=null;
async function refreshSession(previous:Session):Promise<Session>{
 const current=readSession();
 if(current&&current.access_token!==previous.access_token)return current;
 if(!refreshing){refreshing=(async()=>{
  const rr=await fetch('/api/v1/auth/refresh',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({refresh_token:previous.refresh_token})});
  if(!rr.ok){if(rr.status===401){writeSession(null);window.dispatchEvent(new window.Event('docflow:unauthorized'))}throw new Error('Не удалось обновить сессию. Повторите вход.')}
  const next:Session=await rr.json();writeSession(next);return next;
 })().finally(()=>{refreshing=null})}
 return refreshing;
}
export async function request<T>(path:string,opts:RequestInit={}):Promise<T>{
 const session=readSession();const head:Record<string,string>={...(opts.headers as Record<string,string>||{})};if(session)head.Authorization=`Bearer ${session.access_token}`;
 if(opts.body && !(opts.body instanceof FormData))head['Content-Type']='application/json';
 const doFetch=()=>fetch('/api/v1'+path,{...opts,headers:head});let response=await doFetch();
 if(response.status===401&&session?.refresh_token&&path!=='/auth/refresh'&&path!=='/auth/login'){
  const next=await refreshSession(session);head.Authorization=`Bearer ${next.access_token}`;response=await doFetch();
 }
 if(!response.ok){const result=await response.json().catch(()=>({error:response.statusText}));throw new Error(result.error||`HTTP ${response.status}`)}
 if(response.status===204)return undefined as T;return response.json() as Promise<T>;
}
