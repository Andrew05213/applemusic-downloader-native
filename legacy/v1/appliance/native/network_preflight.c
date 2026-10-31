#define _POSIX_C_SOURCE 200809L

#include "network_preflight.h"

#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

extern int unsetenv(const char *name);

// Host-only syntax checks use MinGW, which omits these POSIX declarations.
// The production Android/Bionic build must supply the real flags; it never
// takes this compatibility branch.
#if defined(_WIN32)
extern int lstat(const char *path, struct stat *status);
#define O_CLOEXEC 0
#define O_NOFOLLOW 0
#endif

typedef void CURL;
typedef int CURLcode;
typedef unsigned char xmlChar;
typedef struct _xmlDoc *xmlDocPtr;
typedef struct _xmlXPathContext *xmlXPathContextPtr;
typedef struct _xmlXPathObject *xmlXPathObjectPtr;

extern CURLcode curl_global_init(long flags);
extern void curl_global_cleanup(void);
extern CURL *curl_easy_init(void);
extern CURLcode curl_easy_setopt(CURL *handle, int option, ...);
extern CURLcode curl_easy_perform(CURL *handle);
extern CURLcode curl_easy_getinfo(CURL *handle, int info, ...);
extern void curl_easy_cleanup(CURL *handle);

extern xmlDocPtr xmlReadMemory(const char *buffer, int size, const char *url,
                              const char *encoding, int options);
extern void xmlFreeDoc(xmlDocPtr document);
extern xmlXPathContextPtr xmlXPathNewContext(xmlDocPtr document);
extern void xmlXPathFreeContext(xmlXPathContextPtr context);
extern xmlXPathObjectPtr xmlXPathEvalExpression(const xmlChar *expression,
                                                xmlXPathContextPtr context);
extern xmlChar *xmlXPathCastToString(xmlXPathObjectPtr object);
extern void xmlXPathFreeObject(xmlXPathObjectPtr object);

#define AMDL_BAG_URL "https://init.itunes.apple.com/bag.xml?ix=6"
#define AMDL_BAG_MAX_BYTES (1024 * 1024)
#define AMDL_ANDROID_CA_BUNDLE_PATH "/usr/local/android/cert.pem"
#define AMDL_ANDROID_CA_BUNDLE_MAX_BYTES (4 * 1024 * 1024)
#define AMDL_CURL_GLOBAL_DEFAULT 3L
#define AMDL_CURLOPT_WRITEDATA 10001
#define AMDL_CURLOPT_URL 10002
#define AMDL_CURLOPT_PROXY 10004
#define AMDL_CURLOPT_WRITEFUNCTION 20011
#define AMDL_CURLOPT_USERAGENT 10018
#define AMDL_CURLOPT_FAILONERROR 45
#define AMDL_CURLOPT_FOLLOWLOCATION 52
#define AMDL_CURLOPT_SSL_VERIFYPEER 64
#define AMDL_CURLOPT_CAINFO 10065
#define AMDL_CURLOPT_MAXREDIRS 68
#define AMDL_CURLOPT_SSL_VERIFYHOST 81
#define AMDL_CURLOPT_NOSIGNAL 99
#define AMDL_CURLOPT_TIMEOUT_MS 155
#define AMDL_CURLOPT_CONNECTTIMEOUT_MS 156
#define AMDL_CURLOPT_PROTOCOLS 181
#define AMDL_CURLOPT_REDIR_PROTOCOLS 182
#define AMDL_CURLPROTO_HTTPS 2L
#define AMDL_CURLINFO_RESPONSE_CODE 0x200002
#define AMDL_CURLINFO_SSL_VERIFYRESULT 0x20000d
#define AMDL_XML_PARSE_NOERROR 32
#define AMDL_XML_PARSE_NOWARNING 64
#define AMDL_XML_PARSE_NONET 2048

struct amdl_bag_buffer {
    unsigned char *data;
    size_t length;
    int overflow;
};

struct amdl_network_preflight_result {
    int ok;
    const char *stage;
    int curl_code;
    long ssl_verify_result;
};

static int amdl_parse_result_fd(void) {
    const char *raw = getenv("AMDL_NETWORK_RESULT_FD");
    if (raw == NULL || *raw == '\0') {
        return -1;
    }
    char *end = NULL;
    errno = 0;
    long descriptor = strtol(raw, &end, 10);
    unsetenv("AMDL_NETWORK_RESULT_FD");
    if (errno != 0 || end == raw || *end != '\0' || descriptor < 3 ||
        descriptor > 1024) {
        return -2;
    }
    return (int)descriptor;
}

static int amdl_write_all(int fd, const char *data, size_t length) {
    while (length > 0) {
        ssize_t written = write(fd, data, length);
        if (written < 0 && errno == EINTR) {
            continue;
        }
        if (written <= 0) {
            return 0;
        }
        data += (size_t)written;
        length -= (size_t)written;
    }
    return 1;
}

static const char *amdl_result_stage_suffix(const char *stage) {
    if (stage == NULL || *stage == '\0') {
        return "";
    }
    if (strcmp(stage, "ca_bundle_missing") == 0) {
        return ",\"stage\":\"ca_bundle_missing\"";
    }
    if (strcmp(stage, "ca_bundle_invalid") == 0) {
        return ",\"stage\":\"ca_bundle_invalid\"";
    }
    if (strcmp(stage, "dns") == 0) {
        return ",\"stage\":\"dns\"";
    }
    if (strcmp(stage, "tcp_connect") == 0) {
        return ",\"stage\":\"tcp_connect\"";
    }
    if (strcmp(stage, "tls") == 0) {
        return ",\"stage\":\"tls\"";
    }
    if (strcmp(stage, "url_bag_fetch") == 0) {
        return ",\"stage\":\"url_bag_fetch\"";
    }
    if (strcmp(stage, "url_bag_parse") == 0) {
        return ",\"stage\":\"url_bag_parse\"";
    }
    if (strcmp(stage, "url_bag_key_lookup") == 0) {
        return ",\"stage\":\"url_bag_key_lookup\"";
    }
    return "";
}

static int amdl_safe_curl_code(CURLcode code) {
    return code >= 0 && code <= 99 ? code : 0;
}

static long amdl_safe_ssl_verify_result(long value) {
    // OpenSSL verification codes are non-negative and small. Do not serialize
    // an unexpected library value across the result-FD protocol.
    return value >= 0 && value <= 65535 ? value : 0;
}

static int amdl_write_result(
    int fd, const struct amdl_network_preflight_result *result) {
    if (result == NULL) {
        return 0;
    }
    char document[256];
    int length = snprintf(
        document, sizeof(document),
        "{\"schemaVersion\":2,\"ok\":%s%s,\"curlCode\":%d,\"sslVerifyResult\":%ld,\"caSource\":\"bundled_v1\"}\n",
        result->ok ? "true" : "false", amdl_result_stage_suffix(result->stage),
        amdl_safe_curl_code(result->curl_code),
        amdl_safe_ssl_verify_result(result->ssl_verify_result));
    if (length <= 0 || (size_t)length >= sizeof(document)) {
        return 0;
    }
    return amdl_write_all(fd, document, (size_t)length);
}

static const char *amdl_validate_ca_bundle(void) {
    struct stat link_status;
    if (lstat(AMDL_ANDROID_CA_BUNDLE_PATH, &link_status) != 0) {
        return errno == ENOENT || errno == ENOTDIR ? "ca_bundle_missing"
                                                : "ca_bundle_invalid";
    }
    if (!S_ISREG(link_status.st_mode) || link_status.st_uid != 0 ||
        link_status.st_gid != 0 || (link_status.st_mode & 07777) != 0644 ||
        link_status.st_nlink != 1 || link_status.st_size <= 0 ||
        link_status.st_size > AMDL_ANDROID_CA_BUNDLE_MAX_BYTES) {
        return "ca_bundle_invalid";
    }
    int descriptor = open(AMDL_ANDROID_CA_BUNDLE_PATH,
                          O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (descriptor < 0) {
        return errno == ENOENT || errno == ENOTDIR ? "ca_bundle_missing"
                                                : "ca_bundle_invalid";
    }
    struct stat opened_status;
    unsigned char first_byte = 0;
    int valid = fstat(descriptor, &opened_status) == 0 &&
                S_ISREG(opened_status.st_mode) &&
                opened_status.st_dev == link_status.st_dev &&
                opened_status.st_ino == link_status.st_ino &&
                opened_status.st_uid == 0 && opened_status.st_gid == 0 &&
                (opened_status.st_mode & 07777) == 0644 &&
                opened_status.st_nlink == 1 && opened_status.st_size > 0 &&
                opened_status.st_size <= AMDL_ANDROID_CA_BUNDLE_MAX_BYTES &&
                read(descriptor, &first_byte, sizeof(first_byte)) ==
                    (ssize_t)sizeof(first_byte);
    close(descriptor);
    return valid ? "" : "ca_bundle_invalid";
}

static size_t amdl_bag_write(char *data, size_t size, size_t count,
                             void *opaque) {
    struct amdl_bag_buffer *buffer = (struct amdl_bag_buffer *)opaque;
    if (buffer == NULL || data == NULL || size == 0 ||
        count > SIZE_MAX / size) {
        return 0;
    }
    size_t length = size * count;
    if (length > AMDL_BAG_MAX_BYTES - buffer->length) {
        buffer->overflow = 1;
        return 0;
    }
    memcpy(buffer->data + buffer->length, data, length);
    buffer->length += length;
    return length;
}

static int amdl_setopt_long(CURL *curl, int option, long value) {
    return curl_easy_setopt(curl, option, value) == 0;
}

static const char *amdl_curl_failure_stage(CURLcode code) {
    switch (code) {
    case 5:
    case 6:
        return "dns";
    case 7:
        return "tcp_connect";
    case 35:
    case 51:
    case 58:
    case 59:
    case 60:
    case 66:
    case 80:
    case 82:
    case 83:
    case 90:
    case 91:
        return "tls";
    case 77:
        return "ca_bundle_invalid";
    case 22:
    case 23:
    case 47:
    case 52:
    case 55:
    case 56:
        return "url_bag_fetch";
    default:
        return "";
    }
}

static int amdl_bag_has_authenticate_account(const unsigned char *data,
                                              size_t length,
                                              const char **stage) {
    if (data == NULL || length == 0 || length > AMDL_BAG_MAX_BYTES ||
        length > INT_MAX) {
        *stage = "url_bag_parse";
        return 0;
    }
    xmlDocPtr document = xmlReadMemory(
        (const char *)data, (int)length, NULL, NULL,
        AMDL_XML_PARSE_NOERROR | AMDL_XML_PARSE_NOWARNING | AMDL_XML_PARSE_NONET);
    if (document == NULL) {
        *stage = "url_bag_parse";
        return 0;
    }
    xmlXPathContextPtr context = xmlXPathNewContext(document);
    if (context == NULL) {
        xmlFreeDoc(document);
        *stage = "url_bag_parse";
        return 0;
    }
    static const xmlChar expression[] =
        "string(//dict/key[normalize-space(.)='authenticateAccount']/following-sibling::*[1])";
    xmlXPathObjectPtr object = xmlXPathEvalExpression(expression, context);
    if (object == NULL) {
        xmlXPathFreeContext(context);
        xmlFreeDoc(document);
        *stage = "url_bag_parse";
        return 0;
    }
    xmlChar *value = xmlXPathCastToString(object);
    int present = 0;
    if (value != NULL) {
        for (const xmlChar *cursor = value; *cursor != 0; ++cursor) {
            if (*cursor != ' ' && *cursor != '\t' && *cursor != '\r' &&
                *cursor != '\n') {
                present = 1;
                break;
            }
        }
    }
    xmlXPathFreeObject(object);
    xmlXPathFreeContext(context);
    xmlFreeDoc(document);
    if (!present) {
        *stage = "url_bag_key_lookup";
    }
    return present;
}

static int amdl_run_bag_preflight(
    const char *proxy, struct amdl_network_preflight_result *result) {
    CURL *curl = NULL;
    struct amdl_bag_buffer buffer = {0};
    long status = 0;
    int curl_global_ready = 0;
    if (result == NULL) {
        return 0;
    }
    result->ok = 0;
    result->stage = "";
    result->curl_code = 0;
    result->ssl_verify_result = 0;
    if (proxy == NULL || strncmp(proxy, "http://127.0.0.1:", 17) != 0 ||
        strchr(proxy + 17, '/') != NULL || strchr(proxy, '@') != NULL) {
        return 0;
    }
    result->stage = amdl_validate_ca_bundle();
    if (*result->stage != '\0') {
        return 0;
    }
    buffer.data = calloc(AMDL_BAG_MAX_BYTES + 1, 1);
    if (buffer.data == NULL || curl_global_init(AMDL_CURL_GLOBAL_DEFAULT) != 0) {
        goto cleanup;
    }
    curl_global_ready = 1;
    curl = curl_easy_init();
    if (curl == NULL ||
        curl_easy_setopt(curl, AMDL_CURLOPT_URL, AMDL_BAG_URL) != 0 ||
        curl_easy_setopt(curl, AMDL_CURLOPT_PROXY, proxy) != 0 ||
        curl_easy_setopt(curl, AMDL_CURLOPT_CAINFO, AMDL_ANDROID_CA_BUNDLE_PATH) != 0 ||
        curl_easy_setopt(curl, AMDL_CURLOPT_USERAGENT,
                         "AppleMusicDownloader-NetworkPreflight/1") != 0 ||
        curl_easy_setopt(curl, AMDL_CURLOPT_WRITEFUNCTION, amdl_bag_write) != 0 ||
        curl_easy_setopt(curl, AMDL_CURLOPT_WRITEDATA, &buffer) != 0 ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_FAILONERROR, 0L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_FOLLOWLOCATION, 0L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_MAXREDIRS, 0L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_NOSIGNAL, 1L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_TIMEOUT_MS, 20000L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_CONNECTTIMEOUT_MS, 10000L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_SSL_VERIFYPEER, 1L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_SSL_VERIFYHOST, 2L) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_PROTOCOLS, AMDL_CURLPROTO_HTTPS) ||
        !amdl_setopt_long(curl, AMDL_CURLOPT_REDIR_PROTOCOLS,
                          AMDL_CURLPROTO_HTTPS)) {
        goto cleanup;
    }
    CURLcode curl_result = curl_easy_perform(curl);
    result->curl_code = amdl_safe_curl_code(curl_result);
    long ssl_verify_result = 0;
    if (curl_easy_getinfo(curl, AMDL_CURLINFO_SSL_VERIFYRESULT,
                          &ssl_verify_result) == 0) {
        result->ssl_verify_result = amdl_safe_ssl_verify_result(ssl_verify_result);
    }
    if (curl_result != 0) {
        result->stage = buffer.overflow ? "url_bag_fetch"
                                         : amdl_curl_failure_stage(curl_result);
        goto cleanup;
    }
    if (curl_easy_getinfo(curl, AMDL_CURLINFO_RESPONSE_CODE, &status) != 0 ||
        status < 200 || status >= 300 || buffer.length == 0) {
        result->stage = "url_bag_fetch";
        goto cleanup;
    }
    result->ok = amdl_bag_has_authenticate_account(
        buffer.data, buffer.length, &result->stage);

cleanup:
    if (curl != NULL) {
        curl_easy_cleanup(curl);
    }
    if (curl_global_ready) {
        curl_global_cleanup();
    }
    if (buffer.data != NULL) {
        memset(buffer.data, 0, AMDL_BAG_MAX_BYTES + 1);
        free(buffer.data);
    }
    return result->ok;
}

int amdl_network_preflight_if_requested(const char *proxy, int *exit_code) {
    int fd = amdl_parse_result_fd();
    if (fd == -1) {
        return 0;
    }
    if (exit_code == NULL) {
        if (fd >= 0) {
            close(fd);
        }
        return 1;
    }
    struct amdl_network_preflight_result result = {0};
    int ok = fd >= 0 && amdl_run_bag_preflight(proxy, &result);
    if (fd >= 0) {
        result.ok = ok;
        if (!amdl_write_result(fd, &result)) {
            ok = 0;
        }
        close(fd);
    }
    *exit_code = ok ? EXIT_SUCCESS : EXIT_FAILURE;
    return 1;
}
