import {useEffect,useState,type FormEvent} from 'react';
import {Modal} from './App';
import {defaultCardFields,request,type CardField,type Department,type DocType,type Document,type FieldDef,type User} from './model';

type Props={types:DocType[];users:User[];departments:Department[];onClose:()=>void;saved:(id:string)=>void;notify:(message:string)=>void;initial?:Document};

export default function DocumentEditor({types,users,departments,onClose,saved,notify,initial}:Props){
 const [typeID,setTypeID]=useState(initial?.type_id||types.find(t=>t.active)?.id||'');
 const [title,setTitle]=useState(initial?.title||'');
 const [summary,setSummary]=useState(initial?.summary||'');
 const [number,setNumber]=useState(initial?.registration_number||'');
 const [due,setDue]=useState(initial?.due_at?.substring(0,16)||'');
 const [assignee,setAssignee]=useState(initial?.assignee_id||'');
 const [department,setDepartment]=useState(initial?.department_id||'');
 const [fields,setFields]=useState<Record<string,unknown>>(initial?.fields||{});
 const [busy,setBusy]=useState(false);
 useEffect(()=>{if(!initial&&!typeID&&types.length)setTypeID(types.find(t=>t.active)?.id||'')},[types,typeID,initial]);
 const type=types.find(t=>t.id===typeID);
 const card=type?.card_fields?.length?type.card_fields:defaultCardFields;
 const sections=[...new Set((type?.fields||[]).map(field=>field.section?.trim()||'Дополнительные реквизиты'))];
 function updateField(key:string,value:unknown){setFields(current=>({...current,[key]:value}))}
 async function submit(e:FormEvent){e.preventDefault();const missingUsers=type?.fields.find(field=>{const selected=fields[field.key];return field.type==='users'&&field.required&&(!Array.isArray(selected)||selected.length===0)});if(missingUsers){notify(`Выберите пользователей в поле «${missingUsers.label}»`);return}setBusy(true);try{const payload={type_id:typeID,title,summary,registration_number:number,assignee_id:assignee,department_id:department,due_at:due?new Date(due).toISOString():null,fields};const result=await request<{id:string}>(initial?`/documents/${initial.id}`:'/documents',{method:initial?'PATCH':'POST',body:JSON.stringify(payload)});saved(initial?.id||result.id)}catch(error){notify((error as Error).message)}finally{setBusy(false)}}
 function standard(field:CardField){const label=field.label+(field.required?' *':'');const common={required:field.required,placeholder:field.placeholder||'',id:`card-${field.key}`};switch(field.key){
  case 'title':return <label className="full" key={field.key}>{label}<input {...common} value={title} maxLength={500} onChange={e=>setTitle(e.target.value)}/></label>;
  case 'summary':return <label className="full" key={field.key}>{label}<textarea {...common} value={summary} rows={3} onChange={e=>setSummary(e.target.value)}/></label>;
  case 'registration_number':return <label key={field.key}>{label}<input {...common} value={number} onChange={e=>setNumber(e.target.value)}/></label>;
  case 'assignee_id':return <label key={field.key}>{label}<select required={field.required} value={assignee} onChange={e=>setAssignee(e.target.value)}><option value="">{field.placeholder||'Не назначен'}</option>{users.filter(u=>u.active||u.id===assignee).map(u=><option key={u.id} value={u.id}>{u.full_name}</option>)}</select></label>;
  case 'department_id':return <label key={field.key}>{label}<select required={field.required} value={department} onChange={e=>setDepartment(e.target.value)}><option value="">{field.placeholder||'Не выбрано'}</option>{departments.map(d=><option key={d.id} value={d.id}>{d.name}</option>)}</select></label>;
  case 'due_at':return <label key={field.key}>{label}<input type="datetime-local" required={field.required} value={due} onChange={e=>setDue(e.target.value)}/></label>;
 }}
 function extra(field:FieldDef){const value=fields[field.key];const label=field.label+(field.required?' *':'');const selected=Array.isArray(value)?value.map(String):[];let control;
  if(field.type==='user')control=<select required={field.required} value={String(value||'')} onChange={e=>updateField(field.key,e.target.value)}><option value="">{field.placeholder||'Выберите пользователя'}</option>{users.filter(u=>u.active||u.id===value).map(u=><option key={u.id} value={u.id}>{u.full_name}</option>)}</select>;
  else if(field.type==='users')control=<div className="card-users" role="group" aria-label={field.label}>{users.filter(u=>u.active||selected.includes(u.id)).map(u=><label className="checkbox-line" key={u.id}><input type="checkbox" checked={selected.includes(u.id)} onChange={e=>updateField(field.key,e.target.checked?[...selected,u.id]:selected.filter(id=>id!==u.id))}/>{u.full_name}</label>)}{users.length===0&&<span className="muted">Нет пользователей</span>}</div>;
  else if(field.type==='boolean')control=<select required={field.required} value={String(value??'')} onChange={e=>updateField(field.key,e.target.value===''?null:e.target.value==='true')}><option value="">Не указано</option><option value="true">Да</option><option value="false">Нет</option></select>;
  else if(field.type==='select')control=<select required={field.required} value={String(value||'')} onChange={e=>updateField(field.key,e.target.value)}><option value="">{field.placeholder||'Выберите значение'}</option>{field.options?.map(option=><option key={option}>{option}</option>)}</select>;
  else if(field.type==='text')control=<textarea required={field.required} placeholder={field.placeholder||''} rows={3} value={String(value||'')} onChange={e=>updateField(field.key,e.target.value)}/>;
  else control=<input type={field.type==='number'?'number':field.type==='date'?'date':'text'} required={field.required} placeholder={field.placeholder||''} value={String(value??'')} onChange={e=>updateField(field.key,field.type==='number'?(e.target.value===''?null:Number(e.target.value)):e.target.value)}/>;
  return <div className={'card-extra '+(field.full_width||field.type==='text'||field.type==='users'?'full':'')} key={field.key}>{field.type==='users'?<><label>{label}</label>{control}</>:<label>{label}{control}</label>}{field.hint&&<small className="card-field-hint">{field.hint}</small>}</div>;
 }
 return <Modal onClose={onClose} width="840px"><h2>{initial?'Редактирование документа':'Создание документа'}</h2><p className="muted">Карточка типа документа</p><form onSubmit={submit}><div className="form-grid"><label>Тип документа<select value={typeID} disabled={Boolean(initial)} onChange={e=>{setTypeID(e.target.value);setFields({})}} required><option value="">Выберите тип</option>{types.filter(t=>t.active||t.id===typeID).map(t=><option value={t.id} key={t.id}>{t.name}</option>)}</select></label>{card.filter(field=>field.visible).map(standard)}</div>{sections.map(section=><div key={section}><h3 className="form-group-title">{section}</h3><div className="form-grid">{type?.fields.filter(field=>(field.section?.trim()||'Дополнительные реквизиты')===section).map(extra)}</div></div>)}<div className="dialog-actions"><button type="button" className="button subtle" onClick={onClose}>Отмена</button><button className="button primary" disabled={busy||!typeID}>{busy?'Сохранение…':'Сохранить'}</button></div></form></Modal>;
}
