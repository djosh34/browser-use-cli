import https from 'node:https';
import fs from 'node:fs';
import path from 'node:path';
import {spawn} from 'node:child_process';
const dir=path.dirname(new URL(import.meta.url).pathname);
const chrome='/home/joshazimullah.linux/work_mounts/browser-use-cli/prototypes/browser-cli/.runtime/chrome-linux-arm64/chrome';
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const fixture=(port,child)=>`<!doctype html><meta charset=utf-8><title>Owned nonpointer fixture</title>
<style>#overlay{position:fixed;inset:0;background:#8888;z-index:99;pointer-events:auto} #off{position:absolute;top:3000px} #pe{pointer-events:none} #transform{transform:rotate(20deg) scale(.7)}</style>
<input id=prior value=prior><button id=button>Button</button><button id=pe>Pointer none</button><button id=transform>Transformed</button><button id=off>Offscreen</button>
<button id=disabled disabled>Native disabled</button><fieldset disabled><button id=fieldset>Fieldset disabled</button></fieldset>
<button id=aria aria-disabled=true>ARIA disabled</button><div aria-disabled=true><button id=ariaChild>Inherited ARIA disabled</button></div>
<div inert><button id=inert>Inert</button></div><button id=hidden style="display:none">Hidden</button>
<input id=text value=old><input id=readonly readonly value=old><input id=ariaReadonly aria-readonly=true value=old>
<textarea id=textarea>old</textarea><div id=editable contenteditable role=textbox>old</div><input id=number type=number value=2>
<input id=check type=checkbox><input id=radio type=radio name=r><label id=label for=check>Checkbox label</label>
<a id=link href="#arrived">Link</a><a id=newtab href="/done?newtab" target=_blank>New tab</a>
<form action=/done><input name=q value=owned><button id=submit type=submit>Submit</button></form>
<details id=details><summary id=summary>Details</summary>Body</details>
<select id=select><option value=a>A</option><option value=b>B</option><option value=c disabled>C</option></select>
<select id=list size=3><option value=a>A</option><option id=option value=b>B</option></select>
<input id=range type=range min=0 max=10 value=3><input id=date type=date value=2026-01-01><input id=color type=color value=#123456><input id=file type=file>
<div id=custom role=button tabindex=0>Custom role, click handler</div><div id=nonfocus role=button>Nonfocusable click handler</div><button id=afterNonfocus>After nonfocus</button>
<button id=trusted>Trust-gated</button><button id=pointer>Pointerdown-only</button><button id=redirect>Click redirects focus</button><button id=focusRedirect>Focus redirects</button><button id=popup>Popup</button>
<div id=host></div><div id=closedHost></div>
${child?'':`<iframe id=same src="https://localhost:${port}/?child=1"></iframe><iframe id=cross src="https://127.0.0.1:${port}/?child=1"></iframe>`}
<div id=overlay></div>
<script>
window.effects={}; window.targets={};
for(const mode of ['open','closed']) {const host=document.getElementById(mode==='open'?'host':'closedHost');const root=host.attachShadow({mode});root.innerHTML='<button id='+mode+'Button>'+mode+' shadow button</button>';targets[mode]=root.firstChild;}
for(const id of ['button','custom','nonfocus','aria','ariaChild','pe','off','transform','disabled','fieldset','inert','hidden']) document.getElementById(id).addEventListener('click',()=>effects[id]=(effects[id]||0)+1);
document.getElementById('trusted').onclick=e=>{if(e.isTrusted)effects.trusted=true};
document.getElementById('pointer').onpointerdown=()=>effects.pointer=true;
document.getElementById('redirect').onclick=()=>document.getElementById('text').focus();
document.getElementById('focusRedirect').onfocus=()=>document.getElementById('text').focus();
document.getElementById('popup').onclick=()=>{let w=window.open('/done?popup','_blank');effects.popup=!!w;};
</script>`;
const server=https.createServer({key:fs.readFileSync(dir+'/key.pem'),cert:fs.readFileSync(dir+'/cert.pem')},(req,res)=>{res.setHeader('content-type','text/html');res.end(req.url.startsWith('/done')?'<!doctype html><title>Owned destination</title>Done':fixture(server.address().port,req.url.includes('child=1')))});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const port=server.address().port; const origin=`https://localhost:${port}`;
const profile=dir+'/profile-'+Date.now();
const args=['--headless=new','--window-size=1200,900','--remote-debugging-address=127.0.0.1','--remote-debugging-port=0',`--user-data-dir=${profile}`,'--no-first-run','--no-default-browser-check','--no-proxy-server','--site-per-process','about:blank'];
const stderr=fs.openSync(dir+'/chrome.log','w');
const child=spawn(chrome,args,{env:{...process.env,HOME:dir+'/home'},stdio:['ignore','ignore',stderr]});
let ws; let rpc; const pending=new Map();let seq=0;let events=[];
try {
 for(let i=0;i<100 && !fs.existsSync(profile+'/DevToolsActivePort');i++)await sleep(100);
 const [debugPort,endpoint]=fs.readFileSync(profile+'/DevToolsActivePort','utf8').trim().split('\n');
 ws=new WebSocket(`ws://127.0.0.1:${debugPort}${endpoint}`);await new Promise((r,j)=>{ws.onopen=r;ws.onerror=j});
 ws.onmessage=({data})=>{const m=JSON.parse(data);if(m.id){const p=pending.get(m.id);if(!p)return;pending.delete(m.id);m.error?p.reject(new Error(JSON.stringify(m.error))):p.resolve(m.result)}else events.push(m)};
 rpc=(method,params={},sessionId)=>new Promise((resolve,reject)=>{const id=++seq;pending.set(id,{resolve,reject});ws.send(JSON.stringify({id,method,params,...(sessionId?{sessionId}:{})}));setTimeout(()=>{if(pending.delete(id))reject(new Error('timeout '+method))},10000).unref()});
 const version=await rpc('Browser.getVersion');
 const protocol=await (await fetch(`http://127.0.0.1:${debugPort}/json/protocol`)).json();
 fs.writeFileSync(dir+'/protocol.json',JSON.stringify(protocol,null,2));
 const logScript=`window.logs=[];for(const type of ['focus','focusin','blur','focusout','pointerdown','mousedown','pointerup','mouseup','click','keydown','keypress','keyup','beforeinput','input','change','submit','toggle'])document.addEventListener(type,e=>{const p=e.composedPath()[0];const row={type,id:e.target.id||e.target.tagName,inner:p.id||p.tagName,trusted:e.isTrusted,key:e.key,detail:e.detail,pointerType:e.pointerType,active:document.activeElement?.id,ua:{active:navigator.userActivation.isActive,ever:navigator.userActivation.hasBeenActive},value:e.target.value,checked:e.target.checked};logs.push(row);__report(JSON.stringify(row));},true);`;
 const evaluate=async(session,expression,contextId)=>{const r=await rpc('Runtime.evaluate',{expression,returnByValue:true,...(contextId?{contextId}:{})},session);if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value};
 const {targetId:securityTarget}=await rpc('Target.createTarget',{url:'about:blank'});
 const {sessionId:securitySession}=await rpc('Target.attachToTarget',{targetId:securityTarget,flatten:true});
 await rpc('Page.enable',{},securitySession);await rpc('Security.enable',{},securitySession);
 await rpc('Page.navigate',{url:'chrome://sandbox'},securitySession);await sleep(200);
 const sandbox=await evaluate(securitySession,'document.body.innerText');
 await rpc('Page.navigate',{url:origin},securitySession);await sleep(500);
 const security=events.filter(e=>e.sessionId===securitySession && e.method==='Security.visibleSecurityStateChanged').at(-1)?.params;
 fs.writeFileSync(dir+'/security.json',JSON.stringify({sandbox,security},null,2));
 await rpc('Target.closeTarget',{targetId:securityTarget});
 const cases=[];
 const add=(id,mode,extra={})=>cases.push({id,mode,...extra});
 for(const id of ['button','pe','transform','off','disabled','fieldset','aria','ariaChild','inert','hidden','readonly','ariaReadonly','check','radio','label','summary','select','list','option','range','date','color','file','custom','nonfocus','trusted','pointer','redirect','focusRedirect','link','newtab','submit','popup']) {add(id,'bare');add(id,'focusGesture');}
 add('button','focusNoGesture');add('button','bareGesture');add('popup','bareGesture');add('file','focusNoGesture');
 for(const id of ['button','check','radio','link','custom','nonfocus','trusted','pointer','summary','submit','select']){add(id,'Enter');add(id,'Space')}
 for(const id of ['select','list','range','number','date'])add(id,'ArrowDown');
 for(const id of ['text','readonly','ariaReadonly','textarea','editable','number'])add(id,'insertText');
 for(const frame of ['same','cross']){add('button','focusGesture',{frame});add('button','Enter',{frame})}
 for(const shadow of ['open','closed']){add('button','focusGesture',{shadow});add('button','Enter',{shadow})}
 add('nonfocus','focusGesture',{thenTab:true});add('nonfocus','bare',{thenTab:true});
 add('select','setSelect');
 const results=[];
 for(const c of cases){
  const {browserContextId}=await rpc('Target.createBrowserContext');
  const {targetId}=await rpc('Target.createTarget',{url:'about:blank',browserContextId});
  const {sessionId:rootSession}=await rpc('Target.attachToTarget',{targetId,flatten:true});
  events=[];let session=rootSession;
  const setup=async(s)=>{await rpc('Runtime.enable',{},s);await rpc('Page.enable',{},s);await rpc('Runtime.addBinding',{name:'__report'},s);await rpc('Page.addScriptToEvaluateOnNewDocument',{source:logScript},s);await rpc('Page.setInterceptFileChooserDialog',{enabled:true},s)};
  await setup(session);await rpc('Page.navigate',{url:origin},session);
  for(let n=0;n<100;n++){if(await evaluate(session,'document.readyState==="complete" && !!window.effects').catch(()=>false))break;await sleep(50)}
  let frame=(await rpc('Page.getFrameTree',{},session)).frameTree.frame;
  let mainContext;
  if(c.frame==='same') {frame=(await rpc('Page.getFrameTree',{},session)).frameTree.childFrames.find(x=>x.frame.url.startsWith(origin)).frame;mainContext=events.find(e=>e.sessionId===session && e.method==='Runtime.executionContextCreated' && e.params.context.auxData?.isDefault && e.params.context.auxData.frameId===frame.id)?.params.context.id;}
  if(c.frame==='cross') {
   const t=(await rpc('Target.getTargets')).targetInfos.find(t=>t.type==='iframe'&&t.browserContextId===browserContextId);
   if(!t)throw new Error('expected OOPIF');
   session=(await rpc('Target.attachToTarget',{targetId:t.targetId,flatten:true})).sessionId;
   await setup(session);await evaluate(session,logScript);frame=(await rpc('Page.getFrameTree',{},session)).frameTree.frame;
  }
  const {executionContextId}=await rpc('Page.createIsolatedWorld',{frameId:frame.id,worldName:'nonpointer-research'},session);
  const expression=c.shadow?`window.targets.${c.shadow}`:`document.getElementById(${JSON.stringify(c.id)})`;
  const original=(await rpc('Runtime.evaluate',{expression,...(mainContext?{contextId:mainContext}:{})},session)).result;
  const {node}=await rpc('DOM.describeNode',{objectId:original.objectId},session);
  const {object}=await rpc('DOM.resolveNode',{backendNodeId:node.backendNodeId,executionContextId},session);
  const ax=(await rpc('Accessibility.getPartialAXTree',{backendNodeId:node.backendNodeId,fetchRelatives:false},session)).nodes.map(n=>({role:n.role?.value,name:n.name?.value,ignored:n.ignored,ignoredReasons:n.ignoredReasons,properties:n.properties}));
  await evaluate(session,'document.getElementById("prior").focus();window.logs=[]',mainContext);
  const before=await evaluate(session,'({ua:{active:navigator.userActivation.isActive,ever:navigator.userActivation.hasBeenActive},active:document.activeElement.id,scrollY,secure:isSecureContext})',mainContext);
  const start=events.length;
  const call=async(fn,userGesture=false)=>{const r=await rpc('Runtime.callFunctionOn',{objectId:object.objectId,functionDeclaration:fn,userGesture,returnByValue:true},session);return r.exceptionDetails?{exception:r.exceptionDetails}:r.result.value};
  const key=async(name)=>{const keys={Enter:['Enter','Enter',13,'\r'],Space:[' ','Space',32,' '],ArrowDown:['ArrowDown','ArrowDown',40,''],Tab:['Tab','Tab',9,''],Escape:['Escape','Escape',27,'']};const [key,code,windowsVirtualKeyCode,text]=keys[name];for(const type of ['keyDown','keyUp'])await rpc('Input.dispatchKeyEvent',{type,key,code,windowsVirtualKeyCode,text:type==='keyDown'?text:''},rootSession)};
  let returned;
  try {
   if(['bare','bareGesture','focusNoGesture','focusGesture'].includes(c.mode)) returned=await call(`function(){${c.mode.startsWith('focus')?'this.focus();':''}this.click();return {active:document.activeElement.id,ua:{active:navigator.userActivation.isActive,ever:navigator.userActivation.hasBeenActive}}}`,c.mode.endsWith('Gesture')&&!c.mode.includes('No'));
   else if(c.mode==='insertText') {await call('function(){this.focus();if(this.select)this.select();else{const r=document.createRange();r.selectNodeContents(this);const s=getSelection();s.removeAllRanges();s.addRange(r)}}');await rpc('Input.insertText',{text:'7'},rootSession);}
   else if(c.mode==='setSelect') returned=await call('function(){this.focus();this.value="b";this.dispatchEvent(new Event("input",{bubbles:true}));this.dispatchEvent(new Event("change",{bubbles:true}));}');
   else {await call('function(){this.focus()}');await key(c.mode)}
   if(c.thenTab)await key('Tab');
  } catch(e){returned={error:e.message}}
  await sleep(120);
  const observed=events.slice(start);
  const after=await evaluate(session,`({active:document.activeElement?.id,shadowActive:document.activeElement?.shadowRoot?.activeElement?.id,ua:{active:navigator.userActivation.isActive,ever:navigator.userActivation.hasBeenActive},scrollY,effects:window.effects,href:location.href,values:Object.fromEntries(['text','readonly','ariaReadonly','textarea','editable','number','check','radio','select','list','range','date','color','details'].map(id=>{let e=document.getElementById(id);return [id,e?{value:e.value,text:id==='editable'?e.textContent:undefined,checked:e.checked,open:e.open,pickerOpen:e.matches(':open')}:null]}))})`,mainContext).catch(e=>({error:e.message}));
  const targets=(await rpc('Target.getTargets')).targetInfos.filter(t=>t.browserContextId===browserContextId).map(t=>({type:t.type,url:t.url}));
  const result={...c,before,ax,returned,after,events:observed.filter(e=>e.method==='Runtime.bindingCalled').map(e=>JSON.parse(e.params.payload)),fileChooser:observed.filter(e=>e.method==='Page.fileChooserOpened').map(e=>e.params),targets};
  results.push(result);fs.writeFileSync(dir+'/results.json',JSON.stringify({version,args,origin,results},null,2));
  console.log(JSON.stringify({id:c.id,mode:c.mode,frame:c.frame,shadow:c.shadow,thenTab:c.thenTab,ax:ax.map(n=>({role:n.role,ignored:n.ignored,properties:n.properties?.filter(p=>['disabled','readonly','focusable'].includes(p.name))})),before,returned,after,events:result.events.map(e=>`${e.type}:${e.inner}:${e.trusted?'T':'F'}${e.key?':'+e.key:''}`),fileChooser:result.fileChooser.length,targets}));
  await rpc('Target.disposeBrowserContext',{browserContextId});
 }
 console.log('DONE '+results.length);
 await rpc('Browser.close').catch(()=>{});
}finally{if(ws)ws.close();child.kill('SIGTERM');await new Promise(r=>{if(child.exitCode!==null)r();else {child.once('exit',r);setTimeout(()=>{child.kill('SIGKILL');r()},3000).unref()}});server.close();}
