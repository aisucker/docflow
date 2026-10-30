import {useEffect,useRef} from 'react';
import type {RouteTask,Step} from './model';

export type RoutePlan={configured:boolean;workflow_key?:string;name?:string;version?:number;definition?:{steps:Step[]};instance_id?:string;instance_status?:string;current_step?:number};
const kinds:Record<string,string>={approval:'Согласование',task:'Исполнение',acknowledgement:'Ознакомление',registration:'Регистрация',signing:'Подписание ЭЦП'};
const labels:Record<string,string>={upcoming:'Ожидает',active:'В работе',done:'Завершён',rejected:'Отклонён',returned:'На доработке',skipped:'Пропущен',cancelled:'Отменён'};
export function routeStageStatus(plan:RoutePlan,tasks:RouteTask[],index:number){
 if(!plan.instance_id)return 'upcoming';
 const stage=tasks.filter(t=>t.instance_id===plan.instance_id&&t.step_index===index);
 if(stage.some(t=>t.status==='rejected'))return 'rejected';
 if(stage.some(t=>t.status==='returned'))return 'returned';
 if(plan.definition?.steps[index]?.mode==='any'&&stage.some(t=>['approved','completed'].includes(t.status)))return 'done';
 if(stage.some(t=>['pending','in_progress'].includes(t.status)))return 'active';
 if(stage.some(t=>['approved','completed'].includes(t.status)))return 'done';
 if(stage.length)return 'cancelled';
 if(index<(plan.current_step||0)||['approved','completed'].includes(plan.instance_status||''))return 'skipped';
 return 'upcoming';
}

export default function DocumentRoute({plan,tasks,onProgress}:{plan:RoutePlan|null;tasks:RouteTask[];onProgress:()=>void}){
 const steps=plan?.definition?.steps||[];
 const strip=useRef<HTMLOListElement>(null);
 const current=plan?steps.findIndex((_,i)=>routeStageStatus(plan,tasks,i)==='active'):-1;
 useEffect(()=>{const list=strip.current;const active=list?.children[current] as HTMLElement|undefined;if(list&&active)list.scrollTo({left:Math.max(0,active.offsetLeft-list.offsetLeft-12),behavior:'smooth'})},[plan?.instance_id,current]);
 return <section className="document-route" aria-label="Маршрут документа"><div className="document-route-heading"><div><span className="eyebrow">Маршрут документа</span>{plan?.configured&&<strong>{plan.name}<small>версия {plan.version}</small></strong>}</div><button type="button" className="document-route-details" onClick={onProgress}>Ход исполнения →</button></div>
 {plan?.configured&&steps.length>0?<ol ref={strip} className="document-route-steps">{steps.map((step,i)=>{
  const state=routeStageStatus(plan,tasks,i);
  const assignees=[...new Set(tasks.filter(t=>t.instance_id===plan.instance_id&&t.step_index===i).map(t=>t.assignee_name))];
  const parallel=Boolean(step.parallel_group&&i>0&&steps[i-1].parallel_group===step.parallel_group);
  return <li key={step.id} className={'document-route-step '+state+(parallel?' parallel':'')} aria-current={state==='active'?'step':undefined}><span className="document-route-index" aria-hidden="true">{state==='done'?'✓':state==='skipped'?'–':i+1}</span><div className="document-route-step-body"><div className="document-route-step-meta"><span>{kinds[step.kind||'approval']}</span>{parallel&&<span className="document-route-parallel">Параллельно</span>}</div><strong>{step.title}</strong><span className="document-route-state">{labels[state]}</span>{assignees.length>0&&<small className="document-route-assignees" title={assignees.join(', ')}>{assignees.join(', ')}</small>}</div></li>
 })}</ol>:<p className="document-route-empty">Маршрут для этого типа документа пока не настроен.</p>}
 </section>
}
