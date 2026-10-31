#include "native_store_bridge.h"
#include <cassert>
#include <iostream>
#include <map>

namespace {
const char* const full_binding =
    "{\"storeBinding\":{\"identityToken\":\"YWNjb3VudDppZG1zLXRva2Vu\",\"headers\":{"
    "\"X-Apple-I-MD\":\"otp-fixture\",\"X-Apple-I-MD-M\":\"machine-fixture\","
    "\"X-Apple-I-MD-LU\":\"local-fixture\",\"X-Apple-I-MD-RINFO\":\"17106176\","
    "\"X-Mme-Device-Id\":\"device-fixture\",\"X-Apple-I-Client-Time\":\"2026-10-22T00:00:00Z\","
    "\"X-Apple-Locale\":\"en_US\",\"X-Apple-I-TimeZone\":\"UTC\","
    "\"X-Mme-Client-Info\":\"Android fixture identity\"}}}";

bool parse(const std::string& json) {
    cJSON* root = cJSON_Parse(json.c_str());
    assert(root);
    const bool valid = native_store_binding_parse(root);
    cJSON_Delete(root);
    return valid;
}

std::string replace_once(std::string json, const std::string& from, const std::string& to) {
    const std::size_t position = json.find(from);
    assert(position != std::string::npos);
    json.replace(position, from.size(), to);
    return json;
}

struct Message { void* obj = nullptr; };

void validation() {
    assert(parse("{}"));
    assert(!native_store_binding_enabled());
    assert(parse(full_binding));
    assert(native_store_binding_enabled());
    assert(native_store_binding().headers.size() == 9);
    assert(parse("{\"appleEmail\":\"fixture@example.invalid\",\"applePassword\":\"normal-password\"}"));
    assert(!native_store_binding_enabled());
    assert(!parse("{\"storeBinding\":null}"));
    assert(!parse("{\"storeBinding\":{},\"storeBinding\":{}}"));
    assert(!parse(replace_once(full_binding, "\"headers\":{", "\"unknown\":true,\"headers\":{")));
    assert(!parse(replace_once(full_binding, "\"headers\":{", "\"identityToken\":\"ZHVwbGljYXRl\",\"headers\":{")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":\"otp-fixture\",", "")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":", "\"Cookie\":")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":", "\"Authorization\":")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":", "\"Media-User-Token\":")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":", "\"X-Apple-Identity-Token\":")));
    assert(!parse(replace_once(full_binding, "\"X-Apple-I-MD\":\"otp-fixture\",",
        "\"X-Apple-I-MD\":\"otp-fixture\",\"x-apple-i-md\":\"duplicate\",")));
    assert(!parse(replace_once(full_binding, "otp-fixture", "bad\\r\\nCookie: value")));
    assert(!parse(replace_once(full_binding, "otp-fixture", "")));
    assert(!parse(replace_once(full_binding, "otp-fixture", std::string(4097, 'a'))));
    assert(!parse(replace_once(full_binding, "YWNjb3VudDppZG1zLXRva2Vu", "not base64")));
    assert(!native_store_binding_enabled());
    assert(native_store_binding().identity.empty());
    assert(native_store_binding().headers.empty());
}

void urls() {
    assert(native_store_auth_url("https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate"));
    assert(native_store_auth_url("https://p1-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate?Pod=1&PRH=1"));
    assert(native_store_auth_url("https://p999-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate"));
    const char* invalid[] = {
        "http://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        "https://buy.itunes.apple.com.evil.invalid/WebObjects/MZFinance.woa/wa/authenticate",
        "https://buy.itunes.apple.com@evil.invalid/WebObjects/MZFinance.woa/wa/authenticate",
        "https://user@buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        "https://buy.itunes.apple.com:443/WebObjects/MZFinance.woa/wa/authenticate",
        "https://p-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        "https://p1x-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        "https://p1000-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate#fragment",
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate?bad\\path",
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate?bad\npath",
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/buyProduct",
        "https://gsa.apple.com/auth"
    };
    for (const char* url : invalid) assert(!native_store_auth_url(url));
    assert(!native_store_auth_url(nullptr));
}

void message_boundary() {
    Message output;
    std::map<std::string, std::string> headers;
    int original_calls = 0;
    bool original_finished = false;
    const auto original = [&](Message* result, void* request) {
        assert(request == &original_calls);
        ++original_calls;
        result->obj = &headers;
        original_finished = true;
    };
    const auto setter = [&](void* message, const char* name, const char* value) {
        assert(original_finished);
        assert(message == &headers);
        headers[name] = value;
    };
    assert(parse(full_binding));
    assert(native_store_make_bound_message<Message>(&output, &original_calls,
        "https://p25-buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate", original, setter));
    assert(original_calls == 1);
    assert(headers.size() == 10);
    assert(headers.at("X-Apple-Identity-Token") == "YWNjb3VudDppZG1zLXRva2Vu");
    assert(headers.at("X-Apple-I-MD") == "otp-fixture");
    assert(headers.at("X-Apple-I-MD-M") == "machine-fixture");
    assert(headers.count("X-Token") == 0);
    assert(headers.count("Media-User-Token") == 0);
    assert(!native_store_make_bound_message<Message>(&output, &original_calls,
        "https://evil.invalid/authenticate", original, setter));
    assert(original_calls == 1); // Refused before constructing/sending any PET request.
    assert(!native_store_make_bound_message<Message>(nullptr, &original_calls,
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate", original, setter));
    assert(!native_store_make_bound_message<Message>(&output, &original_calls,
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate", nullptr, setter));
    assert(!native_store_make_bound_message<Message>(&output, &original_calls,
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate", original, nullptr));
    assert(!native_store_make_bound_message<Message>(&output, &original_calls,
        "https://buy.itunes.apple.com/WebObjects/MZFinance.woa/wa/authenticate",
        [](Message* result, void*) { result->obj = nullptr; }, setter));
    assert(parse("{}"));
    headers.clear();
    assert(native_store_make_bound_message<Message>(&output, &original_calls,
        "https://unchanged-normal-sdk.invalid/normal-login", original, setter));
    assert(headers.empty()); // Existing Lite login does not acquire GSA headers.
}
}

int main() {
    validation();
    urls();
    message_boundary();
    std::cout << "native_store_bridge: 3 fixture suites passed (no live account)\n";
}
