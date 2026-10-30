import PDFExport from './PDFExport';
import {useEffect,useState} from 'react';
import {date,readSession,request} from './model';
type Signature={id:string;signer_name:string;certificate_serial:string;sha256:string;verified_at:string};
export default function DocumentSignatures({id,refreshKey}:{id:string;refreshKey:number}){
 const [items,setItems]=useState<Signature[]>([]);const [error,setError]=useState('');
 useEffect(()=>{let current=true;request<Signature[]>(`/documents/${id}/signatures`).then(v=>{if(current)setItems(v)}).catch(e=>{if(current)setError(e.message)});return()=>{current=false}},[id,refreshKey]);
 async function download(sig:Signature,payload=false){try{const res=await fetch(`/api/v1/signatures/${sig.id}/content${payload?'?format=payload':''}`,{headers:{Authorization:'Bearer '+readSession()?.access_token}});if(!res.ok)throw Error('Не удалось скачать подпись');const url=URL.createObjectURL(await res.blob());const a=document.createElement('a');a.href=url;a.download=`${sig.id}.${payload?'json':'p7s'}`;a.click();setTimeout(()=>URL.revokeObjectURL(url),10000)}catch(e){setError((e as Error).message)}}
 if(!items.length&&!error)return null;
 return <section className="document-signatures"><h4>Электронные подписи</h4>{items.length>0&&<PDFExport id={id}/>}<p className="muted">Проверенные подписи пакетов документа на момент подписания.</p>{error&&<p className="form-error">{error}</p>}{items.map(sig=><div className="signature-entry" key={sig.id}><strong>{sig.signer_name}</strong><small>{date(sig.verified_at)} · НУЦ РК</small><small>Сертификат: {sig.certificate_serial}</small><div className="action-strip"><button className="button subtle" onClick={()=>void download(sig)}>Скачать CMS</button><button className="button subtle" onClick={()=>void download(sig,true)}>Подписанный пакет</button></div></div>)}</section>
}
