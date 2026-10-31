#pragma once

#include <cstddef>
#include <cstring>
#include <functional>
#include <string>
#include <utility>
#include <vector>
#include "cJSON.h"

// The PET is supplied as the private login password, never as a store XToken.
// These additional headers bind that password exchange to its GrandSlam
// identity. Protocol reference (not a claim of live Music interoperability):
// lazyeel/ipatool 4a61d30faaaf164d64e75936690ccb828b995aa2,
// appstore.cpp, AppStore::do_itunes_auth. No implementation is copied.
inline void native_store_wipe(std::string& value) {
    volatile char* bytes = value.empty() ? nullptr : &value[0];
    for (std::size_t index = 0; index < value.size(); ++index) bytes[index] = 0;
    value.clear();
}

struct NativeStoreBinding {
    bool enabled = false;
    std::string identity;
    std::vector<std::pair<std::string, std::string> > headers;

    void clear() {
        enabled = false;
        native_store_wipe(identity);
        for (auto& header : headers) native_store_wipe(header.second);
        headers.clear();
    }
    ~NativeStoreBinding() { clear(); }
};

// External-linkage inline function-local state is one object across C++11
// translation units. Parsing is done before Lite forks its owned login child;
// no mutable provider or arbitrary header API is exposed to the renderer.
inline NativeStoreBinding& native_store_binding() {
    static NativeStoreBinding value;
    return value;
}

inline const char* native_store_header_name(const char* name) {
    static const char* const allowed[] = {
        "X-Apple-I-MD", "X-Apple-I-MD-M", "X-Apple-I-MD-LU",
        "X-Apple-I-MD-RINFO", "X-Mme-Device-Id", "X-Apple-I-Client-Time",
        "X-Apple-Locale", "X-Apple-I-TimeZone", "X-MMe-Client-Info"
    };
    if (!name) return nullptr;
    for (const char* candidate : allowed) {
        const char* left = name;
        const char* right = candidate;
        while (*left && *right) {
            char a = *left++;
            char b = *right++;
            if (a >= 'A' && a <= 'Z') a = static_cast<char>(a + ('a' - 'A'));
            if (b >= 'A' && b <= 'Z') b = static_cast<char>(b + ('a' - 'A'));
            if (a != b) break;
            if (!*left && !*right) return candidate;
        }
    }
    return nullptr;
}

inline bool native_store_header_value(const char* value, std::size_t maximum) {
    if (!value || !*value || std::strlen(value) > maximum) return false;
    for (const unsigned char* character = reinterpret_cast<const unsigned char*>(value);
         *character; ++character) {
        if (*character < 32 || *character > 126) return false;
    }
    return true;
}

inline bool native_store_identity(const char* value) {
    if (!native_store_header_value(value, 8192)) return false;
    const std::size_t length = std::strlen(value);
    if (length < 8 || length % 4 != 0) return false;
    std::size_t padding = 0;
    for (std::size_t index = 0; index < length; ++index) {
        const char character = value[index];
        if (character == '=') {
            if (++padding > 2 || index < length - 2) return false;
        } else if (padding != 0 || !((character >= 'A' && character <= 'Z') ||
                    (character >= 'a' && character <= 'z') ||
                    (character >= '0' && character <= '9') ||
                    character == '+' || character == '/')) return false;
    }
    return true;
}

inline bool native_store_binding_parse(cJSON* root) {
    NativeStoreBinding& binding = native_store_binding();
    binding.clear();
    if (!cJSON_IsObject(root)) return false;
    const cJSON* supplied = nullptr;
    for (const cJSON* item = root->child; item; item = item->next) {
        if (item->string && std::strcmp(item->string, "storeBinding") == 0) {
            if (supplied) return false;
            supplied = item;
        }
    }
    if (!supplied) return true; // Normal Lite credentials remain unchanged.
    if (!cJSON_IsObject(supplied)) return false;
    const cJSON* identity = nullptr;
    const cJSON* headers = nullptr;
    for (const cJSON* item = supplied->child; item; item = item->next) {
        if (!item->string) return false;
        if (std::strcmp(item->string, "identityToken") == 0 && !identity) identity = item;
        else if (std::strcmp(item->string, "headers") == 0 && !headers) headers = item;
        else return false;
    }
    if (!cJSON_IsString(identity) || !native_store_identity(identity->valuestring) ||
        !cJSON_IsObject(headers)) return false;
    NativeStoreBinding candidate;
    candidate.identity = identity->valuestring;
    for (const cJSON* item = headers->child; item; item = item->next) {
        const char* name = native_store_header_name(item->string);
        if (!name || !cJSON_IsString(item) ||
            !native_store_header_value(item->valuestring, 4096) ||
            candidate.headers.size() >= 9) return false;
        for (const auto& header : candidate.headers) if (header.first == name) return false;
        candidate.headers.emplace_back(name, item->valuestring);
    }
    static const char* const required[] = {
        "X-Apple-I-MD", "X-Apple-I-MD-M", "X-Apple-I-MD-LU",
        "X-Apple-I-MD-RINFO", "X-Mme-Device-Id", "X-Apple-I-Client-Time"
    };
    for (const char* name : required) {
        bool found = false;
        for (const auto& header : candidate.headers) if (header.first == name) found = true;
        if (!found) return false;
    }
    binding.identity.swap(candidate.identity);
    binding.headers.swap(candidate.headers);
    binding.enabled = true;
    return true;
}

inline bool native_store_binding_enabled() { return native_store_binding().enabled; }

inline bool native_store_auth_url(const char* url) {
    if (!url || std::strlen(url) > 2048 || std::strncmp(url, "https://", 8) != 0) return false;
    const char* authority = url + 8;
    const char* path = std::strchr(authority, '/');
    if (!path) return false;
    const std::string host(authority, static_cast<std::size_t>(path - authority));
    bool allowed_host = host == "buy.itunes.apple.com";
    if (!allowed_host && host.size() > 22 && host[0] == 'p') {
        const std::size_t dash = host.find('-');
        allowed_host = dash > 1 && dash <= 4 && host.substr(dash) == "-buy.itunes.apple.com";
        for (std::size_t index = 1; allowed_host && index < dash; ++index)
            if (host[index] < '0' || host[index] > '9') allowed_host = false;
    }
    if (!allowed_host) return false;
    const char* query = std::strchr(path, '?');
    const std::string resource(path, query ? static_cast<std::size_t>(query - path) : std::strlen(path));
    if (resource != "/WebObjects/MZFinance.woa/wa/authenticate") return false;
    for (const unsigned char* character = reinterpret_cast<const unsigned char*>(path);
         *character; ++character) {
        if (*character <= 32 || *character >= 127 || *character == '#' || *character == '\\') return false;
    }
    return true;
}

// Injectable adapter makes the actual boundary testable without a live account,
// SDK library, Dobby hook, HTTP client or Apple endpoint.
template <typename Message>
inline bool native_store_make_bound_message(Message* output, void* request,
        const char* url, const std::function<void(Message*, void*)>& original,
        const std::function<void(void*, const char*, const char*)>& set_header) {
    if (!output || !original || !set_header) return false;
    NativeStoreBinding& binding = native_store_binding();
    if (binding.enabled && !native_store_auth_url(url)) return false;
    original(output, request);
    if (!output->obj) return false;
    if (!binding.enabled) return true;
    set_header(output->obj, "X-Apple-Identity-Token", binding.identity.c_str());
    for (const auto& header : binding.headers)
        set_header(output->obj, header.first.c_str(), header.second.c_str());
    return true;
}

bool native_store_bridge_install();

#ifdef AMDL_STORE_BRIDGE_IMPLEMENTATION
#include <unistd.h>
#include "dobby.h"

namespace {
typedef void (*NativeStoreMakeMessage)(struct shared_ptr*, void*, union std_string*);
typedef void (*NativeStoreSetHeader)(void*, union std_string*, union std_string*);
NativeStoreMakeMessage native_store_original_message = nullptr;
NativeStoreSetHeader native_store_set_header = nullptr;

void native_store_make_message_hook(struct shared_ptr* output, void* request, union std_string* url) {
    if (!output || !url || !native_store_original_message || !native_store_set_header) _exit(1);
    const bool valid = native_store_make_bound_message<struct shared_ptr>(output, request, std_string_data(url),
        [url](struct shared_ptr* result, void* object) { native_store_original_message(result, object, url); },
        [](void* message, const char* name, const char* value) {
            union std_string key = new_std_string(name);
            union std_string header = new_std_string(value);
            native_store_set_header(message, &key, &header);
        });
    if (!valid) _exit(1); // Owned native-login child only; never send a PET to an unexpected origin.
}
}

bool native_store_bridge_install() {
    if (!native_store_binding_enabled()) return true;
    if (native_store_original_message) return true;
    void* target = DobbySymbolResolver("libstoreservicescore.so",
        "_ZN17storeservicescore19AuthenticateRequest12_makeMessageERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEE");
    void* setter = DobbySymbolResolver("libmediaplatform.so",
        "_ZN13mediaplatform11HTTPMessage9setHeaderERKNSt6__ndk112basic_stringIcNS1_11char_traitsIcEENS1_9allocatorIcEEEES9_");
    if (!target || !setter) return false;
    native_store_set_header = reinterpret_cast<NativeStoreSetHeader>(setter);
    if (DobbyHook(target, reinterpret_cast<void*>(native_store_make_message_hook),
            reinterpret_cast<void**>(&native_store_original_message)) != 0 ||
            !native_store_original_message) {
        native_store_set_header = nullptr;
        native_store_original_message = nullptr;
        return false;
    }
    return true;
}
#endif
