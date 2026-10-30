import {useState} from 'react';
import {readSession} from './model';
export default function PDFExport({id,file=false,name='Документ'}:{id:string;file?:boolean;name?:string}){
 const [busy,setBusy]=useState(false);const [error,setError]=useState('');
 async function download(){setBusy(true);setError('');try{
  const res=await fetch(`/api/v1/${file?'files':'documents'}/${id}/signed-pdf`,{headers:{Authorization:'Bearer '+readSession()?.access_token}});
  if(!res.ok){const data=await res.json().catch(()=>({error:'Не удалось скачать PDF'}));throw Error(data.error)}
  const url=URL.createObjectURL(await res.blob());const a=document.createElement('a');a.href=url;a.download=name.replace(/\.(pdf|docx)$/i,'')+' — DocJet.pdf';a.click();setTimeout(()=>URL.revokeObjectURL(url),10000);
 }catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 return <div className="pdf-export"><button className="button subtle" disabled={busy} onClick={()=>void download()} title="PDF с данными ЭЦП и QR-кодом проверки">{busy?'Создаём PDF…':file?'PDF с QR':'Скачать карточку PDF с QR'}</button>{error&&<p className="form-error" role="alert">{error}</p>}</div>
}
