# Private build-only toolchain. The Node driver verifies this local image's
# full immutable ID before and after build; it is never part of the runtime.
ARG TOOLCHAIN_IMAGE=applemusic-downloader-v1215-wrapper-harness:retry
FROM ${TOOLCHAIN_IMAGE} AS guest-builder

ARG SOURCE_DATE_EPOCH=1786320000
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} TZ=UTC LC_ALL=C
WORKDIR /native-build
COPY inputs.lock.json /native-build/

RUN set -eux; \
    printf 'deb https://snapshot.debian.org/archive/debian/20260728T000000Z/ bookworm main\ndeb https://snapshot.debian.org/archive/debian-security/20260728T000000Z/ bookworm-security main\n' > /etc/apt/sources.list; \
    find /etc/apt/sources.list.d -type f -delete; \
    apt-get -o Acquire::Check-Valid-Until=false update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      linux-image-6.1.0-51-amd64=6.1.177-1 \
      busybox-static=1:1.35.0-4+deb12u1+b1 \
      cpio=2.13+dfsg-7.1 kmod=30+20221128-1 ca-certificates=20230311+deb12u1; \
    rm -rf /var/lib/apt/lists/*

COPY lite-native.patch lite-session.patch lite-store-bridge.patch native_login.h native_service.h native_store_bridge.h /native-build/
COPY wrapper.tar.gz /native-build/wrapper.tar.gz
RUN set -eux; \
    echo 'cc9fca1a70692ee7feb73c1aa74805cd6749d29200dad34ddbab3d17467464e8  wrapper.tar.gz' | sha256sum -c -; \
    test "$(git -C /src/cjson rev-parse HEAD)" = c859b25da02955fef659d658b8f324b5cde87be3; \
    test "$(git -C /src/dobby rev-parse HEAD)" = 9d271803881f467aabfc24593c7dcaeb710fe861; \
    grep -q 'Pkg.Revision = 23.1.7779620' /opt/android-ndk-r23b/source.properties; \
    /opt/cmake/bin/cmake --version | head -1 | grep -Fx 'cmake version 3.25.3'; \
    git -C /src/cjson diff --quiet; git -C /src/dobby diff --quiet; \
    mkdir -p /native-build/source; \
    tar -xzf wrapper.tar.gz --strip-components=1 -C /native-build/source \
      --exclude='*/rootfs/data' --exclude='*/rootfs/dev' --exclude='*/rootfs/proc'; \
    cd /native-build/source; \
    git apply --recount --check /native-build/lite-native.patch; \
    git apply --recount /native-build/lite-native.patch; \
    git apply --recount --unidiff-zero --check /native-build/lite-session.patch; \
    git apply --recount --unidiff-zero /native-build/lite-session.patch; \
    git apply --recount --unidiff-zero --check /native-build/lite-store-bridge.patch; \
    git apply --recount --unidiff-zero /native-build/lite-store-bridge.patch; \
    cp /native-build/native_login.h /native-build/native_service.h /native-build/native_store_bridge.h lite/; \
    ! grep -R 'curl_easy_setopt_hook\|g_ssl_verify_disabled' lite --exclude=httplib.h; \
    ! grep -E 'setOptInt\(CURLOPT_SSL_(VERIFYPEER|VERIFYHOST), 0\)' lite/apple_api.cpp; \
    /opt/cmake/bin/cmake -S . -B /native-build/compiled \
      -DANDROID_NDK_PATH=/opt/android-ndk-r23b \
      -DFETCHCONTENT_SOURCE_DIR_CJSON=/src/cjson \
      -DFETCHCONTENT_SOURCE_DIR_DOBBY=/src/dobby \
      -DFETCHCONTENT_FULLY_DISCONNECTED=ON -DBUILD_HOST_LAUNCHERS=OFF \
      -DCMAKE_BUILD_TYPE=Release \
      '-DCMAKE_C_FLAGS=-ffile-prefix-map=/native-build=. -fdebug-prefix-map=/native-build=.' \
      '-DCMAKE_CXX_FLAGS=-ffile-prefix-map=/native-build=. -fdebug-prefix-map=/native-build=.' \
      '-DCMAKE_EXE_LINKER_FLAGS=-Wl,--build-id=none'; \
    /opt/cmake/bin/cmake --build /native-build/compiled --target lite --parallel 2

COPY build-guest.sh init amdl-guest guest-agent.lock.json guest-agent-source.json /native-build/
# Force only guest assembly on repeat builds; do not claim cache hits prove
# reproducibility. Source compilation remains independently content-addressed.
ARG GUEST_REPRO_RUN=first
RUN --network=none test -n "$GUEST_REPRO_RUN" && /bin/bash /native-build/build-guest.sh

FROM scratch AS runtime-export
COPY --from=guest-builder /out/ /
