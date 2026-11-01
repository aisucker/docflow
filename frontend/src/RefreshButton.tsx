import {useEffect,useRef,useState} from 'react';
import {Icon} from './App';

export default function RefreshButton({onRefresh}:{onRefresh:()=>Promise<void>}){
 const [spinning,setSpinning]=useState(false);
 const loading=useRef(false),running=useRef(false),mounted=useRef(true);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false}},[]);
 function start(){
  if(running.current)return;
  running.current=true;loading.current=true;setSpinning(true);
  // Data loaders display their own errors. An error still finishes a half-turn.
  void Promise.resolve().then(onRefresh).catch(()=>{}).finally(()=>{loading.current=false});
 }
 function finishHalfTurn(){
  if(loading.current)return;
  running.current=false;
  if(mounted.current)setSpinning(false);
 }
 return <button type="button" className={'button subtle refresh-button'+(spinning?' is-refreshing':'')} aria-label="Обновить" aria-busy={spinning} disabled={spinning} title={spinning?'Обновление данных…':'Обновить данные'} onClick={start}><span className="refresh-spinner" onAnimationIteration={finishHalfTurn}><Icon name="refresh" size={17}/></span> Обновить</button>;
}
