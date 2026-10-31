# Both defaults are immutable linux/amd64 manifests. The build driver passes
# the same locked references explicitly and records them in provenance.
ARG GO_BUILDER_IMAGE=golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e
ARG UBUNTU_RUNTIME_IMAGE=ubuntu:22.04@sha256:0d779ea97881505f5ef0039336ee85edba27519bdba968c284c86ee066a973c8
FROM --platform=linux/amd64 ${GO_BUILDER_IMAGE} AS build-base

ARG APPLIANCE_VERSION=1.4.3
ARG SOURCE_DATE_EPOCH=1786320000
ENV CGO_ENABLED=0 \
    GOARCH=amd64 \
    GOOS=linux \
    GOTOOLCHAIN=local \
    SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH}

WORKDIR /src

FROM build-base AS go-builder

ARG APPLIANCE_VERSION
ARG APPLIANCE_BUILD_ID
ARG ANDROID_MAIN_BUILT_SHA256
ARG ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256
ARG ANDROID_CA_BUNDLE_PATH
ARG ANDROID_CA_BUNDLE_SHA256
ARG RENDERER_SHA256

COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN set -eux; \
    test "$(printf '%s' "${APPLIANCE_BUILD_ID}" | wc -c)" -eq 64; \
    test "$(printf '%s' "${ANDROID_MAIN_BUILT_SHA256}" | wc -c)" -eq 64; \
    test "$(printf '%s' "${ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256}" | wc -c)" -eq 64; \
    test "${ANDROID_CA_BUNDLE_PATH}" = '/usr/local/android/cert.pem'; \
    test "$(printf '%s' "${ANDROID_CA_BUNDLE_SHA256}" | wc -c)" -eq 64; \
    test "$(printf '%s' "${RENDERER_SHA256}" | wc -c)" -eq 64; \
    go test ./...; \
    identity_ldflags="-s -w -buildid= -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.Version=${APPLIANCE_VERSION} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.ApplianceBuildID=${APPLIANCE_BUILD_ID} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.AndroidMainBuiltSHA256=${ANDROID_MAIN_BUILT_SHA256} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.AndroidNetworkPreflightBuiltSHA256=${ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.AndroidCABundlePath=${ANDROID_CA_BUNDLE_PATH} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.AndroidCABundleSHA256=${ANDROID_CA_BUNDLE_SHA256} -X github.com/Andrew05213/applemusic_downloader/internal/buildinfo.RendererSHA256=${RENDERER_SHA256}"; \
    go build -buildvcs=false -trimpath -ldflags="$identity_ldflags" -o /out/apple-music-downloader .; \
    go build -buildvcs=false -trimpath -ldflags="$identity_ldflags" -o /out/amdl-appliance ./cmd/amdl-appliance

FROM build-base AS wrapper-compiled

ARG WRAPPER_COMMIT=0d4823df8097a88fd62dc5f8112dc4a5c733b65a
ARG CJSON_COMMIT=c859b25da02955fef659d658b8f324b5cde87be3
ARG DOBBY_COMMIT=9d271803881f467aabfc24593c7dcaeb710fe861
ARG ANDROID_NDK_SHA256=c6e97f9c8cfe5b7be0a9e6c15af8e7a179475b7ded23e2d1c1fa0945d6fb4382
ARG ANDROID_NDK_VERSION=r23b
ARG CMAKE_VERSION=3.25.3
ARG CMAKE_LINUX_AMD64_SHA256=d4d2ba83301b215857d3b6590cd4434a414fa151c5807693abe587bd6c03581e
ARG MEDIAPLATFORM_BINARY_SHA256
ARG STORESERVICESCORE_BINARY_SHA256
ARG ANDROIDAPPMUSIC_BINARY_SHA256
ARG URLREQUEST_ALLOCATION_BYTES
ARG URLREQUEST_OBJECT_OFFSET
ARG URLREQUEST_OBJECT_BYTES
ARG URLREQUEST_OBJECT_ALIGNMENT
ARG HTTPMESSAGE_ALLOCATION_BYTES
ARG HTTPMESSAGE_OBJECT_OFFSET
ARG HTTPMESSAGE_OBJECT_BYTES
ARG HTTPMESSAGE_STORAGE_ALIGNMENT
ARG HTTPMESSAGE_OBJECT_ALIGNMENT
ARG STOREERRORCODE_OBJECT_OFFSET
ARG STOREERRORCODE_VALUE_BYTES
ARG WRAPPER_PATCH_SHA256
ARG NETWORK_PREFLIGHT_SOURCE_SHA256

ENV SOURCE_DATE_EPOCH=1786320000

RUN set -eux; \
    git init /src/wrapper-source; \
    git -C /src/wrapper-source remote add origin https://github.com/WorldObservationLog/wrapper.git; \
    git -C /src/wrapper-source fetch --depth=1 origin "${WRAPPER_COMMIT}"; \
    test "$(git -C /src/wrapper-source rev-parse FETCH_HEAD)" = "${WRAPPER_COMMIT}"; \
    git -C /src/wrapper-source checkout --detach FETCH_HEAD; \
    git init /src/cjson; \
    git -C /src/cjson remote add origin https://github.com/DaveGamble/cJSON.git; \
    git -C /src/cjson fetch --depth=1 origin "${CJSON_COMMIT}"; \
    test "$(git -C /src/cjson rev-parse FETCH_HEAD)" = "${CJSON_COMMIT}"; \
    git -C /src/cjson checkout --detach FETCH_HEAD; \
    git init /src/dobby; \
    git -C /src/dobby remote add origin https://github.com/BepInEx/Dobby.git; \
    git -C /src/dobby fetch --depth=1 origin "${DOBBY_COMMIT}"; \
    test "$(git -C /src/dobby rev-parse FETCH_HEAD)" = "${DOBBY_COMMIT}"; \
    git -C /src/dobby checkout --detach FETCH_HEAD; \
    git -C /src/dobby submodule update --init --recursive --depth=1

COPY extract-zip.py /tmp/extract-zip.py
COPY verify-urlrequest-abi.sh /tmp/verify-urlrequest-abi.sh
RUN set -eux; \
    curl --fail --location --retry 2 \
      --output /tmp/cmake.tar.gz \
      "https://github.com/Kitware/CMake/releases/download/v${CMAKE_VERSION}/cmake-${CMAKE_VERSION}-linux-x86_64.tar.gz"; \
    printf '%s  %s\n' "${CMAKE_LINUX_AMD64_SHA256}" /tmp/cmake.tar.gz | sha256sum -c -; \
    mkdir -p /opt/cmake; \
    tar -xzf /tmp/cmake.tar.gz --strip-components=1 -C /opt/cmake; \
    curl --fail --location --retry 2 \
      --output /tmp/android-ndk.zip \
      "https://dl.google.com/android/repository/android-ndk-${ANDROID_NDK_VERSION}-linux.zip"; \
    printf '%s  %s\n' "${ANDROID_NDK_SHA256}" /tmp/android-ndk.zip | sha256sum -c -; \
    python3 /tmp/extract-zip.py /tmp/android-ndk.zip /opt; \
    rm -f /tmp/cmake.tar.gz /tmp/android-ndk.zip

COPY wrapper-login-fd.patch /tmp/wrapper-login-fd.patch
COPY network_preflight.c network_preflight.h network_preflight_main.c /tmp/
COPY run-native-abi-harness.sh /tmp/run-native-abi-harness.sh
COPY native-abi-fixture-server.py /tmp/native-abi-fixture-server.py
COPY run-network-preflight-harness.sh /tmp/run-network-preflight-harness.sh
COPY network-preflight-fixture-server.py /tmp/network-preflight-fixture-server.py
COPY network-preflight-fixtures/ /tmp/network-preflight-fixtures/
RUN set -eux; \
    test "$(sha256sum /tmp/wrapper-login-fd.patch | awk '{print $1}')" = "${WRAPPER_PATCH_SHA256}"; \
    network_preflight_source_sha="$( { printf 'F\0network_preflight.c\0'; cat /tmp/network_preflight.c; printf '\0F\0network_preflight.h\0'; cat /tmp/network_preflight.h; printf '\0F\0network_preflight_main.c\0'; cat /tmp/network_preflight_main.c; } | sha256sum | awk '{print $1}')"; \
    test "$network_preflight_source_sha" = "${NETWORK_PREFLIGHT_SOURCE_SHA256}"; \
    git -C /src/wrapper-source apply --recount --check /tmp/wrapper-login-fd.patch; \
    git -C /src/wrapper-source apply --recount /tmp/wrapper-login-fd.patch; \
    install -m 0644 /tmp/network_preflight.c /src/wrapper-source/network_preflight.c; \
    install -m 0644 /tmp/network_preflight.h /src/wrapper-source/network_preflight.h; \
    install -m 0644 /tmp/network_preflight_main.c /src/wrapper-source/network_preflight_main.c; \
    grep -Fq 'detach_stale_mounts(ROOTFS_PROC_TARGET)' /src/wrapper-source/wrapper.c; \
    grep -Fq 'detach_stale_mounts(ROOTFS_URANDOM_TARGET)' /src/wrapper-source/wrapper.c; \
    grep -Fq 'wait_for_child(fork_result, &child_status)' /src/wrapper-source/wrapper.c; \
    grep -Fq 'cleanup_mounts(proc_mount_attempted, urandom_mounted, rootfs_cwd, chrooted)' /src/wrapper-source/wrapper.c

# The reviewed local Android rootfs supplies only the proprietary link/runtime
# inputs. Its Wrapper and Android main binaries are overwritten by the
# source-matched, patched outputs below.
COPY wrapper/rootfs/ /src/wrapper-source/rootfs/

RUN set -eux; \
	data_library=/src/wrapper-source/rootfs/system/lib64/libmediaplatform.so; \
	storeservices_library=/src/wrapper-source/rootfs/system/lib64/libstoreservicescore.so; \
	androidappmusic_library=/src/wrapper-source/rootfs/system/lib64/libandroidappmusic.so; \
	bash /tmp/verify-urlrequest-abi.sh "$storeservices_library" \
	  "${STORESERVICESCORE_BINARY_SHA256}" "$data_library" "${MEDIAPLATFORM_BINARY_SHA256}" "$androidappmusic_library" \
	  "${ANDROIDAPPMUSIC_BINARY_SHA256}" \
	  "${URLREQUEST_ALLOCATION_BYTES}" \
	  "${URLREQUEST_OBJECT_OFFSET}" "${URLREQUEST_OBJECT_BYTES}" \
	  "${URLREQUEST_OBJECT_ALIGNMENT}" \
	  "${HTTPMESSAGE_ALLOCATION_BYTES}" "${HTTPMESSAGE_OBJECT_OFFSET}" \
	  "${HTTPMESSAGE_OBJECT_BYTES}" "${HTTPMESSAGE_STORAGE_ALIGNMENT}" \
	  "${HTTPMESSAGE_OBJECT_ALIGNMENT}" "${STOREERRORCODE_OBJECT_OFFSET}" \
	  "${STOREERRORCODE_VALUE_BYTES}"; \
	printf '%s  %s\n' "${MEDIAPLATFORM_BINARY_SHA256}" "$data_library" | sha256sum -c -; \
	data_length_record="$(nm -D -S --defined-only "$data_library" | awk '$4 == "_ZNK13mediaplatform4Data6lengthEv" { print $1 " " $2 }')"; \
	test "$(printf '%s\n' "$data_length_record" | wc -w)" = 2; \
	set -- $data_length_record; data_length_symbol="$1"; data_length_size="$2"; \
	test "$(printf '%s\n' "$data_length_symbol" | grep -Ec '^[0-9a-fA-F]+$')" = 1; \
	test "$data_length_size" = 0000000000000028; \
	data_length_start=$((0x${data_length_symbol})); \
	data_length_stop=$((data_length_start + 0x${data_length_size})); \
	objdump -d --start-address="$data_length_start" --stop-address="$data_length_stop" "$data_library" > /tmp/data-length.disassembly; \
	test "$(grep -Ec 'mov[[:space:]]+0x8\(%rdi\),%rax' /tmp/data-length.disassembly)" = 1; \
	test "$(grep -Ec '[[:space:]]ret([[:space:]]|$)' /tmp/data-length.disassembly)" = 1; \
	rm -f /tmp/data-length.disassembly; \
    /opt/cmake/bin/cmake \
      -S /src/wrapper-source \
      -B /src/wrapper-build \
      -DANDROID_NDK_PATH="/opt/android-ndk-${ANDROID_NDK_VERSION}" \
      -DFETCHCONTENT_SOURCE_DIR_CJSON=/src/cjson \
      -DFETCHCONTENT_SOURCE_DIR_DOBBY=/src/dobby \
      -DCMAKE_BUILD_TYPE=Release \
      '-DCMAKE_C_FLAGS=-ffile-prefix-map=/src=. -fdebug-prefix-map=/src=.' \
      '-DCMAKE_CXX_FLAGS=-ffile-prefix-map=/src=. -fdebug-prefix-map=/src=.' \
      '-DCMAKE_EXE_LINKER_FLAGS=-Wl,--build-id=none'; \
    /opt/cmake/bin/cmake --build /src/wrapper-build --target main wrapper_exe amdl_network_preflight --parallel 2; \
    /opt/cmake/bin/cmake --build /src/wrapper-build --target native_abi_harness --parallel 2; \
    test -x /src/wrapper-build/native_abi_harness; \
    test -x /src/wrapper-source/wrapper; \
    test -x /src/wrapper-source/rootfs/system/bin/main; \
    test -x /src/wrapper-build/amdl_network_preflight; \
    grep -a -q -- '--login-fd' /src/wrapper-source/wrapper; \
    grep -a -q -- 'too many stale mount layers' /src/wrapper-source/wrapper; \
    grep -a -q -- 'notify proc mount attempt' /src/wrapper-source/wrapper; \
    grep -a -q -- 'AMDL_WRAPPER_LOGIN_FD' /src/wrapper-source/rootfs/system/bin/main; \
    grep -a -q -- 'AMDL_NETWORK_RESULT_FD' /src/wrapper-source/wrapper; \
    ! grep -a -q -- 'authenticateAccount' /src/wrapper-source/rootfs/system/bin/main; \
    grep -a -q -- 'authenticateAccount' /src/wrapper-build/amdl_network_preflight; \
    ! grep -a -q -- 'curl_easy_setopt_hook' /src/wrapper-source/rootfs/system/bin/main; \
    ! grep -a -q -- 'install_hooks' /src/wrapper-source/rootfs/system/bin/main; \
    curl_library="$(find /src/wrapper-source/rootfs/system/lib64 -maxdepth 1 -type f -name 'libcurl.so' -print -quit)"; \
    test -n "$curl_library"; \
    tr '\000' '\n' < "$curl_library" | grep -Fx '/usr/local/android/cert.pem'; \
    grep -a -q -- 'authentication_succeeded' /src/wrapper-source/rootfs/system/bin/main; \
    grep -a -q -- 'dev_token complete' /src/wrapper-source/rootfs/system/bin/main; \
    mkdir -p /out; \
    wrapper_built_sha256="$(sha256sum /src/wrapper-source/wrapper | awk '{print $1}')"; \
    android_main_built_sha256="$(sha256sum /src/wrapper-source/rootfs/system/bin/main | awk '{print $1}')"; \
    android_network_preflight_built_sha256="$(sha256sum /src/wrapper-build/amdl_network_preflight | awk '{print $1}')"; \
    printf 'WRAPPER_BUILT_SHA256=%s\nANDROID_MAIN_BUILT_SHA256=%s\nANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=%s\nWRAPPER_BUILT_FROM_PATCH_SHA256=%s\n' \
      "$wrapper_built_sha256" "$android_main_built_sha256" "$android_network_preflight_built_sha256" "${WRAPPER_PATCH_SHA256}" \
      > /out/native-pins.env; \
    install -D -m 0755 /src/wrapper-source/wrapper /out/wrapper; \
    install -D -m 0755 /src/wrapper-source/rootfs/system/bin/main /out/android-main; \
    install -D -m 0755 /src/wrapper-build/amdl_network_preflight /out/android-network-preflight

RUN --network=none bash /tmp/run-native-abi-harness.sh \
    /src/wrapper-source/rootfs /src/wrapper-build/native_abi_harness /tmp/native-abi-fixture-server.py

# This invokes the standalone Bionic network probe inside a throwaway chroot. It does
# not receive credentials or a real endpoint; the only network path is a
# fixed loopback CONNECT/TLS fixture in Docker's network=none namespace.
RUN --network=none bash /tmp/run-network-preflight-harness.sh \
    /src/wrapper-source/rootfs /src/wrapper-build/amdl_network_preflight \
    /tmp/network-preflight-fixtures /tmp/network-preflight-fixture-server.py

# This is the only stage the runtime may consume. The preceding stage is an
# intentionally non-deliverable measurement boundary: it can expose the three
# freshly compiled digests, but cannot flow into the runtime until these locked
# expectations agree byte-for-byte.
FROM wrapper-compiled AS wrapper-builder

ARG WRAPPER_BUILT_SHA256=380d55a91c6da9ac518ede14b19a7aa390bd32315fb0f02e63aa75a3fca94b17
ARG ANDROID_MAIN_BUILT_SHA256=f1cf33a411d3a33bce0c79c7561670e6095a60366715538fa07d0ed06ee3facb
ARG ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=77c93c3cec9906b32e7825629bdc3a2bd5392888fdcdabc0c5884352ec28133a

RUN set -eux; \
    printf '%s  %s\n' "${WRAPPER_BUILT_SHA256}" /out/wrapper | sha256sum -c -; \
    printf '%s  %s\n' "${ANDROID_MAIN_BUILT_SHA256}" /out/android-main | sha256sum -c -; \
    printf '%s  %s\n' "${ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256}" /out/android-network-preflight | sha256sum -c -

# This stage is a non-deliverable measurement boundary. It derives only the
# CA bundle from the exact ca-certificates package in the locked Ubuntu
# snapshot, so the pin can be measured independently from the release image.
FROM --platform=linux/amd64 ${UBUNTU_RUNTIME_IMAGE} AS android-ca-bundle-compiled

ARG UBUNTU_SNAPSHOT=20260728T000000Z
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY packages.lock packages.resolved.lock /tmp/appliance-locks/
RUN set -eux; \
    ca_package_version="$(awk -F= '$1 == "ca-certificates" { print $2 }' /tmp/appliance-locks/packages.resolved.lock)"; \
    test -n "$ca_package_version"; \
    test "$(printf '%s\n' "$ca_package_version" | wc -l)" -eq 1; \
    printf '%s\n' \
      'Acquire::Check-Valid-Until "false";' \
      'Acquire::Retries "3";' \
      > /etc/apt/apt.conf.d/99amdl-snapshot; \
    printf '%s\n' \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy main restricted universe multiverse" \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy-updates main restricted universe multiverse" \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy-security main restricted universe multiverse" \
      > /etc/apt/sources.list; \
    rm -f /etc/apt/sources.list.d/*; \
    apt-get update; \
    apt-get install -y --no-install-recommends "ca-certificates=${ca_package_version}"; \
    test "$(dpkg-query -W -f='${Version}' ca-certificates)" = "$ca_package_version"; \
    install -D -o root -g root -m 0644 /etc/ssl/certs/ca-certificates.crt /out/android-ca-bundle.pem; \
    test ! -L /out/android-ca-bundle.pem; \
    test "$(stat -c '%u:%g:%a' /out/android-ca-bundle.pem)" = '0:0:644'; \
    test -s /out/android-ca-bundle.pem; \
    test "$(stat -c '%s' /out/android-ca-bundle.pem)" -le 4194304

FROM android-ca-bundle-compiled AS android-ca-bundle

ARG ANDROID_CA_BUNDLE_SHA256
RUN set -eux; \
    test "$(printf '%s' "${ANDROID_CA_BUNDLE_SHA256}" | wc -c)" -eq 64; \
    printf '%s  %s\n' "${ANDROID_CA_BUNDLE_SHA256}" /out/android-ca-bundle.pem | sha256sum -c -

FROM --platform=linux/amd64 ${UBUNTU_RUNTIME_IMAGE} AS runtime

ARG APPLIANCE_VERSION=1.4.3
ARG SOURCE_DATE_EPOCH=1786320000
ARG UBUNTU_SNAPSHOT=20260728T000000Z
ARG BACKEND_TREE_SHA256
ARG WEB_UI_TREE_SHA256
ARG WRAPPER_TREE_SHA256
ARG WRAPPER_BINARY_SHA256
ARG ANDROID_MAIN_SHA256
ARG INPUT_LOCK_SHA256
ARG NOTICE_SHA256
ARG WRAPPER_COMMIT=0d4823df8097a88fd62dc5f8112dc4a5c733b65a
ARG CJSON_COMMIT=c859b25da02955fef659d658b8f324b5cde87be3
ARG DOBBY_COMMIT=9d271803881f467aabfc24593c7dcaeb710fe861
ARG ANDROID_NDK_SHA256=c6e97f9c8cfe5b7be0a9e6c15af8e7a179475b7ded23e2d1c1fa0945d6fb4382
ARG WRAPPER_PATCH_SHA256
ARG WRAPPER_BUILT_SHA256=380d55a91c6da9ac518ede14b19a7aa390bd32315fb0f02e63aa75a3fca94b17
ARG ANDROID_MAIN_BUILT_SHA256=f1cf33a411d3a33bce0c79c7561670e6095a60366715538fa07d0ed06ee3facb
ARG ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=77c93c3cec9906b32e7825629bdc3a2bd5392888fdcdabc0c5884352ec28133a
ARG ANDROID_CA_BUNDLE_PATH=/usr/local/android/cert.pem
ARG ANDROID_CA_BUNDLE_SHA256
ARG BUILD_METADATA_SHA256
ARG VERSIONS_LOCK_SHA256
ARG PACKAGES_LOCK_SHA256
ARG PACKAGES_RESOLVED_LOCK_SHA256
ARG RELEASE_POLICY_SHA256

ENV DEBIAN_FRONTEND=noninteractive \
    AMDL_RUNTIME=wsl-appliance \
    AMDL_RELEASE_ROOT=/opt/applemusic-downloader/current \
    AMDL_DATA_DIR=/var/lib/applemusic-downloader/data \
    AMDL_DOWNLOAD_DIR=/srv/applemusic-downloader/downloads \
    AMDL_WRAPPER_STATE_DIR=/var/lib/applemusic-downloader/wrapper-state \
    AMDL_RUN_DIR=/run/applemusic-downloader

# The minimal Ubuntu rootfs has no CA bundle. Bootstrap HTTPS trust from the
# already digest-pinned builder, then replace it with the locked Ubuntu package.
COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY packages.lock packages.resolved.lock /tmp/appliance-locks/
RUN printf '%s\n' \
      'Acquire::Check-Valid-Until "false";' \
      'Acquire::Retries "3";' \
      > /etc/apt/apt.conf.d/99amdl-snapshot && \
    printf '%s\n' \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy main restricted universe multiverse" \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy-updates main restricted universe multiverse" \
      "deb https://snapshot.ubuntu.com/ubuntu/${UBUNTU_SNAPSHOT}/ jammy-security main restricted universe multiverse" \
      > /etc/apt/sources.list && \
    rm -f /etc/apt/sources.list.d/* && \
    apt-get update && \
    sed -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' /tmp/appliance-locks/packages.lock > /tmp/direct-packages && \
    xargs -r apt-get install -y --no-install-recommends < /tmp/direct-packages && \
    dpkg-query -W -f='${Package}=${Version}\n' | LC_ALL=C sort > /tmp/packages.actual && \
    sed -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' /tmp/appliance-locks/packages.resolved.lock | LC_ALL=C sort > /tmp/packages.expected && \
    diff -u /tmp/packages.expected /tmp/packages.actual && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/* /var/cache/apt/* /tmp/direct-packages /tmp/packages.actual /tmp/packages.expected

COPY rootfs-overlay/ /

ARG RELEASE_ROOT=/opt/applemusic-downloader/releases/${APPLIANCE_VERSION}
COPY --from=go-builder /out/apple-music-downloader ${RELEASE_ROOT}/apple-music-downloader
COPY --from=go-builder /out/amdl-appliance ${RELEASE_ROOT}/amdl-appliance
COPY backend/config.yaml ${RELEASE_ROOT}/config.yaml
COPY web/renderer/ ${RELEASE_ROOT}/web/
COPY wrapper/ ${RELEASE_ROOT}/wrapper-linux/
COPY --from=wrapper-builder /out/wrapper /tmp/amdl-wrapper
COPY --from=wrapper-builder /out/android-main /tmp/amdl-android-main
COPY --from=wrapper-builder /out/android-network-preflight /tmp/amdl-android-network-preflight
COPY --from=android-ca-bundle /out/android-ca-bundle.pem /tmp/amdl-android-ca-bundle.pem
COPY NOTICE ${RELEASE_ROOT}/NOTICE
COPY release-policy.json ${RELEASE_ROOT}/RELEASE-POLICY.json
COPY release-policy.json /usr/local/share/applemusic-downloader/release-policy.json
COPY APPLIANCE-METADATA.json ${RELEASE_ROOT}/APPLIANCE-METADATA.json

RUN mv "${RELEASE_ROOT}/wrapper-linux/rootfs" "${RELEASE_ROOT}/rootfs" && \
    mv /opt/applemusic-downloader/amdl-appliance-keepalive "${RELEASE_ROOT}/amdl-appliance-keepalive" && \
    install -m 0755 /tmp/amdl-wrapper "${RELEASE_ROOT}/wrapper-linux/wrapper" && \
    install -m 0755 /tmp/amdl-android-main "${RELEASE_ROOT}/rootfs/system/bin/main" && \
    install -m 0755 /tmp/amdl-android-network-preflight "${RELEASE_ROOT}/rootfs/system/bin/amdl-network-preflight" && \
    printf '%s  %s\n' "${ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256}" "${RELEASE_ROOT}/rootfs/system/bin/amdl-network-preflight" | sha256sum -c - && \
    test "${ANDROID_CA_BUNDLE_PATH}" = '/usr/local/android/cert.pem' && \
    install -D -o root -g root -m 0644 /tmp/amdl-android-ca-bundle.pem "${RELEASE_ROOT}/rootfs${ANDROID_CA_BUNDLE_PATH}" && \
    rm -f /tmp/amdl-wrapper /tmp/amdl-android-main /tmp/amdl-android-network-preflight /tmp/amdl-android-ca-bundle.pem && \
    find "${RELEASE_ROOT}" -type d -exec chmod 0755 '{}' + && \
    find "${RELEASE_ROOT}" -type f -exec chmod 0644 '{}' + && \
    chmod 0755 "${RELEASE_ROOT}/apple-music-downloader" "${RELEASE_ROOT}/amdl-appliance" \
      "${RELEASE_ROOT}/amdl-appliance-keepalive" "${RELEASE_ROOT}/wrapper-linux/wrapper" && \
    chmod 0755 "${RELEASE_ROOT}/rootfs/system/bin/main" "${RELEASE_ROOT}/rootfs/system/bin/amdl-network-preflight" && \
    ca_bundle="${RELEASE_ROOT}/rootfs${ANDROID_CA_BUNDLE_PATH}" && \
    test -f "$ca_bundle" && test ! -L "$ca_bundle" && \
    test "$(stat -c '%u:%g:%a' "$ca_bundle")" = '0:0:644' && \
    test -s "$ca_bundle" && test "$(stat -c '%s' "$ca_bundle")" -le 4194304 && \
    printf '%s  %s\n' "${ANDROID_CA_BUNDLE_SHA256}" "$ca_bundle" | sha256sum -c - && \
    chmod 0644 /etc/wsl.conf /etc/systemd/system/amdl-appliance.service \
      /etc/systemd/journald.conf.d/applemusic-downloader.conf \
      /usr/lib/tmpfiles.d/applemusic-downloader.conf && \
    mkdir -p /opt/applemusic-downloader/releases \
      /var/lib/applemusic-downloader/data \
      /var/lib/applemusic-downloader/wrapper-state \
      /run/applemusic-downloader \
      /srv/applemusic-downloader/downloads \
      /etc/systemd/system/multi-user.target.wants /usr/local/bin && \
    ln -sfn "releases/${APPLIANCE_VERSION}" /opt/applemusic-downloader/current && \
    ln -sfn /opt/applemusic-downloader/current/amdl-appliance /usr/local/bin/amdl-appliance-control && \
    ln -sfn /init /usr/bin/wslpath && \
    ln -sfn ../amdl-appliance.service /etc/systemd/system/multi-user.target.wants/amdl-appliance.service && \
    rm -rf "${RELEASE_ROOT}/rootfs/data" && \
    mkdir -p "${RELEASE_ROOT}/rootfs/data" && \
    : > /etc/machine-id && \
    rm -f /var/lib/dbus/machine-id && \
    ln -s /etc/machine-id /var/lib/dbus/machine-id && \
    rm -rf /var/log/* /var/cache/* /tmp/* /var/tmp/* /run/* && \
    mkdir -p /run/applemusic-downloader && \
    chmod 0700 /var/lib/applemusic-downloader /var/lib/applemusic-downloader/data \
      /var/lib/applemusic-downloader/wrapper-state /run/applemusic-downloader \
      /srv/applemusic-downloader/downloads

LABEL org.opencontainers.image.title="Apple Music Downloader WSL2 Appliance" \
      org.opencontainers.image.version="${APPLIANCE_VERSION}" \
      org.opencontainers.image.architecture="amd64" \
      org.opencontainers.image.source="local-locked-inputs" \
      io.applemusic-downloader.public-release-allowed="false" \
      io.applemusic-downloader.input-lock-sha256="${INPUT_LOCK_SHA256}" \
      io.applemusic-downloader.versions-lock-sha256="${VERSIONS_LOCK_SHA256}" \
      io.applemusic-downloader.packages-lock-sha256="${PACKAGES_LOCK_SHA256}" \
      io.applemusic-downloader.packages-resolved-lock-sha256="${PACKAGES_RESOLVED_LOCK_SHA256}" \
      io.applemusic-downloader.release-policy-sha256="${RELEASE_POLICY_SHA256}" \
      io.applemusic-downloader.wrapper-patch-sha256="${WRAPPER_PATCH_SHA256}" \
      io.applemusic-downloader.metadata-sha256="${BUILD_METADATA_SHA256}"

STOPSIGNAL SIGTERM
CMD ["/opt/applemusic-downloader/current/amdl-appliance", "supervisor"]
