// Apply cached appearance before rendering the application.
try{document.documentElement.dataset.theme=localStorage.getItem('docjet:theme')==='dark'?'dark':'light'}catch{document.documentElement.dataset.theme='light'}
