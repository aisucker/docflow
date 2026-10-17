import {useState} from 'react';
import {Modal} from './App';
import {statusName,type RouteTask,type User} from './model';

type Props={tasks:RouteTask[];users:User[];busy:boolean;onDecision:(task:string,action:string,comment:string)=>Promise<boolean>;onDelegate:(task:string,assignee:string,comment:string)=>Promise<boolean>};
type Dialog={task:RouteTask;mode:'decision'|'delegate'};

export default function TaskActions({tasks,users,busy,onDecision,onDelegate}:Props){
 const [dialog,setDialog]=useState<Dialog|null>(null);
 const [comment,setComment]=useState('');
 const [assignee,setAssignee]=useState('');
 function open(task:RouteTask,mode:Dialog['mode']){setDialog({task,mode});setComment('');setAssignee('')}
 async function decide(code:string){if(!dialog)return;const ok=await onDecision(dialog.task.id,code,comment);if(ok)setDialog(null)}
 async function delegate(){if(!dialog||!assignee)return;const ok=await onDelegate(dialog.task.id,assignee,comment);if(ok)setDialog(null)}
 return <><div className="task-action-list" aria-label="Мои действия по этапам">{tasks.map(task=><div className="task-action-row" key={task.id}><div className="task-action-description"><small>Этап {task.step_index+1}{task.delegated_by?` · поручено ${task.delegated_by}`:''}{task.delegated_to?` · поручено ${task.delegated_to}`:''}</small><strong>{task.title}</strong><span className={'status '+task.status}>{statusName[task.status]||task.status}</span></div><div className="task-action-buttons"><button className="button primary" disabled={busy} onClick={()=>open(task,'decision')}>Действия</button>{task.can_delegate&&<button className="button subtle" disabled={busy} onClick={()=>open(task,'delegate')}>Поручить</button>}</div></div>)}</div>
 {dialog&&<Modal onClose={()=>{if(!busy)setDialog(null)}} width="560px"><h2>{dialog.mode==='decision'?'Действие по этапу':'Поручить задачу'}</h2><p className="muted">{dialog.task.title}{dialog.task.instructions?` · ${dialog.task.instructions}`:''}</p>{dialog.mode==='decision'?<><label className="label-block">Комментарий<textarea rows={4} value={comment} onChange={e=>setComment(e.target.value)} placeholder="Укажите комментарий к действию"/></label><div className="dialog-actions task-dialog-actions"><button type="button" className="button subtle" disabled={busy} onClick={()=>setDialog(null)}>Отмена</button>{dialog.task.actions.map(action=><button type="button" key={action.code} className={'button '+action.style} disabled={busy||(action.comment_required&&!comment.trim())} onClick={()=>void decide(action.code)}>{action.label}</button>)}</div></>:<><p className="muted task-delegate-note">Вы и выбранный сотрудник сможете выполнить задачу. После первого решения вторая задача закроется.</p><label className="label-block">Сотрудник<select required value={assignee} onChange={e=>setAssignee(e.target.value)}><option value="">Выберите сотрудника</option>{users.filter(user=>user.active&&user.id!==dialog.task.assignee_id).map(user=><option key={user.id} value={user.id}>{user.full_name}</option>)}</select></label><label className="label-block">Поручение<textarea rows={4} value={comment} onChange={e=>setComment(e.target.value)} placeholder="Что нужно выполнить"/></label><div className="dialog-actions"><button type="button" className="button subtle" disabled={busy} onClick={()=>setDialog(null)}>Отмена</button><button type="button" className="button primary" disabled={busy||!assignee} onClick={()=>void delegate()}>Поручить</button></div></>}</Modal>}
 </>;
}
