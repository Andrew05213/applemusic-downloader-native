'use strict';
const fs=require('node:fs');const path=require('node:path');const crypto=require('node:crypto');
const STAGES=new Set(['restore_session','login_start','stop_lite','device_login','device_code','gsa_provider','gsa_prepare','adi_helper_start','adi_initialize','adi_status','adi_lookup','adi_provision_start','adi_provision_sdk_start','adi_provision_finish','adi_provision_sdk_finish','adi_state_save','adi_otp','gsa_authenticate','phone_list','sms_wait_phone','sms_send','sms_wait_code','sms_verify','gsa_resume','store_credentials','lite_bind','music_service_start','music_tokens','music_storefront','login_complete','login_cancel','desktop_login_submit','desktop_sms_send','desktop_sms_code','desktop_service','desktop_recheck','desktop_code','diagnostics']);
const CODES=new Set(['native_auth_required','native_session_unverified','native_login_failed','native_auth_busy','native_auth_input_invalid','native_auth_cancelled','native_adi_provider_unavailable','native_adi_provisioning_failed','native_adi_state_invalid','native_adi_cancelled','native_adi_not_provisioned','native_network_dns_failed','native_network_timeout','native_tls_certificate_invalid','native_network_connection_failed','native_adi_http_failed','native_adi_response_invalid','native_agent_unavailable','native_agent_protocol_invalid','native_service_unavailable','native_service_timeout','native_service_closed','native_api_unavailable','native_api_failed','native_api_response_invalid','native_login_rejected','native_code_rejected','native_sms_rejected','native_operation_failed','native_runtime_unavailable','native_diagnostics_read_failed','native_diagnostics_write_failed','native_gsa_provider_unavailable','native_gsa_protocol_invalid','native_gsa_unavailable','native_gsa_authentication_failed','native_gsa_cancelled','native_gsa_input_invalid','native_gsa_code_rejected','native_gsa_challenge_expired','native_gsa_sms_cooldown','native_gsa_sms_unavailable','native_gsa_busy','native_music_binding_failed']);
const OUTCOMES=new Set(['begin','ok','waiting','failed','cancelled']);
const runtimeStages = new Set('runtime_port runtime_bundle runtime_state runtime_boot desktop_api'.split(' '));
const runtimeCodes = new Set('native_server_failed native_arguments_invalid native_parent_required native_parent_invalid native_parent_unavailable native_bundle_missing native_bundle_invalid native_directory_unavailable native_job_store_unavailable native_queue_unavailable native_queue_in_use native_api_unavailable native_bundle_identity_mismatch native_state_invalid native_state_in_use native_operation_cancelled native_runtime_prepare_timeout native_guest_not_ready native_guest_exited native_process_start_failed native_port_in_use lite_invalid_port native_service_protocol_invalid native_service_version_mismatch native_api_connection_failed native_api_timeout native_service_unavailable native_service_timeout native_service_closed native_runtime_unavailable native_api_failed native_api_response_invalid'.split(' '));
for (const stage of runtimeStages) STAGES.add(stage);
for (const code of runtimeCodes) CODES.add(code);
const exitCodeValid = code => Number.isInteger(code) && code >= 0 && code <= 0xffffffff;
const fallbackCodes = new Set(['native_process_start_failed','native_guest_exited','native_guest_not_ready']);
const guestBootEvents = new Set('init_start modules_ready network_ready state_mounted service_start dev_mount_failed proc_mount_failed sys_mount_failed run_mount_failed module_load_failed loopback_failed network_device_missing network_address_failed network_route_failed state_device_missing data_mount_failed ca_bundle_missing control_capability_missing control_capability_copy_failed control_capability_permissions_failed service_exited'.split(' '));
function safeRuntime(input) {
 if (!['stopped','starting','ready','stopping','failed','unavailable'].includes(input?.phase)) return undefined;
 const {phase, stage, code, authState, processExitCode, guestBootEvent, accelerator, acceleratorFallback} = input;
 return {phase, ...(runtimeStages.has(stage)||stage==='desktop_service' ? {stage} : {}),
  ...((phase==='failed'||phase==='unavailable')&&runtimeCodes.has(code) ? {code} : {}),
  ...(['checking','needs_login','authenticating','needs_2fa','needs_interaction','authenticated','unverified'].includes(authState) ? {authState} : {}),
  ...(['whpx','tcg'].includes(accelerator) ? {accelerator} : {}),
  ...(fallbackCodes.has(acceleratorFallback) ? {acceleratorFallback} : {}),
  ...(phase==='failed'&&stage==='runtime_boot'&&guestBootEvents.has(guestBootEvent) ? {guestBootEvent} : {}),
  ...(phase==='failed'&&((stage==='runtime_boot'&&code==='native_guest_exited')||stage==='desktop_service')&&exitCodeValid(processExitCode) ? {processExitCode} : {})};
}
STAGES.add('music_dns_fallback');
CODES.add('native_music_dns_fallback_failed');
for(const code of 'native_music_developer_rejected native_music_user_rejected native_music_rate_limited native_music_upstream_failed native_music_http_failed native_music_response_invalid'.split(' '))CODES.add(code);
const APPLE_CODES=new Set(['40100','40101','40300','40301','40302','40303','42900','50000','50001','50300']);
// Keep this fixed vocabulary aligned with nativeauth.ADIResponseCodes and the
// sandboxed preload; no raw parser errors or response fields are accepted.
for(const code of 'native_adi_body_read_failed native_adi_body_too_large native_adi_body_empty native_adi_plist_invalid_xml native_adi_plist_envelope_invalid native_adi_plist_duplicate_key native_adi_plist_type_unsupported native_adi_plist_real_unsupported native_adi_plist_date_unsupported native_adi_plist_integer_invalid native_adi_plist_integer_range native_adi_plist_data_invalid native_adi_plist_real_invalid native_adi_plist_depth_limit native_adi_plist_node_limit native_adi_plist_binary_unsupported native_adi_status_invalid native_adi_service_rejected native_adi_spim_invalid native_adi_cpim_invalid native_adi_ptm_invalid native_adi_trust_key_invalid native_adi_routing_invalid native_adi_endpoints_invalid'.split(' '))CODES.add(code);
for(const code of 'native_login_process_start_failed native_login_process_exited native_login_process_signalled native_login_protocol_failed native_login_process_timeout native_login_store_no_response native_login_store_response_failed native_login_store_bridge_failed native_login_input_invalid native_login_interaction_timeout native_login_interaction_unavailable native_login_interaction_unsupported native_login_internal_error native_login_token_cache_failed'.split(' '))CODES.add(code);
// Sandboxed preload mirrors this allowlist. Keep both boundaries in sync.
const tokenFileStages=new Set(['storefront_file','developer_token_file','music_token_file']);
function safeTokenDiagnostic(e){
 const t=e.tokenDiagnostic;
 if(!t||typeof t!=='object'||e.stage!=='device_login'||e.loginEvent!==undefined||e.httpStatus!==undefined||
    e.appleErrorCode!==undefined||e.storeResponseType!==undefined||e.storeErrorCode!==undefined||
    e.storeExternalErrorCode!==undefined||e.storeProtocolErrorCode!==undefined||e.storeAuthStatus!==undefined)return undefined;
 const {stage,reason,attempt,usedCache,httpStatus}=t;
 if(!Number.isInteger(attempt)||attempt<0||attempt>5||usedCache!==(reason==='cache_fallback_used')||
    (httpStatus!==undefined&&(!Number.isInteger(httpStatus)||httpStatus<100||httpStatus>599)))return undefined;
 const reading=['cache_read_failed','cache_invalid'].includes(reason);
 const writing=['cache_write_failed','cache_replace_failed'].includes(reason);
 const noHTTP=httpStatus===undefined;
 let valid=false;
 switch(stage){
 case 'storefront':valid=attempt>0&&reason==='storefront_fetch_failed'&&noHTTP;break;
 case 'developer_token':valid=attempt>0&&reason==='developer_token_fetch_failed';break;
 case 'music_user_token':valid=attempt>0&&reason==='music_user_token_fetch_failed';break;
 case 'music_token_fallback':valid=attempt>0&&(usedCache||reading&&noHTTP);break;
 case 'token_cache_json':valid=attempt===0&&noHTTP&&writing;break;
 case 'refresh':valid=noHTTP&&(attempt>=2&&reason==='retry_succeeded'||attempt===5&&reason==='retry_exhausted');break;
 default:valid=tokenFileStages.has(stage)&&noHTTP&&(reading&&(attempt===0||stage==='developer_token_file')||writing&&attempt>0);
 }
 if(!valid)return undefined;
 if(e.outcome==='waiting'){
  if(e.code!==undefined||e.exitCode!==undefined||e.exitSignal!==undefined)return undefined;
 }else if(e.outcome!=='failed'||e.code!=='native_login_token_cache_failed'||usedCache||stage==='refresh')return undefined;
 return {stage,reason,attempt,usedCache,...(noHTTP?{}:{httpStatus})};
}
function safeEvent(e){
 if(!e||typeof e.traceId!=='string'||!/^[a-f0-9]{32}$/.test(e.traceId)||!Number.isSafeInteger(e.seq)||e.seq<1||!Number.isSafeInteger(e.attempt)||e.attempt<1||!Number.isSafeInteger(e.at)||e.at<1||e.at>4102444800000||!STAGES.has(e.stage)||!OUTCOMES.has(e.outcome)||(e.code!==undefined&&!CODES.has(e.code))||(e.outcome==='failed'&&!CODES.has(e.code))||(e.httpStatus!==undefined&&(!Number.isInteger(e.httpStatus)||e.httpStatus<100||e.httpStatus>599))||(e.appleErrorCode!==undefined&&!APPLE_CODES.has(e.appleErrorCode)))return undefined;
 const LOGIN_EVENTS=new Set('dialog_sign_in dialog_other dialog_existing_account_selected dialog_unhandled credentials_requested verification_requested store_response_received credentials_submitted verification_submitted retry_selected interaction_answer_rejected interaction_unsupported_field interaction_unsupported_action interaction_unsupported_metadata interaction_unsupported_limits interaction_unsupported_credentials'.split(' '));
 const extra={};if(e.loginEvent!==undefined){if(e.stage!=='device_login'||e.outcome!=='waiting'||e.code!==undefined||!LOGIN_EVENTS.has(e.loginEvent))return undefined;extra.loginEvent=e.loginEvent;}
 for(const [key,min,max] of [['exitCode',0,255],['exitSignal',1,64],['storeResponseType',0,255],['storeErrorCode',-2147483648,2147483647],['storeExternalErrorCode',-9007199254740991,9007199254740991],['storeProtocolErrorCode',-9007199254740991,9007199254740991],['storeAuthStatus',-2147483648,2147483647]])if(e[key]!==undefined){if(e.stage!=='device_login'||!Number.isSafeInteger(e[key])||e[key]<min||e[key]>max)return undefined;if(key.startsWith('store')){if(!(e.outcome==='failed'&&e.code==='native_login_store_response_failed')&&!(e.outcome==='waiting'&&e.loginEvent==='store_response_received'&&e.code===undefined&&e.exitCode===undefined&&e.exitSignal===undefined))return undefined;}else if(e.outcome!=='failed')return undefined;extra[key]=e[key];}
 if(extra.exitCode!==undefined&&extra.exitSignal!==undefined)return undefined;
 if(e.tokenDiagnostic!==undefined){const detail=safeTokenDiagnostic(e);if(!detail)return undefined;extra.tokenDiagnostic=detail;}
 if (e.outcome==='failed'&&((e.stage==='runtime_boot'&&e.code==='native_guest_exited')||e.stage==='desktop_service')&&exitCodeValid(e.processExitCode)) extra.processExitCode=e.processExitCode;
 if (e.outcome==='failed'&&e.stage==='runtime_boot'&&guestBootEvents.has(e.guestBootEvent)) extra.guestBootEvent=e.guestBootEvent;
 return {traceId:e.traceId,seq:e.seq,attempt:e.attempt,at:e.at,stage:e.stage,outcome:e.outcome,...(e.code!==undefined?{code:e.code}:{}),...(e.httpStatus!==undefined?{httpStatus:e.httpStatus}:{}),...(e.appleErrorCode!==undefined?{appleErrorCode:e.appleErrorCode}:{}),...extra};
}
function sanitizeDiagnostics(input,limit=256){
 const events=Array.isArray(input?.events)?input.events.slice(-limit).map(safeEvent).filter(Boolean):[];const lastFailure=safeEvent(input?.lastFailure);
 return {events,...(lastFailure?.outcome==='failed'?{lastFailure}:{})};
}
// Typed diagnostics only: never sessions, process output, URLs or raw errors.
class DiagnosticJournal{
 constructor(file){this.file=file;this.data={events:[]};this.sequences=new Map();this.traceId=crypto.randomBytes(16).toString('hex');this.seq=0;
  try{if(fs.existsSync(file)){if(fs.statSync(file).size>192*1024)throw new Error();this.data=sanitizeDiagnostics(JSON.parse(fs.readFileSync(file,'utf8')));}}catch{this.storageCode='native_diagnostics_read_failed';}
  for(const e of this.data.events)this.sequences.set(e.traceId,Math.max(this.sequences.get(e.traceId)||0,e.seq));
 }
 observe(session){const d=sanitizeDiagnostics(session?.diagnostics,128);let changed=false;
  for(const e of d.events){if(e.seq<=(this.sequences.get(e.traceId)||0))continue;this.sequences.set(e.traceId,e.seq);this.data.events.push(e);changed=true;if(e.outcome==='failed')this.retain(e);}
  if(d.lastFailure&&(!this.data.lastFailure||d.lastFailure.at>this.data.lastFailure.at||(d.lastFailure.traceId===this.data.lastFailure.traceId&&d.lastFailure.seq>this.data.lastFailure.seq))){this.retain(d.lastFailure);changed=true;}
  if(changed){this.data.events=this.data.events.slice(-256);while(this.sequences.size>64)this.sequences.delete(this.sequences.keys().next().value);this.persist();}
 }
 retain(e){if(!this.data.lastFailure||e.at>=this.data.lastFailure.at)this.data.lastFailure={...e};}
 record(stage,outcome,code,{processExitCode,guestBootEvent}={}){const e=safeEvent({traceId:this.traceId,seq:++this.seq,attempt:1,at:Date.now(),stage,outcome,processExitCode,guestBootEvent,...(code||outcome==='failed'?{code:CODES.has(code)?code:'native_operation_failed'}:{})});if(!e)return;this.data.events.push(e);this.data.events=this.data.events.slice(-256);if(outcome==='failed')this.retain(e);this.persist();}
 persist(){let temporary;
  try{fs.mkdirSync(path.dirname(this.file),{recursive:true});temporary=this.file+'.'+crypto.randomUUID()+'.tmp';const fd=fs.openSync(temporary,'wx',0o600);try{fs.writeFileSync(fd,JSON.stringify(this.data,null,2)+'\n');fs.fsyncSync(fd);}finally{fs.closeSync(fd);}fs.renameSync(temporary,this.file);this.storageCode=undefined;}
  catch{this.storageCode='native_diagnostics_write_failed';if(temporary)try{fs.unlinkSync(temporary);}catch{}}
 }
 snapshot(limit=256){return {...sanitizeDiagnostics(this.data,limit),...(this.storageCode?{storageCode:this.storageCode}:{})};}
}
// A shareable task snapshot must never contain titles, source IDs, paths or raw errors.
const downloadReasons = new Set(('catalog_track_missing track_source_missing track_type_unsupported duplicate_track playlist_no_downloadable_tracks track_unavailable playlist_access_denied playlist_auth_required playlist_not_found playlist_pagination_failed session_verification_pending session_rejected resource_forbidden resource_not_found catalog_token_failed disk_full permission_denied decryption_failed download_result_missing credentials_failed metadata_failed manifest_failed manifest_queue_timeout_transient manifest_empty_transient manifest_response_invalid quality_unavailable media_failed output_failed lyrics_failed tagging_failed muxing_failed publish_failed validation_failed license_failed interrupted').split(' '));
const downloadStates = new Set(['queued','running','cancelling','succeeded','partial','failed','cancelled','interrupted']);
function safeDownloadReason(code) {
  if (downloadReasons.has(code) || ['native_batch_response_unknown','native_batch_grant_revoked','native_batch_stopped'].includes(code)) return code;
  if (typeof code === 'string' && /^(metadata|manifest|media)_(timeout_|dns_|connection_)?transient$/.test(code)) return code;
  return 'unknown';
}
function safeDownloadTasks(input) {
  if (!Array.isArray(input?.jobs)) return {available:false};
  return {available:true, jobs:input.jobs.slice(0,8).map(job=>{
    const counts = {}, reasons = {}, items = [];
    for (const key of ['total','succeeded','failed','skipped','unavailable']) {
      if (Number.isSafeInteger(job?.[key]) && job[key]>=0) counts[key]=job[key];
    }
    let affected=0, timed=0;
    const trackTimings=[];
    for (const [index,item] of (Array.isArray(job?.items)?job.items:[]).entries()) {
      const timing=item?.timing;
      const keys=['fragments','requests','queuedRequests','readMs','queueMs','replyMs','muxWriteMs'];
      if(timing && keys.every(k=>Number.isSafeInteger(timing[k]) && timing[k]>=0) && timing.queuedRequests<=timing.requests) {
        timed++;
        const counts = Number.isSafeInteger(timing.transportRequests) && timing.transportRequests>=0 && timing.transportRequests<=timing.requests
          ? {transportRequests:timing.transportRequests,...(Number.isSafeInteger(timing.batchRequests) && timing.batchRequests>=0 && timing.batchRequests<=timing.transportRequests ? {batchRequests:timing.batchRequests} : {})} : {};
        if(trackTimings.length<1024) trackTimings.push({position:index+1,...Object.fromEntries(keys.map(k=>[k,timing[k]])),...counts});
      }
      if (!['failed','unavailable','interrupted'].includes(item?.status) && !(item?.status==='skipped' && item.errorCode)) continue;
      const reason=safeDownloadReason(item.errorCode);
      reasons[reason]=(reasons[reason]||0)+1;
      affected++;
      if (items.length<64) items.push({position:index+1,status:item.status,reason});
    }
    return {status:downloadStates.has(job?.status)?job.status:'unknown',...counts,...(job?.errorCode?{reason:safeDownloadReason(job.errorCode)}:{}),reasons,items,omittedItems:affected-items.length,...(timed?{trackTimings,omittedTimings:timed-trackTimings.length}:{})};
  })};
}
function safeLiteProcessing(input) {
  const valid = n => Number.isSafeInteger(n) && n >= 0;
  if (!valid(input?.seen) || !Array.isArray(input.streams) || input.streams.length !== Math.min(64,input.seen)) return {available:false};
  const keys = ['requests','bytes','completed_bytes','wait_ns','create_ns','copy_ns','sdk_ns','held_ns','bytes_per_second'];
  const streams = [];
  for (const s of input.streams) {
    if (!['close','idle','failure','shutdown'].includes(s?.end)) return {available:false};
    if (s.transport_requests!==undefined && !valid(s.transport_requests)) return {available:false};
    const item = {end:s.end,...(s.transport_requests===undefined?{}:{transport_requests:s.transport_requests})};
    for (const group of ['first','reuse']) {
      const g=s[group];
      if (!keys.every(k=>valid(g?.[k])) || g.completed_bytes>g.bytes || (g.sdk_calls!==undefined && !valid(g.sdk_calls))) return {available:false};
      item[group]={...Object.fromEntries(keys.map(k=>[k,g[k]])),...(g.sdk_calls===undefined?{}:{sdk_calls:g.sdk_calls})};
    }
    streams.push(item);
  }
  return {available:true,scope:'current_guest',seen:input.seen,omitted:input.seen-streams.length,streams};
}
module.exports={STAGES,CODES,OUTCOMES,APPLE_CODES,safeEvent,sanitizeDiagnostics,DiagnosticJournal,runtimeCodes,safeRuntime,guestBootEvents,safeDownloadTasks,safeLiteProcessing};
