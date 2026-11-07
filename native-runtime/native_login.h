#pragma once
#include <algorithm>
#include <cstdio>
#include <cstring>
#include <string>
#include <mutex>
#include <thread>
#include <cerrno>
#include <cstdint>
#include "auth_input.h"
#include "cJSON.h"
#include "native_store_bridge.h"
extern bool g_native_login;
// Fixed reasons and numeric ABI fields only; never forward Store messages.
inline void native_login_failure(const char* reason, int response_type = -1, const int* store_error = nullptr, const long* external_error = nullptr) {
    printf("AMDL_NATIVE_EVENT:{\"type\":\"login_failure\",\"reason\":\"%s\"", reason);
    if (response_type >= 0 && response_type <= 255) printf(",\"storeResponseType\":%d", response_type);
    if (store_error) printf(",\"storeErrorCode\":%d", *store_error);
    if (external_error && *external_error >= -9007199254740991L && *external_error <= 9007199254740991L)
        printf(",\"storeExternalErrorCode\":%ld", *external_error);
    puts("}");
    fflush(stdout);
}
inline void native_login_event(const char* event) {
    if (!g_native_login) return;
    printf("AMDL_NATIVE_EVENT:{\"type\":\"login_event\",\"event\":\"%s\"}\n", event);
    fflush(stdout);
}
inline bool native_line(std::string& out, size_t limit) {
    out.clear(); int ch;
    while ((ch = fgetc(stdin)) != EOF) {
        if (ch == '\n') return !out.empty();
        if (out.size() >= limit || ch == 0) return false;
        out.push_back((char)ch);
    }
    return false;
}
inline void native_wipe(std::string& value) {
    volatile char* p = value.empty() ? nullptr : &value[0];
    for (size_t i = 0; i < value.size(); ++i) p[i] = 0;
    value.clear();
}
inline bool native_credentials(std::string& credentials) {
    std::string line; if (!native_line(line, 32768)) { native_wipe(line); native_login_failure("input_invalid"); return false; }
    cJSON* root = auth_input::parse_record(line); native_wipe(line);
    if (!root) { native_login_failure("input_invalid"); return false; }
    const char* user = auth_input::unique_string(root, "appleEmail");
    const char* pass = auth_input::unique_string(root, "applePassword");
    bool valid = user && *user && pass && *pass && strlen(user) <= 512 && strlen(pass) <= 4096 && !strchr(user, ':') && native_store_binding_parse(root);
    if (valid) credentials = std::string(user) + ":" + pass;
    if (pass) memset((void*)pass, 0, strlen(pass));
    cJSON* binding=cJSON_GetObjectItemCaseSensitive(root,"storeBinding");
    if (binding) {cJSON* item=cJSON_GetObjectItemCaseSensitive(binding,"identityToken");if(cJSON_IsString(item))memset(item->valuestring,0,strlen(item->valuestring));cJSON* headers=cJSON_GetObjectItemCaseSensitive(binding,"headers");for(cJSON* h=headers?headers->child:nullptr;h;h=h->next)if(cJSON_IsString(h))memset(h->valuestring,0,strlen(h->valuestring));}
    cJSON_Delete(root); if (!valid) native_login_failure("input_invalid"); return valid;
}
inline bool native_code(std::string& code) {
    if(native_store_binding_enabled()) return false;
    puts("AMDL_NATIVE_EVENT:{\"type\":\"two_factor_required\"}"); fflush(stdout);
    std::string line; if (!native_line(line, 128)) return false;
    cJSON* root = cJSON_Parse(line.c_str()); native_wipe(line);
    if (!root) return false;
    const char* value = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(root, "code"));
    bool valid = value && strlen(value) == 6;
    if (valid) for (int i = 0; i < 6; ++i) if (value[i] < '0' || value[i] > '9') valid = false;
    if (valid) code = value;
    if (value) memset((void*)value, 0, strlen(value));
    cJSON_Delete(root); return valid;
}

// Private UI transport. Never put interaction text in AuthEvent/stderr/cache.
inline void native_json_line(cJSON* value, const char* prefix) {
    static std::mutex output;
    char* raw = cJSON_PrintUnformatted(value);
    if (raw) { std::lock_guard<std::mutex> lock(output); printf("%s%s\n",prefix,raw); fflush(stdout); memset(raw,0,strlen(raw)); cJSON_free(raw); }
    auth_input::wipe_json_strings(value);cJSON_Delete(value);
}
inline void native_json_id(cJSON* obj,const char* key,uint64_t id) { cJSON_AddStringToObject(obj,key,std::to_string(id).c_str()); }
inline bool native_id(cJSON* obj,const char* key,uint64_t& id,bool optional=false) {
    const char* s=auth_input::unique_string(obj,key);
    if (!s) {id=0;return optional&&!cJSON_GetObjectItemCaseSensitive(obj,key);}
    if (!*s||*s=='0'||strspn(s,"0123456789")!=strlen(s))return false;
    errno=0;char* end=nullptr;id=strtoull(s,&end,10);return !errno&&end&&!*end&&id;
}
inline bool native_number(cJSON* obj,const char* key,int& number) {
    auto* item=cJSON_GetObjectItemCaseSensitive(obj,key);
    if (!cJSON_IsNumber(item)||item->valuedouble!=item->valueint)return false;
    number=item->valueint;return true;
}
inline bool native_keys(cJSON* obj,const char* const* keys,size_t count) {
    if(!cJSON_IsObject(obj))return false;
    for(auto* item=obj->child;item;item=item->next) {
        bool allowed=false;for(size_t i=0;i<count;++i)if(item->string&&strcmp(item->string,keys[i])==0)allowed=true;
        if(!allowed)return false;
        for(auto* prior=obj->child;prior!=item;prior=prior->next)if(strcmp(prior->string,item->string)==0)return false;
    }
    return true;
}
// IDs and enum roles only; never retain copied UI text or credentials here.
struct NativeInteractionMeta { uint64_t attempt=0,request=0,retry=0,cancel=0;int kind=0; };
inline std::mutex& native_meta_mutex(){static std::mutex value;return value;}
inline NativeInteractionMeta& native_meta(){static NativeInteractionMeta value;return value;}
inline void native_interaction_reader() {
    std::string line;
    while(auth_input::read_record(stdin,line,196608)) {
        cJSON* root=auth_input::parse_record(line);native_wipe(line);
        const char* const envelope[]={"type","response"};
        const char* type=auth_input::unique_string(root,"type");
        auto* obj=cJSON_GetObjectItemCaseSensitive(root,"response");
        const char* const keys[]={"protocolVersion","attemptId","requestId","disposition","buttonId","values"};
        AuthInteractionResponse answer;
        int version=0,disposition=0;
        bool valid=native_keys(root,envelope,2)&&type&&strcmp(type,"interaction_response")==0&&native_keys(obj,keys,6)&&
            native_number(obj,"protocolVersion",version)&&version==1&&native_number(obj,"disposition",disposition)&&disposition>=1&&disposition<=3&&
            native_id(obj,"attemptId",answer.attempt_id)&&native_id(obj,"requestId",answer.request_id)&&native_id(obj,"buttonId",answer.button_id,true);
        answer.disposition=static_cast<AuthInteractionDisposition>(disposition);
        auto* values=cJSON_GetObjectItemCaseSensitive(obj,"values");
        valid=valid&&cJSON_IsArray(values)&&cJSON_GetArraySize(values)<=16;
        if(valid)for(auto* v=values->child;v;v=v->next) {
            const char* const vk[]={"fieldId","value"};AuthInteractionValue input;
            const char* value=auth_input::unique_string(v,"value");
            if(!native_keys(v,vk,2)||!native_id(v,"fieldId",input.field_id)||!value||strlen(value)>4096){valid=false;break;}
            input.value=value;answer.values.push_back(std::move(input));
        }
        auth_input::wipe_json_strings(root);cJSON_Delete(root);
        if(valid){
            NativeInteractionMeta meta;
            {std::lock_guard<std::mutex> lock(native_meta_mutex());meta=native_meta();}
            const bool matches=meta.attempt==answer.attempt_id&&meta.request==answer.request_id;
            const auto disposition=answer.disposition;const auto button=answer.button_id;
            const auto result=submit_auth_interaction_response(std::move(answer));
            if(result!=AuthInteractionSubmitResult::Accepted)native_login_event("interaction_answer_rejected");
            else if(matches&&disposition==AuthInteractionDisposition::Submit&&button!=meta.cancel){
                if(meta.kind==4)native_login_event("credentials_submitted");
                else if(meta.kind==5)native_login_event("verification_submitted");
                else if(meta.retry&&meta.retry==button)native_login_event("retry_selected");
            }
        }
    }
    cancel_auth_interaction(current_auth_interaction_attempt());
}
inline void native_display_interaction(AuthInteractionRequest q) {
    NativeInteractionMeta meta;meta.attempt=q.attempt_id;meta.request=q.request_id;meta.kind=static_cast<int>(q.kind);
    for(const auto& b:q.buttons){if(b.role==AuthInteractionButtonRole::Retry)meta.retry=b.id;if(b.role==AuthInteractionButtonRole::Cancel)meta.cancel=b.id;}
    {std::lock_guard<std::mutex> lock(native_meta_mutex());native_meta()=meta;}
    switch(q.unsupported){
    case AuthInteractionUnsupported::FieldType:native_login_event("interaction_unsupported_field");break;
    case AuthInteractionUnsupported::Action:native_login_event("interaction_unsupported_action");break;
    case AuthInteractionUnsupported::Metadata:native_login_event("interaction_unsupported_metadata");break;
    case AuthInteractionUnsupported::Limits:native_login_event("interaction_unsupported_limits");break;
    case AuthInteractionUnsupported::MissingCredentials:native_login_event("interaction_unsupported_credentials");break;
    default:break;
    }
    auto* root=cJSON_CreateObject();
    cJSON_AddNumberToObject(root,"protocolVersion",q.protocol_version);
    native_json_id(root,"attemptId",q.attempt_id);native_json_id(root,"requestId",q.request_id);
    cJSON_AddNumberToObject(root,"expiresAfterMs",q.expires_after_ms);
    cJSON_AddNumberToObject(root,"kind",static_cast<int>(q.kind));cJSON_AddNumberToObject(root,"unsupported",static_cast<int>(q.unsupported));
    cJSON_AddBoolToObject(root,"textTruncated",q.text_truncated);cJSON_AddStringToObject(root,"title",q.title.c_str());cJSON_AddStringToObject(root,"message",q.message.c_str());
    auto* buttons=cJSON_AddArrayToObject(root,"buttons");
    for(const auto& b:q.buttons) {
        auto* v=cJSON_CreateObject();native_json_id(v,"id",b.id);cJSON_AddStringToObject(v,"label",b.label.c_str());
        cJSON_AddNumberToObject(v,"role",static_cast<int>(b.role));cJSON_AddBoolToObject(v,"enabled",b.enabled);
        cJSON_AddNumberToObject(v,"unsupported",static_cast<int>(b.unsupported));cJSON_AddItemToArray(buttons,v);
    }
    auto* fields=cJSON_AddArrayToObject(root,"fields");
    for(const auto& f:q.fields) {
        auto* v=cJSON_CreateObject();native_json_id(v,"id",f.id);cJSON_AddStringToObject(v,"label",f.label.c_str());
        cJSON_AddNumberToObject(v,"type",static_cast<int>(f.type));cJSON_AddBoolToObject(v,"required",f.required);
        cJSON_AddNumberToObject(v,"minBytes",f.min_bytes);cJSON_AddNumberToObject(v,"maxBytes",f.max_bytes);
        cJSON_AddBoolToObject(v,"digitsOnly",f.digits_only);cJSON_AddItemToArray(fields,v);
    }
    native_json_line(root,"AMDL_NATIVE_INTERACTION:");
}
inline void native_auth_number(cJSON* root,const char* key,const AuthNumber& n) {
    if(n.present&&n.value>=-9007199254740991LL&&n.value<=9007199254740991LL)cJSON_AddNumberToObject(root,key,static_cast<double>(n.value));
}
inline void native_auth_event(const AuthEvent& event,void*) {
    if(event.type==AuthEventType::Response||event.type==AuthEventType::AuthenticationFailurePrompt){
        auto* root=cJSON_CreateObject();cJSON_AddStringToObject(root,"type","login_response");
        native_auth_number(root,"storeResponseType",event.diagnostics.response_type);
        native_auth_number(root,"storeErrorCode",event.diagnostics.store_error);
        native_auth_number(root,"storeExternalErrorCode",event.diagnostics.failure_type);
        native_auth_number(root,"storeProtocolErrorCode",event.diagnostics.protocol_error);
        native_auth_number(root,"storeAuthStatus",event.diagnostics.status);
        if(event.diagnostics.http_status.present&&event.diagnostics.http_status.value>=100&&event.diagnostics.http_status.value<=599)native_auth_number(root,"httpStatus",event.diagnostics.http_status);
        native_json_line(root,"AMDL_NATIVE_EVENT:");
        if(event.type==AuthEventType::Response)return;
    }
    const char* notice=nullptr;
    switch(event.type) {
    case AuthEventType::AccountChoice:notice="dialog_sign_in";break;
    case AuthEventType::ExistingAccountSelected:notice="dialog_existing_account_selected";break;
    case AuthEventType::CredentialsRequested:notice="credentials_requested";break;
    case AuthEventType::VerificationCodeRequested:notice="verification_requested";break;
    case AuthEventType::AuthenticationFailurePrompt:notice="dialog_other";break;
    case AuthEventType::UnsupportedDialog:notice="dialog_unhandled";break;
    default:break;
    }
    if(notice){auto* root=cJSON_CreateObject();cJSON_AddStringToObject(root,"type","login_event");cJSON_AddStringToObject(root,"event",notice);native_json_line(root,"AMDL_NATIVE_EVENT:");}
    if(event.type!=AuthEventType::Completed||event.outcome==AuthOutcome::Success)return;
    const char* reason="internal_error";
    switch(event.outcome) {
    case AuthOutcome::AuthenticationFailed:case AuthOutcome::UnexpectedResponse:reason="store_response_failed";break;
    case AuthOutcome::Cancelled:reason="cancelled";break;
    case AuthOutcome::UnsupportedDialog:reason="interaction_unsupported";break;
    case AuthOutcome::InteractionTimedOut:reason="interaction_timeout";break;
    case AuthOutcome::InteractionUnavailable:reason="interaction_unavailable";break;
    case AuthOutcome::MissingResponse:case AuthOutcome::TransportFailed:reason="store_no_response";break;
    case AuthOutcome::InvalidCredentials:case AuthOutcome::InvalidVerificationCode:reason="input_invalid";break;
    default:break;
    }
    auto* root=cJSON_CreateObject();cJSON_AddStringToObject(root,"type","login_failure");cJSON_AddStringToObject(root,"reason",reason);
    if(strcmp(reason,"store_response_failed")==0) {
        native_auth_number(root,"storeResponseType",event.diagnostics.response_type);
        native_auth_number(root,"storeErrorCode",event.diagnostics.store_error);
        native_auth_number(root,"storeExternalErrorCode",event.diagnostics.failure_type);
        native_auth_number(root,"storeProtocolErrorCode",event.diagnostics.protocol_error);
        native_auth_number(root,"storeAuthStatus",event.diagnostics.status);
        native_auth_number(root,"httpStatus",event.diagnostics.http_status);
    }
    native_json_line(root,"AMDL_NATIVE_EVENT:");
}
inline bool native_run_login(struct shared_ptr context) {
    if(!native_store_bridge_install()){native_login_failure("store_bridge_failed");return false;}
    set_auth_event_handler(native_auth_event,nullptr);
    if(!native_store_binding_enabled()) {
        if(!set_auth_interaction_handler(native_display_interaction))return false;
        std::thread(native_interaction_reader).detach();
    } else set_verification_code_provider(native_code);
    const bool success=login(context);
    set_auth_interaction_handler({});set_auth_event_handler(nullptr);
    return success;
}
