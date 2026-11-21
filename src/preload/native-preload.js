'use strict';
const { contextBridge, ipcRenderer } = require('electron');
// Sandboxed preloads cannot load project modules. Keep the narrow wire schema
// aligned with electron/native-login.js; no arbitrary response object crosses.
const states = new Set(['checking', 'needs_login', 'authenticating', 'needs_2fa', 'needs_interaction', 'authenticated', 'unverified', 'runtime_unavailable']);
const codes = new Set(['native_gsa_provider_unavailable', 'native_gsa_protocol_invalid', 'native_gsa_unavailable', 'native_gsa_authentication_failed', 'native_gsa_cancelled', 'native_gsa_input_invalid', 'native_gsa_code_rejected', 'native_gsa_challenge_expired', 'native_gsa_sms_cooldown', 'native_gsa_sms_unavailable', 'native_gsa_busy', 'native_music_binding_failed']);
for(const code of 'native_auth_required native_session_unverified native_login_failed native_auth_busy native_auth_input_invalid native_auth_cancelled native_adi_provider_unavailable native_adi_provisioning_failed native_adi_state_invalid native_adi_cancelled native_adi_not_provisioned native_network_dns_failed native_network_timeout native_tls_certificate_invalid native_network_connection_failed native_adi_http_failed native_adi_response_invalid native_agent_unavailable native_agent_protocol_invalid native_service_unavailable native_service_timeout native_service_closed native_api_unavailable native_api_failed native_api_response_invalid native_login_rejected native_code_rejected native_sms_rejected native_operation_failed native_runtime_unavailable native_diagnostics_read_failed native_diagnostics_write_failed'.split(' '))codes.add(code);
const diagnosticStages=new Set('restore_session login_start stop_lite device_login device_code gsa_provider gsa_prepare adi_helper_start adi_initialize adi_status adi_lookup adi_provision_start adi_provision_sdk_start adi_provision_finish adi_provision_sdk_finish adi_state_save adi_otp gsa_authenticate phone_list sms_wait_phone sms_send sms_wait_code sms_verify gsa_resume store_credentials lite_bind music_service_start music_tokens music_storefront login_complete login_cancel desktop_login_submit desktop_sms_send desktop_sms_code desktop_service desktop_recheck desktop_code diagnostics'.split(' '));
for(const code of 'native_music_developer_rejected native_music_user_rejected native_music_rate_limited native_music_upstream_failed native_music_http_failed native_music_response_invalid'.split(' '))codes.add(code);
const appleCodes=new Set(['40100','40101','40300','40301','40302','40303','42900','50000','50001','50300']);
const guestBootEvents = new Set('init_start modules_ready network_ready state_mounted service_start dev_mount_failed proc_mount_failed sys_mount_failed run_mount_failed module_load_failed loopback_failed network_device_missing network_address_failed network_route_failed state_device_missing data_mount_failed ca_bundle_missing control_capability_missing control_capability_copy_failed control_capability_permissions_failed service_exited'.split(' '));
for (const stage of 'runtime_port runtime_bundle runtime_state runtime_boot desktop_api'.split(' ')) diagnosticStages.add(stage);
for (const code of 'native_server_failed native_arguments_invalid native_parent_required native_parent_invalid native_parent_unavailable native_bundle_missing native_bundle_invalid native_directory_unavailable native_job_store_unavailable native_queue_unavailable native_queue_in_use native_api_unavailable native_bundle_identity_mismatch native_state_invalid native_state_in_use native_operation_cancelled native_runtime_prepare_timeout native_guest_not_ready native_guest_exited native_process_start_failed native_port_in_use lite_invalid_port native_service_protocol_invalid native_service_version_mismatch native_api_connection_failed native_api_timeout'.split(' ')) codes.add(code);
for(const code of 'native_adi_body_read_failed native_adi_body_too_large native_adi_body_empty native_adi_plist_invalid_xml native_adi_plist_envelope_invalid native_adi_plist_duplicate_key native_adi_plist_type_unsupported native_adi_plist_real_unsupported native_adi_plist_date_unsupported native_adi_plist_integer_invalid native_adi_plist_integer_range native_adi_plist_data_invalid native_adi_plist_real_invalid native_adi_plist_depth_limit native_adi_plist_node_limit native_adi_plist_binary_unsupported native_adi_status_invalid native_adi_service_rejected native_adi_spim_invalid native_adi_cpim_invalid native_adi_ptm_invalid native_adi_trust_key_invalid native_adi_routing_invalid native_adi_endpoints_invalid'.split(' '))codes.add(code);
for(const code of 'native_login_process_start_failed native_login_process_exited native_login_process_signalled native_login_protocol_failed native_login_process_timeout native_login_store_no_response native_login_store_response_failed native_login_store_bridge_failed native_login_input_invalid native_login_interaction_timeout native_login_interaction_unavailable native_login_interaction_unsupported native_login_internal_error native_login_token_cache_failed'.split(' '))codes.add(code);
// Mirrors the main-process token diagnostic allowlist above this boundary.
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
function diagnosticEvent(e){
 if(!e||typeof e.traceId!=='string'||!/^[a-f0-9]{32}$/.test(e.traceId)||!Number.isSafeInteger(e.seq)||e.seq<1||!Number.isSafeInteger(e.attempt)||e.attempt<1||!Number.isSafeInteger(e.at)||e.at<1||e.at>4102444800000||!diagnosticStages.has(e.stage)||!['begin','ok','waiting','failed','cancelled'].includes(e.outcome)||(e.code!==undefined&&!codes.has(e.code))||(e.outcome==='failed'&&!codes.has(e.code))||(e.httpStatus!==undefined&&(!Number.isInteger(e.httpStatus)||e.httpStatus<100||e.httpStatus>599))||(e.appleErrorCode!==undefined&&!appleCodes.has(e.appleErrorCode)))return undefined;
 const loginEvents=new Set('dialog_sign_in dialog_other dialog_existing_account_selected dialog_unhandled credentials_requested verification_requested store_response_received credentials_submitted verification_submitted retry_selected interaction_answer_rejected interaction_unsupported_field interaction_unsupported_action interaction_unsupported_metadata interaction_unsupported_limits interaction_unsupported_credentials'.split(' '));
 const extra={};if(e.loginEvent!==undefined){if(e.stage!=='device_login'||e.outcome!=='waiting'||e.code!==undefined||!loginEvents.has(e.loginEvent))return undefined;extra.loginEvent=e.loginEvent;}
 for(const [key,min,max] of [['exitCode',0,255],['exitSignal',1,64],['storeResponseType',0,255],['storeErrorCode',-2147483648,2147483647],['storeExternalErrorCode',-9007199254740991,9007199254740991],['storeProtocolErrorCode',-9007199254740991,9007199254740991],['storeAuthStatus',-2147483648,2147483647]])if(e[key]!==undefined){if(e.stage!=='device_login'||!Number.isSafeInteger(e[key])||e[key]<min||e[key]>max)return undefined;if(key.startsWith('store')){if(!(e.outcome==='failed'&&e.code==='native_login_store_response_failed')&&!(e.outcome==='waiting'&&e.loginEvent==='store_response_received'&&e.code===undefined&&e.exitCode===undefined&&e.exitSignal===undefined))return undefined;}else if(e.outcome!=='failed')return undefined;extra[key]=e[key];}
 if(extra.exitCode!==undefined&&extra.exitSignal!==undefined)return undefined;
 if(e.tokenDiagnostic!==undefined){const detail=safeTokenDiagnostic(e);if(!detail)return undefined;extra.tokenDiagnostic=detail;}
 if(e.outcome==='failed'&&((e.stage==='runtime_boot'&&e.code==='native_guest_exited')||e.stage==='desktop_service')&&Number.isInteger(e.processExitCode)&&e.processExitCode>=0&&e.processExitCode<=0xffffffff)extra.processExitCode=e.processExitCode;
 if(e.outcome==='failed'&&e.stage==='runtime_boot'&&guestBootEvents.has(e.guestBootEvent))extra.guestBootEvent=e.guestBootEvent;
 return {traceId:e.traceId,seq:e.seq,attempt:e.attempt,at:e.at,stage:e.stage,outcome:e.outcome,...(e.code!==undefined?{code:e.code}:{}),...(e.httpStatus!==undefined?{httpStatus:e.httpStatus}:{}),...(e.appleErrorCode!==undefined?{appleErrorCode:e.appleErrorCode}:{}),...extra};
}
function sessionCopy(value) {
  const legacyFactor=value?.factor!=null;
  const state = legacyFactor?'needs_login':states.has(value?.state) ? value.state : 'runtime_unavailable';
  const region = state === 'authenticated' && /^[a-z]{2}$/.test(value?.region) ? value.region : undefined;
  if (state === 'authenticated' && !region) return { state: 'unverified' };
  const result = { state, ...(region ? { region } : {}), ...(legacyFactor?{code:'native_login_failed'}:codes.has(value?.code) ? { code: value.code } : {}) };
  if(diagnosticStages.has(value?.stage))result.stage=value.stage;
  if(value?.diagnostics){const d=value.diagnostics;const lastFailure=diagnosticEvent(d.lastFailure);result.diagnostics={events:Array.isArray(d.events)?d.events.slice(-128).map(diagnosticEvent).filter(Boolean):[],...(lastFailure?.outcome==='failed'?{lastFailure}:{})};}
  return result;
}
contextBridge.exposeInMainWorld('nativeDesktop', Object.freeze({
  cacheInfo: () => ipcRenderer.invoke('native:cache-info'),
  setCacheLimit: limit => ipcRenderer.invoke('native:cache-limit',limit),
  clearCache: () => ipcRenderer.invoke('native:cache-clear'),
  chooseDownloadDirectory: () => ipcRenderer.invoke('native:choose-download-directory'),
  revealJobFolder: id => ipcRenderer.invoke('native:reveal-job-folder',id),
  refreshCatalog: () => ipcRenderer.invoke('native:catalog-refresh'),
  prefetchCatalog: target => ipcRenderer.invoke('native:catalog-prefetch',target),
  artworkURL: url => 'amdl-artwork://image/?url='+encodeURIComponent(url),
  api: (path, options) => ipcRenderer.invoke('native:api', path, options),
  playbackChunk: path => ipcRenderer.invoke('native:playback-chunk',path),
  cancelPlaybackChunks: base => ipcRenderer.invoke('native:playback-cancel',base),
  openLogin: () => ipcRenderer.invoke('native:open-login'),
  login: (input) => ipcRenderer.invoke('native:login', input),
  submitCode: (code) => ipcRenderer.invoke('native:two-factor', code),
  getInteraction: () => ipcRenderer.invoke('native:get-auth-interaction'),
  answerInteraction: value => ipcRenderer.invoke('native:answer-auth-interaction', value),
  cancelLogin: () => ipcRenderer.invoke('native:cancel-login'),
  recheck: () => ipcRenderer.invoke('native:recheck'),
  getState: () => ipcRenderer.invoke('native:state'),
  exportDiagnostics: () => ipcRenderer.invoke('native:export-diagnostics'),
  onReady: (callback) => { if(typeof callback !== 'function') return () => {}; const listener=()=>callback();ipcRenderer.on('native:backend-ready',listener);return ()=>ipcRenderer.removeListener('native:backend-ready',listener); },
  onSession: (callback) => {
    if (typeof callback !== 'function') return () => {};
    const listener = (_event, value) => { if (value && typeof value.state === 'string') callback(sessionCopy(value)); };
    ipcRenderer.on('native:session-state', listener);
    return () => ipcRenderer.removeListener('native:session-state', listener);
  },
}));
