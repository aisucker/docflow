import {useEffect,useId,useRef,useState,type ReactNode} from 'react';
import {createPortal} from 'react-dom';
import {request,type Department,type Position,type User} from './model';

type Props={users:User[];departments?:Department[];value:string[];onChange:(ids:string[])=>void;multiple?:boolean;label?:string;placeholder?:string;required?:boolean;disabled?:boolean};
const normalize=(text:string)=>text.toLocaleLowerCase('ru').replace(/ё/g,'е').trim();

export default function UserPicker({users,departments:provided,value,onChange,multiple=false,label='Пользователь',placeholder='Начните набирать имя или подразделение',required=false,disabled=false}:Props){
 const [departments,setDepartments]=useState<Department[]>(provided||[]);
 const [positions,setPositions]=useState<Position[]>([]);
 const [query,setQuery]=useState('');
 const [focused,setFocused]=useState(false);
 const [open,setOpen]=useState(false);
 const [draft,setDraft]=useState<string[]>([]);
 const [treeQuery,setTreeQuery]=useState('');
 const [expanded,setExpanded]=useState<Set<string>>(new Set());
 const [directoryError,setDirectoryError]=useState('');
 const root=useRef<HTMLDivElement>(null),dialog=useRef<HTMLDivElement>(null),opener=useRef<HTMLElement|null>(null);
 const id=useId();
 useEffect(()=>{if(!focused)return;const timer=window.setTimeout(()=>{if(!root.current?.contains(document.activeElement))setFocused(false)},0);return()=>window.clearTimeout(timer)},[focused]);
 useEffect(()=>{let active=true;Promise.all([provided?Promise.resolve(provided):request<Department[]>('/departments'),request<Position[]>('/positions')]).then(([d,p])=>{if(active){setDepartments(d);setPositions(p)}}).catch(()=>{if(active)setDirectoryError('Не удалось загрузить оргструктуру. Поиск по имени доступен.')});return()=>{active=false}},[provided]);
 useEffect(()=>{if(!open)return;opener.current=document.activeElement as HTMLElement;dialog.current?.querySelector<HTMLInputElement>('input[type="search"]')?.focus();return()=>opener.current?.focus()},[open]);
 useEffect(()=>{const outside=(e:PointerEvent)=>{if(!root.current?.contains(e.target as Node))setFocused(false)};document.addEventListener('pointerdown',outside);return()=>document.removeEventListener('pointerdown',outside)},[]);
 const departmentName=(user:User)=>departments.find(d=>d.id===user.department_id)?.name||'Без подразделения';
 const positionName=(user:User)=>positions.find(p=>p.id===user.position_id)?.name||user.position_name||'';
 const matches=(user:User,q:string)=>normalize([user.full_name,user.email,departmentName(user),positionName(user)].join(' ')).includes(normalize(q));
 const available=users.filter(u=>u.active);
 const results=available.filter(u=>matches(u,query));
 const selected=value.map(key=>users.find(u=>u.id===key)||({id:key,full_name:'Недоступный пользователь',active:false} as User));
 function toggle(ids:string[],key:string){return multiple?(ids.includes(key)?ids.filter(id=>id!==key):[...ids,key]):ids.includes(key)?[]:[key]}
 function choose(key:string){onChange(toggle(value,key));setQuery('');if(!multiple)setFocused(false)}
 function showTree(){setDraft([...value]);setTreeQuery('');setOpen(true);setFocused(false)}
 function row(user:User,ids:string[],onSelect:()=>void){return <button type="button" role="option" aria-selected={ids.includes(user.id)} className={'people-row '+(ids.includes(user.id)?'selected':'')} key={user.id} onClick={onSelect}><span className="people-check" aria-hidden="true">{ids.includes(user.id)?'✓':''}</span><span className="people-avatar" aria-hidden="true">{user.full_name.split(/\s+/).slice(0,2).map(n=>n[0]).join('')}</span><span className="people-description"><strong>{user.full_name}</strong><small>{[positionName(user),departmentName(user)].filter(Boolean).join(' · ')}</small><small>{user.email}</small></span></button>}
 function branch(dep:Department,ancestors:Set<string>):ReactNode{
  if(ancestors.has(dep.id))return null;
  const next=new Set([...ancestors,dep.id]);
  const children=departments.filter(d=>d.parent_id===dep.id);
  const members=available.filter(u=>u.department_id===dep.id);
  const isOpen=expanded.has(dep.id);
  return <li key={dep.id}><button type="button" className="people-department" aria-expanded={isOpen} onClick={()=>setExpanded(previous=>{const n=new Set(previous);if(n.has(dep.id))n.delete(dep.id);else n.add(dep.id);return n})}><span aria-hidden="true">{isOpen?'▾':'▸'}</span><span>{dep.name}</span><small>{members.length}</small></button>{isOpen&&<ul>{children.map(d=>branch(d,next))}{members.map(u=><li key={u.id}>{row(u,draft,()=>setDraft(v=>toggle(v,u.id)))}</li>)}{!children.length&&!members.length&&<li className="people-empty">Нет активных сотрудников</li>}</ul>}</li>;
 }
 const roots=departments.filter(d=>!d.parent_id||!departments.some(parent=>parent.id===d.parent_id));
 const unassigned=available.filter(u=>!departments.some(d=>d.id===u.department_id));
 return <div ref={root} className="user-picker" onBlur={e=>{if(!e.currentTarget.contains(e.relatedTarget as Node))setFocused(false)}} onKeyDown={e=>{if(e.key==='Escape'){setFocused(false);e.stopPropagation()}}}>
  <div className={'people-input '+(disabled?'disabled':'')}>
   <div className="people-tokens">{selected.map(u=><span className={'people-token '+(!u.active?'inactive':'')} key={u.id}>{u.full_name}{!u.active?' · отключён':''}<button type="button" disabled={disabled} aria-label={`Убрать ${u.full_name}`} onClick={()=>onChange(value.filter(id=>id!==u.id))}>×</button></span>)}
    <input id={id} type="search" role="combobox" aria-label={label} aria-expanded={focused} aria-controls={id+'-results'} aria-autocomplete="list" autoComplete="off" placeholder={selected.length?(multiple?'Добавить сотрудника…':'Изменить сотрудника…'):placeholder} value={query} required={required&&!value.length} disabled={disabled} onFocus={()=>setFocused(true)} onChange={e=>{setQuery(e.target.value);setFocused(true)}} onKeyDown={e=>{if(e.key==='ArrowDown'){e.preventDefault();root.current?.querySelector<HTMLButtonElement>('[role="option"]')?.focus()}if(e.key==='Enter'){e.preventDefault();if(results[0])choose(results[0].id)}}}/>
   </div><button type="button" className="people-tree-button" disabled={disabled} aria-label={`Оргструктура: ${label}`} title="Выбрать из оргструктуры" onClick={showTree}><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><path d="M9 3h6v5H9zM3 16h6v5H3zM15 16h6v5h-6zM12 8v4M6 16v-4h12v4"/></svg></button>
  </div>
  {focused&&!disabled&&<div className="people-suggestions" id={id+'-results'} role="listbox" aria-label={'Сотрудники: '+label} aria-multiselectable={multiple} onKeyDown={e=>{if(e.key!=='ArrowDown'&&e.key!=='ArrowUp')return;e.preventDefault();const rows=Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="option"]'));const current=rows.indexOf(document.activeElement as HTMLButtonElement);rows[(current+(e.key==='ArrowDown'?1:-1)+rows.length)%rows.length]?.focus()}}>{results.slice(0,30).map(u=>row(u,value,()=>choose(u.id)))}{!results.length&&<p className="people-empty">Сотрудники не найдены</p>}{results.length>30&&<p className="people-empty">Найдено {results.length}. Уточните запрос или откройте оргструктуру.</p>}</div>}
  {open&&createPortal(<div className="shade people-shade" onMouseDown={e=>{if(e.target===e.currentTarget)setOpen(false)}}><div ref={dialog} className="dialog people-dialog" role="dialog" aria-modal="true" aria-labelledby={id+'-title'} onKeyDown={e=>{if(e.key==='Escape'){e.stopPropagation();setOpen(false)}if(e.key==='Tab'){const items=Array.from(e.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled),input:not(:disabled)'));const first=items[0],last=items[items.length-1];if(e.shiftKey&&document.activeElement===first){e.preventDefault();last?.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first?.focus()}}}}>
   <div className="people-dialog-heading"><h2 id={id+'-title'}>Выбор {multiple?'пользователей':'пользователя'}</h2><button type="button" className="iconbutton" aria-label="Закрыть выбор пользователей" onClick={()=>setOpen(false)}>×</button></div><p className="muted">{label} · {multiple?'можно выбрать несколько сотрудников':'выберите одного сотрудника'}</p>
   <input type="search" aria-label="Поиск в оргструктуре" placeholder="Поиск по имени, должности, email или подразделению" value={treeQuery} onChange={e=>setTreeQuery(e.target.value)}/>
   {directoryError&&<p className="form-error" role="alert">{directoryError}</p>}
   <div className="people-tree-content">{treeQuery.trim()?<div role="listbox" aria-label="Результаты поиска в оргструктуре" aria-multiselectable={multiple}>{available.filter(u=>matches(u,treeQuery)).map(u=>row(u,draft,()=>setDraft(v=>toggle(v,u.id))))}{!available.some(u=>matches(u,treeQuery))&&<p className="people-empty">Ничего не найдено</p>}</div>:<><h3>Оргструктура</h3><ul className="people-tree">{roots.map(d=>branch(d,new Set()))}{unassigned.length>0&&<li><h4 className="people-unassigned">Без подразделения</h4>{unassigned.map(u=>row(u,draft,()=>setDraft(v=>toggle(v,u.id))))}</li>}</ul>{!available.length&&<p className="people-empty">Нет активных сотрудников</p>}</>}</div>
   <div className="people-dialog-footer"><span>Выбрано: {draft.length}</span><button type="button" className="button subtle" onClick={()=>setOpen(false)}>Отмена</button><button type="button" className="button primary" onClick={()=>{onChange(draft);setOpen(false);setQuery('')}}>Добавить{draft.length?' ('+draft.length+')':''}</button></div>
  </div></div>,document.body)}
 </div>;
}
