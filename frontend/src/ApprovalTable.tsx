import type {Department,User} from './model';
import UserPicker from './UserPicker';

export type ApprovalStage={id:string;title:string;mode:'all'|'any'|'sequential';user_ids:string[]};
export function approvalStages(value:unknown):ApprovalStage[]{
 if(!Array.isArray(value))return [];
 if(value.length&&typeof value[0]==='string')return [{id:'legacy_stage',title:'Согласование',mode:'all',user_ids:value.map(String)}];
 return value as ApprovalStage[];
}
type Props={value:unknown;onChange?:(stages:ApprovalStage[])=>void;users:User[];departments:Department[];label?:string};
const modeNames={all:'Параллельное · все',any:'Параллельное · любой',sequential:'Последовательное'};
export default function ApprovalTable({value,onChange,users,departments,label='Таблица согласования'}:Props){
 const stages=approvalStages(value);
 function update(index:number,patch:Partial<ApprovalStage>){onChange?.(stages.map((stage,i)=>i===index?{...stage,...patch}:stage))}
 function move(index:number,to:number){const next=[...stages];const [item]=next.splice(index,1);next.splice(to,0,item);onChange?.(next)}
 return <div className="approval-sheet" aria-label={label}>
  <div className="approval-sheet-caption"><span>Этапы выполняются сверху вниз</span><span>{stages.length} {stages.length%10===1&&stages.length%100!==11?'этап':stages.length%10>=2&&stages.length%10<=4&&(stages.length%100<12||stages.length%100>14)?'этапа':'этапов'}</span></div>
  {!stages.length&&<div className="approval-empty">{onChange?'Добавьте этап и выберите участников согласования.':'Участники согласования не выбраны.'}</div>}
  {stages.map((stage,index)=><section className="approval-stage" key={stage.id} aria-label={`Этап согласования ${index+1}`}><div className="approval-stage-heading"><span className="approval-stage-number">{index+1}</span>{onChange?<input aria-label={`Название этапа ${index+1}`} value={stage.title} required maxLength={150} placeholder="Согласование" onChange={e=>update(index,{title:e.target.value})}/>:<strong>{stage.title}</strong>}{onChange?<select aria-label={`Порядок согласования этапа ${index+1}`} value={stage.mode} onChange={e=>update(index,{mode:e.target.value as ApprovalStage['mode']})}>{Object.entries(modeNames).map(([mode,name])=><option value={mode} key={mode}>{name}</option>)}</select>:<span>{modeNames[stage.mode]}</span>}{onChange&&<div className="approval-stage-tools"><button type="button" disabled={index===0} aria-label={`Поднять этап ${index+1}`} onClick={()=>move(index,index-1)}>↑</button><button type="button" disabled={index===stages.length-1} aria-label={`Опустить этап ${index+1}`} onClick={()=>move(index,index+1)}>↓</button><button type="button" aria-label={`Удалить этап ${index+1}`} onClick={()=>onChange(stages.filter((_,i)=>i!==index))}>×</button></div>}</div>
   {onChange?<UserPicker users={users} departments={departments} value={stage.user_ids} onChange={user_ids=>update(index,{user_ids})} multiple required label={`Участники этапа ${index+1}`}/>:<ol className="approval-participants">{stage.user_ids.map(id=><li key={id}>{users.find(u=>u.id===id)?.full_name||'Недоступный пользователь'}</li>)}</ol>}
   {onChange&&stage.mode==='sequential'&&stage.user_ids.length>1&&<ol className="approval-order" aria-label="Очередь согласования">{stage.user_ids.map((id,i)=><li key={id}><span>{i+1}. {users.find(u=>u.id===id)?.full_name||id}</span><button type="button" disabled={!i} aria-label={`Раньше: ${users.find(u=>u.id===id)?.full_name}`} onClick={()=>{const next=[...stage.user_ids];[next[i-1],next[i]]=[next[i],next[i-1]];update(index,{user_ids:next})}}>↑</button><button type="button" disabled={i===stage.user_ids.length-1} aria-label={`Позже: ${users.find(u=>u.id===id)?.full_name}`} onClick={()=>{const next=[...stage.user_ids];[next[i],next[i+1]]=[next[i+1],next[i]];update(index,{user_ids:next})}}>↓</button></li>)}</ol>}
   {onChange&&<small className="approval-stage-hint">{stage.mode==='all'?'Каждый участник должен согласовать документ.':stage.mode==='any'?'Достаточно согласования одного участника.':'Сотрудники согласуют по очереди в указанном порядке.'}</small>}
  </section>)}
  {onChange&&<button type="button" className="button approval-add" disabled={stages.length>=40} onClick={()=>onChange([...stages,{id:'stage_'+crypto.randomUUID().replaceAll('-','').slice(0,12),title:'Согласование',mode:'all',user_ids:[]}])}>＋ Добавить этап</button>}
 </div>;
}
