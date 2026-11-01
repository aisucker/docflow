import {useEffect,useState,type FormEvent} from 'react';
import {request,type NotificationPreferences} from './model';

const categories:{key:Exclude<keyof NotificationPreferences,'enabled'|'reminder_hours'>;label:string;hint:string}[]=[
 {key:'task_assigned',label:'Новые задачи',hint:'Вам назначили согласование, исполнение или другой этап'},
 {key:'task_delegated',label:'Поручения',hint:'Другой сотрудник поручил вам свою задачу'},
 {key:'task_completed',label:'Решения по задачам',hint:'Получено согласование или выполнена задача по вашему документу'},
 {key:'document_returned',label:'Возврат на доработку',hint:'Ваш документ вернули с замечаниями'},
 {key:'document_rejected',label:'Отклонение документа',hint:'Ваш документ отклонили'},
 {key:'document_completed',label:'Завершение маршрута',hint:'Ваш документ согласован или исполнен'},
 {key:'due_soon',label:'Приближение срока',hint:'Напоминание об открытой задаче до срока документа'},
 {key:'overdue',label:'Просроченные задачи',hint:'Срок документа истёк, а ваша задача ещё открыта'},
];

export default function NotificationSettings({focusKey}:{focusKey:number}){
 const [settings,setSettings]=useState<NotificationPreferences|null>(null);
 const [saved,setSaved]=useState<NotificationPreferences|null>(null);
 const [busy,setBusy]=useState(false),[error,setError]=useState(''),[message,setMessage]=useState('');
 useEffect(()=>{let active=true;request<NotificationPreferences>('/me/notification-preferences').then(value=>{if(active){setSettings(value);setSaved(value)}}).catch(e=>{if(active)setError(e.message)});return()=>{active=false}},[]);
 const ready=Boolean(settings);
 useEffect(()=>{if(focusKey>0&&ready)document.getElementById('notification-settings')?.scrollIntoView({behavior:'smooth',block:'start'})},[focusKey,ready]);
 async function submit(e:FormEvent){e.preventDefault();if(!settings)return;setBusy(true);setError('');setMessage('');try{const updated=await request<NotificationPreferences>('/me/notification-preferences',{method:'PUT',body:JSON.stringify(settings)});setSettings(updated);setSaved(updated);setMessage('Настройки уведомлений сохранены');window.dispatchEvent(new Event('docjet:notifications-changed'))}catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 return <section className="profile-panel notification-settings" id="notification-settings" aria-label="Настройки уведомлений"><h3>Уведомления и напоминания</h3><p className="muted">Выберите события, которые хотите видеть в центре уведомлений. Настройки применяются к новым уведомлениям.</p>{error&&<p className="form-error" role="alert">{error}</p>}{settings?<form onSubmit={submit} className="profile-form"><label className="profile-toggle notification-master"><input type="checkbox" checked={settings.enabled} disabled={busy} onChange={e=>{setSettings({...settings,enabled:e.target.checked});setMessage('')}}/><span><strong>Получать уведомления в системе</strong><small>Общий переключатель для всех событий и напоминаний</small></span></label><fieldset disabled={busy||!settings.enabled} className="notification-options">{categories.map(category=><label className="profile-toggle" key={category.key}><input type="checkbox" checked={settings[category.key]} onChange={e=>{setSettings({...settings,[category.key]:e.target.checked});setMessage('')}}/><span><strong>{category.label}</strong><small>{category.hint}</small></span></label>)}<label className="notification-hours">Напоминать до срока за<input type="number" min={1} max={168} required value={settings.reminder_hours} onChange={e=>{setSettings({...settings,reminder_hours:Number(e.target.value)});setMessage('')}}/><span>часов</span></label><p className="card-field-hint">От 1 до 168 часов. Проверка выполняется раз в минуту. Для каждой задачи и срока приходит одно предварительное напоминание и одно уведомление о просрочке.</p></fieldset><button className="button primary" disabled={busy||JSON.stringify(settings)===JSON.stringify(saved)}>{busy?'Сохранение…':'Сохранить уведомления'}</button>{message&&<p className="notification-save-message" role="status">{message}</p>}</form>:!error&&<p className="muted">Загрузка настроек…</p>}</section>;
}
