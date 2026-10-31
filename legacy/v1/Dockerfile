# The embedded wrapper only supports linux/amd64. Keep both stages on that
# platform so a build cannot silently produce an unusable ARM image.
ARG UBUNTU_RUNTIME_IMAGE=ubuntu:22.04@sha256:0d779ea97881505f5ef0039336ee85edba27519bdba968c284c86ee066a973c8
ARG UBUNTU_SNAPSHOT=20260728T000000Z
ARG ANDROID_CA_BUNDLE_SHA256=9481fcd95f41b221f02f14d896535fe500bec539bc563c4cdca1acee483a8bdd

FROM --platform=linux/amd64 golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e AS build

ENV GOTOOLCHAIN=local

WORKDIR /src/apple-music-downloader

# Keep dependency resolution cacheable and never mutate the checked-in module
# metadata during an image build.
COPY apple-music-downloader/go.mod apple-music-downloader/go.sum ./
RUN go mod download

COPY apple-music-downloader/ ./
# Test the complete module before compiling the shipped service.  Neither
# command mutates the checked-in module metadata (in particular, no tidy runs
# during an image build).
RUN go test ./... && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o /out/apple-music-downloader .

# Build the Docker Wrapper from the same pinned source and reviewed login-FD
# patch as the WSL Appliance. The legacy repository binary accepts credentials
# through -L argv and therefore must never be advertised as login-FD capable.
FROM --platform=linux/amd64 golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e AS wrapper-build

ARG WRAPPER_COMMIT=0d4823df8097a88fd62dc5f8112dc4a5c733b65a
ARG CJSON_COMMIT=c859b25da02955fef659d658b8f324b5cde87be3
ARG DOBBY_COMMIT=9d271803881f467aabfc24593c7dcaeb710fe861
ARG ANDROID_NDK_VERSION=r23b
ARG ANDROID_NDK_SHA256=c6e97f9c8cfe5b7be0a9e6c15af8e7a179475b7ded23e2d1c1fa0945d6fb4382
ARG CMAKE_VERSION=3.25.3
ARG CMAKE_LINUX_AMD64_SHA256=d4d2ba83301b215857d3b6590cd4434a414fa151c5807693abe587bd6c03581e
ARG MEDIAPLATFORM_BINARY_SHA256=96ec8f0a9bdd597534fb876d1945f153077d688826dcc7673807e113280d5203
ARG STORESERVICESCORE_BINARY_SHA256=6238261363d7943f1a0a27116b52544831f3e295e8533a634641fe5b09d92c7e
ARG ANDROIDAPPMUSIC_BINARY_SHA256=0a575431868e40ddb29e9e0bac7a4993d6e05971b45403cabe358c081b54a36e
ARG URLREQUEST_ALLOCATION_BYTES=0x250
ARG URLREQUEST_OBJECT_OFFSET=0x20
ARG URLREQUEST_OBJECT_BYTES=0x230
ARG URLREQUEST_OBJECT_ALIGNMENT=16
ARG HTTPMESSAGE_ALLOCATION_BYTES=0x1e0
ARG HTTPMESSAGE_OBJECT_OFFSET=0x18
ARG HTTPMESSAGE_OBJECT_BYTES=0x1c8
ARG HTTPMESSAGE_STORAGE_ALIGNMENT=16
ARG HTTPMESSAGE_OBJECT_ALIGNMENT=8
ARG STOREERRORCODE_OBJECT_OFFSET=0x20
ARG STOREERRORCODE_VALUE_BYTES=4
ARG WRAPPER_PATCH_SHA256=fea223dd26f89ac0abe3b1301717bb5d97537719c50ad05cb2ef9a5cdcf2b950
ARG NETWORK_PREFLIGHT_SOURCE_SHA256=44519033aa4b662f21e3f095559190888a7875c44ffc30ebeee56741c680998e
ARG WRAPPER_BUILT_SHA256=380d55a91c6da9ac518ede14b19a7aa390bd32315fb0f02e63aa75a3fca94b17
ARG ANDROID_MAIN_BUILT_SHA256=f1cf33a411d3a33bce0c79c7561670e6095a60366715538fa07d0ed06ee3facb
ARG ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256=77c93c3cec9906b32e7825629bdc3a2bd5392888fdcdabc0c5884352ec28133a

ENV SOURCE_DATE_EPOCH=1786320000
WORKDIR /src

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

COPY appliance/scripts/extract-zip.py /tmp/extract-zip.py
COPY appliance/scripts/verify-urlrequest-abi.sh /tmp/verify-urlrequest-abi.sh
RUN set -eux; \
    curl --fail --location --retry 2 --output /tmp/cmake.tar.gz \
      "https://github.com/Kitware/CMake/releases/download/v${CMAKE_VERSION}/cmake-${CMAKE_VERSION}-linux-x86_64.tar.gz"; \
    printf '%s  %s\n' "${CMAKE_LINUX_AMD64_SHA256}" /tmp/cmake.tar.gz | sha256sum -c -; \
    mkdir -p /opt/cmake; \
    tar -xzf /tmp/cmake.tar.gz --strip-components=1 -C /opt/cmake; \
    curl --fail --location --retry 2 --output /tmp/android-ndk.zip \
      "https://dl.google.com/android/repository/android-ndk-${ANDROID_NDK_VERSION}-linux.zip"; \
    printf '%s  %s\n' "${ANDROID_NDK_SHA256}" /tmp/android-ndk.zip | sha256sum -c -; \
    python3 /tmp/extract-zip.py /tmp/android-ndk.zip /opt; \
    rm -f /tmp/cmake.tar.gz /tmp/android-ndk.zip

COPY appliance/patches/wrapper-login-fd.patch /tmp/wrapper-login-fd.patch
COPY appliance/native/network_preflight.c appliance/native/network_preflight.h appliance/native/network_preflight_main.c /tmp/
COPY appliance/scripts/run-native-abi-harness.sh /tmp/run-native-abi-harness.sh
COPY appliance/scripts/native-abi-fixture-server.py /tmp/native-abi-fixture-server.py
COPY appliance/scripts/run-network-preflight-harness.sh /tmp/run-network-preflight-harness.sh
COPY appliance/scripts/network-preflight-fixture-server.py /tmp/network-preflight-fixture-server.py
COPY appliance/tests/network-preflight-fixtures/ /tmp/network-preflight-fixtures/
RUN set -eux; \
    test "$(sha256sum /tmp/wrapper-login-fd.patch | awk '{print $1}')" = "${WRAPPER_PATCH_SHA256}"; \
    network_preflight_source_sha="$( { printf 'F\0network_preflight.c\0'; cat /tmp/network_preflight.c; printf '\0F\0network_preflight.h\0'; cat /tmp/network_preflight.h; printf '\0F\0network_preflight_main.c\0'; cat /tmp/network_preflight_main.c; } | sha256sum | awk '{print $1}')"; \
    test "$network_preflight_source_sha" = "${NETWORK_PREFLIGHT_SOURCE_SHA256}"; \
    git -C /src/wrapper-source apply --recount --check /tmp/wrapper-login-fd.patch; \
    git -C /src/wrapper-source apply --recount /tmp/wrapper-login-fd.patch; \
    install -m 0644 /tmp/network_preflight.c /src/wrapper-source/network_preflight.c; \
    install -m 0644 /tmp/network_preflight.h /src/wrapper-source/network_preflight.h; \
    install -m 0644 /tmp/network_preflight_main.c /src/wrapper-source/network_preflight_main.c

COPY wrapper-linux/rootfs/ /src/wrapper-source/rootfs/

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
    grep -a -q -- '--login-fd' /src/wrapper-source/wrapper; \
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
    sha256sum /src/wrapper-source/wrapper /src/wrapper-source/rootfs/system/bin/main /src/wrapper-build/amdl_network_preflight; \
    printf '%s  %s\n' "${WRAPPER_BUILT_SHA256}" /src/wrapper-source/wrapper | sha256sum -c -; \
    printf '%s  %s\n' "${ANDROID_MAIN_BUILT_SHA256}" /src/wrapper-source/rootfs/system/bin/main | sha256sum -c -; \
    printf '%s  %s\n' "${ANDROID_NETWORK_PREFLIGHT_BUILT_SHA256}" /src/wrapper-build/amdl_network_preflight | sha256sum -c -; \
    install -D -m 0755 /src/wrapper-source/wrapper /out/wrapper; \
    install -D -m 0755 /src/wrapper-source/rootfs/system/bin/main /out/android-main; \
    install -D -m 0755 /src/wrapper-build/amdl_network_preflight /out/android-network-preflight

RUN --network=none bash /tmp/run-native-abi-harness.sh \
    /src/wrapper-source/rootfs /src/wrapper-build/native_abi_harness /tmp/native-abi-fixture-server.py

RUN --network=none bash /tmp/run-network-preflight-harness.sh \
    /src/wrapper-source/rootfs /src/wrapper-build/amdl_network_preflight \
    /tmp/network-preflight-fixtures /tmp/network-preflight-fixture-server.py

# The Android libcurl default and the explicit native preflight CAINFO both
# resolve this exact in-chroot path. Derive it from the same locked snapshot as
# the Appliance rather than the runtime image's mutable mirror configuration.
FROM --platform=linux/amd64 ${UBUNTU_RUNTIME_IMAGE} AS android-ca-bundle

ARG UBUNTU_SNAPSHOT
ARG ANDROID_CA_BUNDLE_SHA256

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY appliance/packages.resolved.lock /tmp/appliance-locks/packages.resolved.lock
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
    test "$(stat -c '%s' /out/android-ca-bundle.pem)" -le 4194304; \
    printf '%s  %s\n' "${ANDROID_CA_BUNDLE_SHA256}" /out/android-ca-bundle.pem | sha256sum -c -

FROM --platform=linux/amd64 ${UBUNTU_RUNTIME_IMAGE} AS runtime

ARG UBUNTU_MIRROR=http://mirrors.aliyun.com/ubuntu
ARG ANDROID_CA_BUNDLE_SHA256

ENV DEBIAN_FRONTEND=noninteractive \
	AMDL_CONTAINER_MODE=1 \
    AMDL_WRAPPER_SUPPORTS_LOGIN_FD=1 \
    AMDL_CONFIG=/app/working/config.yaml \
    AMDL_DATA_DIR=/app/data \
    AMDL_DOWNLOAD_DIR=/app/downloads

RUN sed -i "s|http://archive.ubuntu.com/ubuntu|${UBUNTU_MIRROR}|g; s|http://security.ubuntu.com/ubuntu|${UBUNTU_MIRROR}|g" /etc/apt/sources.list && \
    apt-get -o Acquire::Retries=3 update && \
    apt-get -o Acquire::Retries=3 install --no-install-recommends -y ca-certificates curl gpac && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/apple-music-downloader /app/downloader
COPY apple-music-downloader/config.example.yaml /app/working/config.yaml
COPY wrapper-linux/rootfs /app/rootfs
COPY --from=wrapper-build /out/wrapper /app/wrapper-linux/wrapper
COPY --from=wrapper-build /out/android-main /app/rootfs/system/bin/main
COPY --from=wrapper-build /out/android-network-preflight /app/rootfs/system/bin/amdl-network-preflight
COPY --from=android-ca-bundle /out/android-ca-bundle.pem /tmp/amdl-android-ca-bundle.pem
COPY src/ /app/src/
COPY start.sh /app/start.sh

RUN mkdir -p /app/data /app/downloads /app/working /app/rootfs/data && \
    install -D -o root -g root -m 0644 /tmp/amdl-android-ca-bundle.pem /app/rootfs/usr/local/android/cert.pem && \
    test ! -L /app/rootfs/usr/local/android/cert.pem && \
    test "$(stat -c '%u:%g:%a' /app/rootfs/usr/local/android/cert.pem)" = '0:0:644' && \
    test -s /app/rootfs/usr/local/android/cert.pem && \
    test "$(stat -c '%s' /app/rootfs/usr/local/android/cert.pem)" -le 4194304 && \
    printf '%s  %s\n' "${ANDROID_CA_BUNDLE_SHA256}" /app/rootfs/usr/local/android/cert.pem | sha256sum -c - && \
    rm -f /tmp/amdl-android-ca-bundle.pem && \
    chmod 0755 /app/start.sh /app/wrapper-linux/wrapper

# Docker users only need the Web UI. Wrapper ports remain available inside the
# container but are deliberately not published to the host.
EXPOSE 8080
VOLUME ["/app/data", "/app/downloads", "/app/rootfs/data"]

HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=12 \
    CMD curl --fail --silent --show-error http://127.0.0.1:8080/api/v1/health/ready >/dev/null || exit 1

CMD ["/app/start.sh"]
