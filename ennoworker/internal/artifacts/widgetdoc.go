package artifacts

// The widget envelope: the one document every client renders.
//
// Why the Worker builds it (design §3.2, §3.2 P1):
//
// Ennote has more than one client. Local Web reaches the Worker over HTTP through
// `ennogate`; the future native shell reaches it over a single WSS connection
// carrying bounded JSON, where cookies are forbidden (tauri-remote-access §6.4).
// The second client cannot use `<iframe src=workerUrl>` at all, so it must build
// the frame document itself — and any policy that lives in the React client would
// be re-written, differently and probably weaker, on that side.
//
// So the isolation envelope is a Worker fact, not a client fact. `WidgetDocument`
// is the only place the CSP, the receiver script and the sandbox-relevant markup
// exist; a client copies `sandbox="allow-scripts"` from the contract and loads
// this document. Nothing here is model-callable: the receiver exposes no function
// a widget can invoke, only four fixed message types the parent already knows.
//
// The two exceptions to "no client policy" are unavoidable and deliberately small:
// the parent owns the `sandbox` attribute (an iframe attribute, not document
// content) and the parent supplies the theme *values* (only it knows the current
// theme). Neither can loosen the envelope: CSP `sandbox` intersects with the
// attribute, and theme messages only set CSS custom properties.

// WidgetPolicyVersion is stored in artifact metadata so a later reader can tell
// which envelope a published widget was accepted under.
const WidgetPolicyVersion = 1

// widgetCDNOrigins is the single list of hosts a widget may load code from. It is
// defined here and nowhere else: a second copy in the TypeScript client is how
// these two drift apart.
const widgetCDNOrigins = "https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com https://esm.sh"

// WidgetContentSecurityPolicy is served as a response header on the preview
// route. It carries `sandbox allow-scripts` so a client that forgets the iframe
// attribute still gets an opaque origin, and `frame-ancestors 'self'` so only the
// app that serves it can frame it.
//
// img-src does not allow `*`: connect-src 'none' stops fetch/XHR/WebSocket, but
// `new Image().src = 'https://host/?' + data` is still a complete one-way exfil
// channel, and this page runs next to a signed-in local session.
const WidgetContentSecurityPolicy = "sandbox allow-scripts; " +
	"default-src 'none'; " +
	"script-src 'unsafe-inline' " + widgetCDNOrigins + "; " +
	"style-src 'unsafe-inline'; " +
	"img-src data: blob:; " +
	"font-src data:; " +
	"media-src data:; " +
	"connect-src 'none'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"frame-ancestors 'self'"

// widgetMetaContentSecurityPolicy is the same policy minus the two directives a
// meta element cannot carry. It is what protects a document transported as JSON
// and rendered with srcdoc, where there is no response header to read; the
// `sandbox` attribute still has to be set by that client, which is why the
// envelope is documented as a pair (document + attribute) rather than a document.
const widgetMetaContentSecurityPolicy = "default-src 'none'; " +
	"script-src 'unsafe-inline' " + widgetCDNOrigins + "; " +
	"style-src 'unsafe-inline'; " +
	"img-src data: blob:; " +
	"font-src data:; " +
	"media-src data:; " +
	"connect-src 'none'; " +
	"form-action 'none'; " +
	"base-uri 'none'"

// widgetBaseStyle is the pre-theme state. `display:flow-root` on the wrapper is not
// cosmetic: without it the first child's top margin collapses out of the measured
// box and the frame is consistently shorter than its content. The parent pushes resolved token values
// as soon as the frame reports ready, so these are only what the first frame looks
// like: follow the OS preference rather than assume light.
const widgetBaseStyle = `:root{color-scheme:light dark;--color-background-primary:#ffffff;` +
	`--color-background-secondary:#f7f7f5;--color-background-tertiary:#efefec;` +
	`--color-text-primary:#18181b;--color-text-secondary:#52525b;--color-text-tertiary:#71717a;` +
	`--color-border-primary:rgba(24,24,27,.18);--color-border-secondary:rgba(24,24,27,.09);` +
	`--color-border-tertiary:rgba(24,24,27,.09);--color-accent:#2563eb;` +
	`--font-sans:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;` +
	`--font-mono:ui-monospace,SFMono-Regular,Consolas,monospace;` +
	`--color-chart-1:#94a3b8;--color-chart-2:#818cf8;--color-chart-3:#2dd4bf;` +
	`--color-chart-4:#fbbf24;--color-chart-5:#a78bfa;--color-chart-6:#fb7185;` +
	`--color-background-success:#ecfdf3;--color-background-warning:#fffbeb;` +
	`--color-background-danger:#fef2f2;--color-background-info:#f0f9ff;` +
	`--color-text-success:#15803d;--color-text-warning:#a16207;` +
	`--color-text-danger:#b91c1c;--color-text-info:#0369a1;` +
	`--radius-sm:8px;--radius-md:12px;--radius-lg:16px}` +
	`@media (prefers-color-scheme:dark){:root{--color-background-primary:#18181b;` +
	`--color-background-secondary:#202023;--color-background-tertiary:#2a2a2e;` +
	`--color-text-primary:#f4f4f5;--color-text-secondary:#a1a1aa;--color-text-tertiary:#71717a;` +
	`--color-border-primary:rgba(244,244,245,.24);--color-border-secondary:rgba(244,244,245,.12);` +
	`--color-border-tertiary:rgba(244,244,245,.12)}}` +
	`html,body{margin:0;padding:0;background:var(--color-background-primary);` +
	`color:var(--color-text-primary);font:14px/1.5 var(--font-sans)}` +
	`#__ennote_widget{box-sizing:border-box;min-width:0;display:flow-root}`

// widgetReceiverScript runs before any model markup, so an early script error is
// reported rather than lost. It exposes exactly four messages and no callable API:
//
//	ennote:widget:ready     fired once, after the widget root exists
//	ennote:widget:height    fired when the content height changes
//	ennote:widget:error     script error, blocked subresource, or CSP violation
//	ennote:widget:theme     accepted from the parent: CSS custom properties only
//
// Deliberately absent compared to the reference implementation: a "send a chat
// message" hook, cross-widget filter pub/sub, and parent-side fetch proxies. Each
// would have to be reimplemented by every client, and the phone client never will.
const widgetReceiverScript = `(function(){
var MAX_REPORT=20000,ROOT_ID='__ennote_widget';
var last=-1,timer=null,tries=0;
function send(type,extra){
  var msg={type:type};
  if(extra){for(var k in extra){msg[k]=extra[k];}}
  try{parent.postMessage(msg,'*');}catch(e){}
}
function root(){return document.getElementById(ROOT_ID);}
function measure(){
  var el=root();
  var h=0;
  if(el){
    var rect=el.getBoundingClientRect();
    h=Math.ceil(Math.max(rect.height,el.scrollHeight));
  }
  if(h<=0&&document.body){
    // The wrapper is empty, usually because the published markup closed it early.
    // Whatever is left in the body is still content the reader should see; measuring
    // only the wrapper would report nothing and leave them with a 40px slit.
    h=Math.ceil(document.body.scrollHeight);
  }
  if(!isFinite(h)||h<=0||h===last){return;}
  if(h>MAX_REPORT){h=MAX_REPORT;}
  last=h;
  send('ennote:widget:height',{height:h});
}
function schedule(){
  if(timer){clearTimeout(timer);}
  timer=setTimeout(measure,60);
}
window.addEventListener('error',function(event){
  var kind='script',detail='';
  var target=event&&event.target;
  if(target&&target!==window&&target.tagName==='SCRIPT'){kind='csp';}
  if(target&&target!==window&&target.src){detail=String(target.src);}
  else if(event&&event.message){detail=String(event.message);}
  send('ennote:widget:error',{kind:kind,detail:detail.slice(0,200)});
},true);
document.addEventListener('securitypolicyviolation',function(event){
  if(!event){return;}
  var detail=String(event.violatedDirective||'')+' '+String(event.blockedURI||'');
  send('ennote:widget:error',{kind:'csp',detail:detail.slice(0,200)});
});
window.addEventListener('message',function(event){
  if(event.source!==parent){return;}
  var data=event&&event.data;
  if(!data||data.type!=='ennote:widget:theme'||!data.vars){return;}
  var style=document.documentElement.style;
  for(var name in data.vars){
    // Only custom properties: a theme message must not be able to set, say,
    // background-image on the document element.
    if(name.indexOf('--')!==0){continue;}
    if(typeof data.vars[name]==='string'){style.setProperty(name,data.vars[name]);}
  }
  schedule();
});
function start(){
  var el=root();
  if(!el){return false;}
  if(window.ResizeObserver){new ResizeObserver(schedule).observe(el);}
  window.addEventListener('resize',schedule);
  measure();
  send('ennote:widget:ready');
  schedule();
  return true;
}
function boot(){
  if(start()){return;}
  tries=tries+1;
  if(tries<100){setTimeout(boot,30);}
}
if(document.readyState==='loading'){document.addEventListener('DOMContentLoaded',boot);}
else{boot();}
})();`

// WidgetDocument wraps published HTML in the receiver document. The model's bytes
// are inserted verbatim inside `#__ennote_widget`; nothing between here and the
// client rewrites them.
func WidgetDocument(html string) string {
	return "<!doctype html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n" +
		"<meta http-equiv=\"Content-Security-Policy\" content=\"" + widgetMetaContentSecurityPolicy + "\">\n" +
		"<style>" + widgetBaseStyle + "</style>\n" +
		"<script>" + widgetReceiverScript + "</script>\n" +
		"</head>\n<body>\n<div id=\"__ennote_widget\">\n" + html + "\n</div>\n</body>\n</html>\n"
}
