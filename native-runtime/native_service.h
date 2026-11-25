#pragma once
#include "native_batch_http.h"
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <sys/un.h>
#include <unistd.h>

// Only this private agent-owned build wires the upstream opt-in. Each Lite
// worker reuses a guest-local connection for a short grant check, including
// between blocks. The grant is neither an Apple token nor the host capability.
class NativeBatchVerifier {
    int fd_ = -1;

    void reset() {
        if (fd_ >= 0) close(fd_);
        fd_ = -1;
    }

    bool connect_agent() {
        fd_ = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC | SOCK_NONBLOCK, 0);
        if (fd_ < 0) return false;
        sockaddr_un addr{};
        addr.sun_family = AF_UNIX;
        constexpr char path[] = "/run/amdl-native-batch.sock";
        static_assert(sizeof(path) <= sizeof(addr.sun_path), "batch socket path is too long");
        std::memcpy(addr.sun_path, path, sizeof(path));
        const timeval timeout{0, 250000};
        if (connect(fd_, reinterpret_cast<const sockaddr*>(&addr), sizeof(addr)) != 0 ||
            fcntl(fd_, F_SETFL, 0) != 0 ||
            setsockopt(fd_, SOL_SOCKET, SO_SNDTIMEO, &timeout, sizeof(timeout)) != 0 ||
            setsockopt(fd_, SOL_SOCKET, SO_RCVTIMEO, &timeout, sizeof(timeout)) != 0) {
            reset();
            return false;
        }
        return true;
    }

public:
    ~NativeBatchVerifier() { reset(); }

    bool allowed(const std::string& grant) {
        if (grant.size() != 32 || grant.find_first_not_of("0123456789abcdef") != std::string::npos) return false;
        if (fd_ < 0 && !connect_agent()) return false;
        unsigned char result = 0;
        if (send(fd_, grant.data(), grant.size(), MSG_NOSIGNAL) != static_cast<ssize_t>(grant.size()) ||
            recv(fd_, &result, 1, 0) != 1) {
            reset();
            return false;
        }
        return result == 1;
    }
};

// The pinned Wrapper supplies the stream API in lite.h.
// Included after Lite's JSON and token helpers. These routes exist only in
// agent-owned service mode, bound to guest loopback with no host forwarding.
static void native_routes(httplib::Server& server) {
    const char* batch = std::getenv("AMDL_EXPERIMENTAL_LITE_BATCH");
    if (g_native_service && batch && std::strcmp(batch, "1") == 0) {
        native_batch::register_http_routes(server, true, [](const httplib::Request& req) {
            if (req.get_header_value_count("X-AMDL-Batch-Grant") != 1) return false;
            thread_local NativeBatchVerifier verifier;
            return verifier.allowed(req.get_header_value("X-AMDL-Batch-Grant"));
        });
    }
    server.Get("/native/tokens", [](const httplib::Request& req, httplib::Response& res) {
        if (!g_native_service || req.remote_addr != "127.0.0.1") { res.status = 404; return; }
        WebTokens tokens = get_web_tokens();
        if (!tokens.valid()) { res.status = 401; res.set_content(json_error(401, "native_auth_required"), "application/json"); return; }
        cJSON* data = cJSON_CreateObject();
        cJSON_AddStringToObject(data, "developer", tokens.dev_token.c_str());
        cJSON_AddStringToObject(data, "media", tokens.music_token.c_str());
        cJSON_AddStringToObject(data, "region", tokens.storefront_id.c_str());
        res.set_header("Cache-Control", "no-store");
        res.set_content(json_success(data), "application/json");
    });
    server.Post("/native/decrypt", [](const httplib::Request& req, httplib::Response& res) {
        if (!g_native_service || req.remote_addr != "127.0.0.1") { res.status = 404; return; }
        auto stream = req.get_header_value("X-AMDL-Stream");
        auto adam = req.get_param_value("adamId");
        auto uri = req.get_param_value("uri");
        if (stream.size() != 32 || adam.empty() || adam.size() > 20 || uri.size() > 2048 ||
            uri.find("skd://itunes.apple.com/") != 0 || req.body.empty() || req.body.size() > (1<<20) || req.body.size() % 16) { res.status = 400; return; }
        for (char c : stream) if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f'))) { res.status = 400; return; }
        for (char c : adam) if (c < '0' || c > '9') { res.status = 400; return; }
        std::string output;
        if (!native_decrypt(stream, adam, uri, req.body, output)) { res.status = 502; return; }
        res.set_header("Cache-Control", "no-store");
        res.set_content(output, "application/octet-stream");
    });
    server.Delete("/native/decrypt", [](const httplib::Request& req, httplib::Response& res) {
        if (!g_native_service || req.remote_addr != "127.0.0.1") { res.status = 404; return; }
        native_decrypt_close(req.get_header_value("X-AMDL-Stream"));
        res.set_content("{}", "application/json");
    });
}
