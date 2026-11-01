type Batch = {start:()=>void;end:()=>void};
const activeBatches=new Set<Batch>();

// A refresh includes requests started by the parent and all mounted pages.
// Calls already running before the click (for example the AI chat) are excluded.
export function trackRequest<T>(request:Promise<T>):Promise<T>{
 const batches=[...activeBatches];
 batches.forEach(batch=>batch.start());
 return request.finally(()=>batches.forEach(batch=>batch.end()));
}

export function beginRefreshRequests(){
 let pending=0,sealed=false,finished=false;
 let timer:ReturnType<typeof setTimeout>|undefined;
 let resolve!:()=>void;
 const done=new Promise<void>(complete=>{resolve=complete});
 function finish(){if(finished)return;finished=true;clearTimeout(timer);activeBatches.delete(batch);resolve()}
 function check(){
  clearTimeout(timer);
  // Allow state updates from a response to start their dependent requests.
  if(sealed&&pending===0&&!finished)timer=setTimeout(finish,50);
 }
 const batch:Batch={start(){pending++;clearTimeout(timer)},end(){pending--;check()}};
 activeBatches.add(batch);
 return {done,seal(){sealed=true;check()},cancel:finish};
}
