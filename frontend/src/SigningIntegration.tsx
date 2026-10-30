import {useEffect,useState} from 'react';
import {request} from './model';
export default function SigningIntegration(){
 const [state,setState]=useState<{configured:boolean;available:boolean}|null>(null);const [error,setError]=useState('');const [busy,setBusy]=useState(false);
 async function check(){setBusy(true);setError('');try{setState(await request('/signing/status'))}catch(e){setError((e as Error).message)}finally{setBusy(false)}}
 useEffect(()=>{void check()},[]);
 return <section className="signing-panel"><h3>NCALayer · ЭЦП НУЦ РК</h3><p>{state?.available?'Сервер проверки ЭЦП подключён.':state?.configured?'Сервер проверки настроен, но пока недоступен.':'Сервер проверки ЭЦП не настроен.'}</p><p>Добавьте в маршрут этап «Подписание ЭЦП» и укажите ИИН каждого подписанта в разделе «Пользователи». Ключ и пароль выбираются в NCALayer на компьютере подписанта.</p><p>Подписывается карточка документа с контрольными суммами актуальных вложений. CMS и подписанный пакет сохраняются в карточке документа.</p>{error&&<p className="form-error">{error}</p>}<button className="button subtle" disabled={busy} onClick={()=>void check()}>{busy?'Проверка…':'Проверить сервер ЭЦП'}</button><a href="https://pki.gov.kz/ru/ncalayer/" target="_blank" rel="noreferrer">NCALayer на сайте НУЦ РК</a></section>
}
