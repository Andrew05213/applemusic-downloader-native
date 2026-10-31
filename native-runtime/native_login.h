#pragma once
#include <algorithm>
#include <cstdio>
#include <cstring>
#include <string>
#include "cJSON.h"
#include "native_store_bridge.h"
extern bool g_native_login;
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
    std::string line; if (!native_line(line, 32768)) return false;
    cJSON* root = cJSON_Parse(line.c_str()); native_wipe(line);
    if (!root) return false;
    const char* user = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(root, "appleEmail"));
    const char* pass = cJSON_GetStringValue(cJSON_GetObjectItemCaseSensitive(root, "applePassword"));
    bool valid = user && *user && pass && *pass && strlen(user) <= 512 && strlen(pass) <= 4096 && !strchr(user, ':') && native_store_binding_parse(root);
    if (valid) credentials = std::string(user) + ":" + pass;
    if (pass) memset((void*)pass, 0, strlen(pass));
    cJSON* binding=cJSON_GetObjectItemCaseSensitive(root,"storeBinding");
    if (binding) {cJSON* item=cJSON_GetObjectItemCaseSensitive(binding,"identityToken");if(cJSON_IsString(item))memset(item->valuestring,0,strlen(item->valuestring));cJSON* headers=cJSON_GetObjectItemCaseSensitive(binding,"headers");for(cJSON* h=headers?headers->child:nullptr;h;h=h->next)if(cJSON_IsString(h))memset(h->valuestring,0,strlen(h->valuestring));}
    cJSON_Delete(root); return valid;
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
