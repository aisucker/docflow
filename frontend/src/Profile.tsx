import {useEffect,useState,type FormEvent} from 'react';
import {Icon} from './App';
import {date,request,type Identity,type ProfilePreferences,type Task} from './model';

type Overview={pending_tasks:number;overdue_tasks:number;my_documents:number;drafts:number};
type Props={user:Identity;refreshKey:number;onUser:(user:Identity)=>void;onTasks:()=>void;onOverdue:()=>void;onDrafts:()=>void;onDocument:(id:string)=>void;notify:(message:string)=>void};
const roleName:Record<string,string>={admin:'Администратор',registrar:'Регистратор',employee:'Сотрудник'};

export default function Profile({user,refreshKey,onUser,onTasks,onOverdue,onDrafts,onDocument,notify}:Props){
 const [overview,setOverview]=useState<Overview|null>(null);
 const [tasks,setTasks]=useState<Task[]>([]);
 const [fullName,setFullName]=useState(user.full_name);
 const [preferences,setPreferences]=useState<ProfilePreferences>(user.preferences||{compact:false,start_page:'documents'});
 const [saving,setSaving]=useState(false);
 const [message,setMessage]=useState('');
 useEffect(()=>{setFullName(user.full_name);setPreferences(user.preferences||{compact:false,start_page:'documents'})},[user]);
 useEffect(()=>{let active=true;Promise.all([request<Overview>('/me/overview'),request<Task[]>('/tasks')]).then(([a,b])=>{if(active){setOverview(a);setTasks(b)}}).catch(e=>{if(active)notify(e.message)});return()=>{active=false}},[refreshKey,user.id,notify]);
 async function saveName(e:FormEvent){e.preventDefault();setSaving(true);setMessage('');try{const updated=await request<Identity>('/me',{method:'PATCH',body:JSON.stringify({full_name:fullName})});onUser(updated);setMessage('Имя сохранено')}catch(e){notify((e as Error).message)}finally{setSaving(false)}}
 async function savePreferences(e:FormEvent){e.preventDefault();setSaving(true);setMessage('');try{const updated=await request<Identity>('/me/preferences',{method:'PUT',body:JSON.stringify(preferences)});onUser(updated);setMessage('Настройки сохранены')}catch(e){notify((e as Error).message)}finally{setSaving(false)}}
 return <div className="profile-page">
  <section className="profile-hero"><div className="profile-avatar">{user.full_name.slice(0,1).toUpperCase()}</div><div><span className="eyebrow">Личный кабинет</span><h2>{user.full_name}</h2><p>{user.position_name||roleName[user.role]||'Сотрудник'}{user.department_name?' · '+user.department_name:''}</p></div><span className="profile-hero-badge">{roleName[user.role]||user.role}</span></section>
  <div className="profile-stats">
   <button onClick={onTasks}><span>Мои задачи</span><strong>{overview?.pending_tasks??'—'}</strong><small>Открыть список задач →</small></button>
   <button onClick={onOverdue}><span>Просрочено</span><strong className={overview?.overdue_tasks?'danger-text':''}>{overview?.overdue_tasks??'—'}</strong><small>Требуют внимания →</small></button>
   <button onClick={onDrafts}><span>Черновики</span><strong>{overview?.drafts??'—'}</strong><small>Продолжить работу →</small></button>
   <div><span>Мои документы</span><strong>{overview?.my_documents??'—'}</strong><small>Создано вами</small></div>
  </div>
  <div className="profile-grid"><div>
   <section className="profile-panel"><div className="profile-panel-title"><div><h3>Ближайшие задачи</h3><p>Документы, ожидающие вашего действия</p></div><button className="button subtle" onClick={onTasks}>Все задачи</button></div>
    {tasks.slice(0,5).map(t=><button className="profile-task" key={t.id} onClick={()=>onDocument(t.document_id)}><span className="profile-task-icon"><Icon name="file" size={18}/></span><span><strong>{t.title}</strong><small>{t.document_title}</small></span><time className={t.due_at&&new Date(t.due_at)<new Date()?'danger-text':''}>{t.due_at?'До '+date(t.due_at):'Без срока'}</time></button>)}
    {tasks.length===0&&<p className="profile-empty">Открытых задач нет</p>}
   </section>
   <section className="profile-panel"><div className="profile-panel-title"><div><h3>Информация о сотруднике</h3><p>Данные вашего рабочего профиля</p></div></div><dl className="profile-details"><div><dt>Электронная почта</dt><dd>{user.email}</dd></div><div><dt>Подразделение</dt><dd>{user.department_name||'Не указано'}</dd></div><div><dt>Должность</dt><dd>{user.position_name||'Не указана'}</dd></div><div><dt>Роль</dt><dd>{roleName[user.role]||user.role}</dd></div><div><dt>В системе с</dt><dd>{date(user.joined_at)}</dd></div></dl></section>
  </div><div>
   <section className="profile-panel"><h3>Личные данные</h3><p className="muted">Имя отображается в документах и истории действий.</p><form onSubmit={saveName} className="profile-form"><label>Имя и фамилия<input value={fullName} onChange={e=>setFullName(e.target.value)} maxLength={120} required/></label><button className="button primary" disabled={saving||fullName.trim()===user.full_name}>Сохранить имя</button></form></section>
   <section className="profile-panel"><h3>Настройки интерфейса</h3><p className="muted">Настройки привязаны к вашей учётной записи.</p><form onSubmit={savePreferences} className="profile-form"><label>Стартовая страница<select value={preferences.start_page} onChange={e=>setPreferences({...preferences,start_page:e.target.value as ProfilePreferences['start_page']})}><option value="documents">Все документы</option><option value="tasks">Мои задачи</option><option value="profile">Личный кабинет</option></select></label><label className="profile-toggle"><input type="checkbox" checked={preferences.compact} onChange={e=>setPreferences({...preferences,compact:e.target.checked})}/><span><strong>Компактные таблицы</strong><small>Больше документов и задач помещается на экране</small></span></label><button className="button primary" disabled={saving}>Сохранить настройки</button></form></section>
   {message&&<div className="profile-saved" role="status">{message}</div>}
  </div></div>
 </div>
}
