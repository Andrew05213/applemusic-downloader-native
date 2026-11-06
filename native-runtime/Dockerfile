# Source-built toolchain; the driver binds a unique tag to its verified image ID.
# The public inputs and recipe are in toolchain/; no local harness is required.
ARG TOOLCHAIN_IMAGE=scratch
FROM ${TOOLCHAIN_IMAGE} AS guest-builder

ARG SOURCE_DATE_EPOCH=1786320000
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} TZ=UTC LC_ALL=C
WORKDIR /native-build
COPY inputs.lock.json /native-build/

# The source-built toolchain contains the locked kernel/BusyBox packages.
# Compilation and guest assembly need no network access.

COPY lite-native.patch lite-session.patch lite-store-bridge.patch native_login.h native_service.h native_store_bridge.h /native-build/
COPY wrapper.tar.gz /native-build/wrapper.tar.gz
RUN set -eux; \
    echo '6c4a846c2664077c983d252b16e65114cac2d01bc6a632c67f14ea04a1a25041  wrapper.tar.gz' | sha256sum -c -; \
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
