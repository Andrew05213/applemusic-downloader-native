# Public, source-built tools only. No Apple runtime, accounts or old harness.
FROM docker.io/library/golang:1.23.1-bookworm@sha256:31817d38d90894d73ce3fe3b7f01c5f213ac7e9681bf462d4ad65afa8e902c7e
ARG SOURCE_DATE_EPOCH=1786320000
ENV SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} TZ=UTC LC_ALL=C
COPY fetch-snapshot-packages.py /toolchain/
RUN set -eux; \
    find /etc/apt/sources.list.d -type f -delete; \
    set -- \
      python3 xz-utils zstd e2fsprogs ninja-build \
      linux-image-6.1.0-51-amd64=6.1.177-1 busybox-static=1:1.35.0-4+deb12u1+b1 \
      cpio=2.13+dfsg-7.1 kmod=30+20221128-1 ca-certificates=20230311+deb12u1; \
    snapshot_apt() { \
      limit="$1"; shift; \
      timeout -k 10s "$limit" apt-get -o Acquire::https::Timeout=15 \
        -o Acquire::Retries=1 -o Acquire::Languages=none "$@"; \
    }; \
    selected=; \
    for host in snapshot-fastly.debian.org snapshot-cloudflare.debian.org snapshot.debian.org; do \
      echo "Trying Debian snapshot endpoint: $host"; \
      rm -rf /var/lib/apt/lists/*; \
      printf 'deb [check-valid-until=no signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] https://%s/archive/debian/20260728T000000Z/ bookworm main\ndeb [check-valid-until=no signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] https://%s/archive/debian-security/20260728T000000Z/ bookworm-security main\n' "$host" "$host" > /etc/apt/sources.list; \
      if snapshot_apt 90s -o APT::Update::Error-Mode=any update; then \
        selected="$host"; break; \
      fi; \
      echo "Debian snapshot endpoint did not complete: $host" >&2; \
    done; \
    test -n "$selected"; \
    timeout -k 10s 8m python3 /toolchain/fetch-snapshot-packages.py "$@"; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-download --no-install-recommends "$@"; \
    rm -rf /var/lib/apt/lists/*
COPY inputs.lock.json install.py extract-ndk.py /toolchain/
RUN python3 /toolchain/install.py
WORKDIR /native-build
