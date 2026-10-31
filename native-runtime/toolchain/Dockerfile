# Public, source-built tools only. No Apple runtime, accounts or old harness.
FROM docker.io/library/golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e
ARG SOURCE_DATE_EPOCH=1786320000
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} TZ=UTC LC_ALL=C
RUN set -eux; \
    printf 'deb https://snapshot.debian.org/archive/debian/20260728T000000Z/ bookworm main\ndeb https://snapshot.debian.org/archive/debian-security/20260728T000000Z/ bookworm-security main\n' > /etc/apt/sources.list; \
    find /etc/apt/sources.list.d -type f -delete; \
    apt-get -o Acquire::Check-Valid-Until=false -o APT::Update::Error-Mode=any -o Acquire::Retries=3 update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      python3 xz-utils zstd e2fsprogs ninja-build \
      linux-image-6.1.0-51-amd64=6.1.177-1 busybox-static=1:1.35.0-4+deb12u1+b1 \
      cpio=2.13+dfsg-7.1 kmod=30+20221128-1 ca-certificates=20230311+deb12u1; \
    rm -rf /var/lib/apt/lists/*
COPY inputs.lock.json install.py extract-ndk.py /toolchain/
RUN python3 /toolchain/install.py
WORKDIR /native-build
