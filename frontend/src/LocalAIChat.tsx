import {useEffect,useRef,useState,type FormEvent} from 'react';
import {request} from './model';

type Message={role:'user'|'assistant';content:string};
type Status={ready:boolean;model:string};

export default function LocalAIChat(){
 const [open,setOpen]=useState(false);
 const [messages,setMessages]=useState<Message[]>([]);
 const [draft,setDraft]=useState('');
 const [busy,setBusy]=useState(false);
 const [status,setStatus]=useState<'checking'|'ready'|'offline'>('checking');
 const [error,setError]=useState('');
 const list=useRef<HTMLDivElement>(null);

 async function checkStatus(){
  setStatus('checking');
  try{const result=await request<Status>('/ai/status');setStatus(result.ready?'ready':'offline')}
  catch{setStatus('offline')}
 }
 useEffect(()=>{if(open)void checkStatus()},[open]);
 useEffect(()=>{if(list.current)list.current.scrollTop=list.current.scrollHeight},[messages,busy]);

 async function send(e:FormEvent){
  e.preventDefault();
  const question=draft.trim();
  if(!question||busy)return;
  const next:Message[]=[...messages,{role:'user',content:question}];
  setMessages(next);setDraft('');setError('');setBusy(true);
  try{
   const history=next.slice(-10);
   while(history.length>1&&history.reduce((n,m)=>n+m.content.length,0)>7500)history.shift();
   const result=await request<{answer:string}>('/ai/chat',{method:'POST',body:JSON.stringify({messages:history})});
   setMessages(current=>[...current,{role:'assistant',content:result.answer}]);
   setStatus('ready');
  }catch(err){setError((err as Error).message);setStatus('offline')}
  finally{setBusy(false)}
 }

 return <div className="local-ai">
  {!open?<button className="local-ai-launch" onClick={()=>setOpen(true)} aria-label="Открыть локальный ИИ"><span className="local-ai-symbol">✦</span> Спросить ИИ</button>:
  <section className="local-ai-panel" role="dialog" aria-label="Локальный ИИ DocJet">
   <header className="local-ai-header"><div><strong><span aria-hidden="true">✦</span> Локальный ИИ</strong><small>Тестовый помощник DocJet</small></div><button onClick={()=>setOpen(false)} aria-label="Закрыть чат">×</button></header>
   <div className={'local-ai-status '+status}><span className="local-ai-dot"/>{status==='ready'?'Модель готова · работает на этом ПК':status==='checking'?'Проверяю локальную модель…':'Модель недоступна'}{status==='offline'&&<button onClick={()=>void checkStatus()}>Повторить</button>}</div>
   <div className="local-ai-messages" ref={list} aria-live="polite">
    {messages.length===0&&<div className="local-ai-intro"><span aria-hidden="true">✦</span><strong>Задайте простой вопрос</strong><p>Пока помощник отвечает только на текст и не читает документы.</p></div>}
    {messages.map((m,i)=><div className={'local-ai-message '+m.role} key={i}><div>{m.content}</div></div>)}
    {busy&&<div className="local-ai-message assistant"><div>Думаю…</div></div>}
   </div>
   {error&&<div className="local-ai-error" role="alert">{error}</div>}
   <form className="local-ai-compose" onSubmit={e=>void send(e)}><textarea aria-label="Вопрос локальному ИИ" placeholder="Напишите вопрос…" value={draft} maxLength={2000} rows={2} onChange={e=>setDraft(e.target.value)} onKeyDown={e=>{if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();e.currentTarget.form?.requestSubmit()}}}/><button type="submit" disabled={busy||!draft.trim()} aria-label="Отправить вопрос">➤</button></form>
  </section>}
 </div>
}
