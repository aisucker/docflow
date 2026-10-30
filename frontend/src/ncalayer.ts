// KNCA Basics module: https://github.com/pkigovkz/sdkinfo/wiki/KNCA-Basics-Module
export function signWithNCALayer(data:string,signal:AbortSignal):Promise<string>{
 return new Promise((resolve,reject)=>{
  const ws=new WebSocket('wss://127.0.0.1:13579/');let done=false;let sent=false;
  let timer=window.setTimeout(()=>finish(new Error('NCALayer не отвечает. Установите и запустите NCALayer с сайта НУЦ РК.')),8000);
  function finish(error?:Error,cms?:string){if(done)return;done=true;window.clearTimeout(timer);signal.removeEventListener('abort',abort);ws.close();if(error)reject(error);else resolve(cms!)}
  function abort(){finish(new Error('Подписание отменено'))}
  signal.addEventListener('abort',abort,{once:true});if(signal.aborted){abort();return}
  ws.onerror=()=>finish(new Error('Не удалось подключиться к NCALayer. Проверьте его запуск и разрешение браузера на доступ к локальной сети.'));
  ws.onclose=()=>{if(!done)finish(new Error('Соединение с NCALayer закрыто'))};
  ws.onmessage=event=>{try{
   const response=JSON.parse(String(event.data));
   if(!sent){if(!response.result?.version){finish(new Error('Некорректный ответ NCALayer'));return}sent=true;window.clearTimeout(timer);timer=window.setTimeout(()=>finish(new Error('Время ожидания подписи истекло. Подготовьте документ заново.')),180000);
    ws.send(JSON.stringify({module:'kz.gov.pki.knca.basics',method:'sign',args:{format:'cms',data,signingParams:{decode:true,encapsulate:false,digested:false,tsaProfile:{}},signerParams:{extKeyUsageOids:['1.3.6.1.5.5.7.3.4']},locale:'ru'}}));return}
   if(response.status!==true){finish(new Error(String(response.message||'NCALayer не смог создать подпись')));return}
   let result=response.body?.result;if(Array.isArray(result)){if(result.length!==1){finish(new Error('Выберите один сертификат для подписи'));return}result=result[0]}
   if(typeof result!=='string'||!result){finish(new Error('Подписание отменено пользователем'));return}finish(undefined,result);
  }catch{finish(new Error('Не удалось обработать ответ NCALayer'))}}
 })
}
