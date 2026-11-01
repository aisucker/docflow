import type {Theme} from './model';

// Cache only the appearance for the next page load; the account is authoritative.
export function applyTheme(theme:Theme){
 document.documentElement.dataset.theme=theme;
 try{localStorage.setItem('docjet:theme',theme)}catch{/* Appearance still works when storage is unavailable. */}
}
