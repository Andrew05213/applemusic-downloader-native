#pragma once
// The pinned Wrapper supplies the stream API in lite.h.
// Included after Lite's JSON and token helpers. These routes exist only in
// agent-owned service mode, bound to guest loopback with no host forwarding.
static void native_routes(httplib::Server& server) {
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
