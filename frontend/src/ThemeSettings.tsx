import {useState} from 'react';
import {request,type Identity,type Theme} from './model';
import {applyTheme} from './theme';

export default function ThemeSettings({user,onUser}:{user:Identity;onUser:(user:Identity)=>void}){
 const [busy,setBusy]=useState(false);
 const [error,setError]=useState('');
 const [message,setMessage]=useState('');
 const [preview,setPreview]=useState<Theme|null>(null);
 const current=preview??user.preferences?.theme??'light';
 async function toggle(){
  if(busy)return;
  const previous=current,next=current==='dark'?'light':'dark';
  setBusy(true);setError('');setMessage('');setPreview(next);applyTheme(next);
  try{const updated=await request<Identity>('/me/theme',{method:'PUT',body:JSON.stringify({theme:next})});onUser(updated);setMessage('Тема сохранена')}
  catch(e){applyTheme(previous);setError((e as Error).message)}
  finally{setPreview(null);setBusy(false)}
 }
 return <div className="theme-settings">
  <div className="theme-setting-row"><div><strong>Тёмная тема</strong><small>Мягкий фон для работы при слабом освещении</small></div>
   <button type="button" role="switch" aria-checked={current==='dark'} aria-label="Тёмная тема" aria-describedby="theme-setting-hint" disabled={busy} className="theme-switch" onClick={()=>void toggle()}><span aria-hidden="true"><svg viewBox="0 0 20 20" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.5">{current==='dark'?<path d="M16 12A7 7 0 0 1 8 3a7 7 0 1 0 8 9Z"/>:<><circle cx="10" cy="10" r="3"/><path d="M10 1v2m0 14v2M1 10h2m14 0h2M3.6 3.6 5 5m10 10 1.4 1.4M3.6 16.4 5 15M15 5l1.4-1.4"/></>}</svg></span></button>
  </div><p id="theme-setting-hint" className="card-field-hint">{busy?'Сохраняем тему…':message||'Тема применяется сразу и сохраняется в вашей учётной записи.'}</p>{error&&<p className="form-error" role="alert">{error}</p>}
 </div>;
}
