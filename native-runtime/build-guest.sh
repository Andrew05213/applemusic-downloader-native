#!/usr/bin/env bash
set -euo pipefail
umask 022
export LC_ALL=C TZ=UTC
: "${SOURCE_DATE_EPOCH:?}"
kernel_version=6.1.0-51-amd64
stage=/native-build/guest-root
source=/native-build/source
mkdir -p "$stage"/{bin,dev,proc,sys,data,tmp,run,etc,usr/local/android,lib/modules} /out/guest /out/notices /out/evidence
install -m 0755 "$(command -v busybox)" "$stage/bin/busybox"
ln -s busybox "$stage/bin/sh"
cp -a "$source/rootfs/system" "$stage/system"
# Never ship the old/native main executable or any upstream cached state.
rm -f "$stage/system/bin/main"
install -m 0755 "$source/rootfs/system/bin/lite" "$stage/system/bin/lite"
install -m 0755 /native-build/init "$stage/init"
install -m 0755 /native-build/amdl-guest "$stage/bin/amdl-guest"
install -m 0644 /etc/ssl/certs/ca-certificates.crt "$stage/usr/local/android/cert.pem"
printf 'nameserver 10.0.2.3\n' > "$stage/etc/resolv.conf"
chmod 1777 "$stage/tmp"
test -z "$(find "$stage/data" -mindepth 1 -print -quit)"
grep -a -q '/usr/local/android/cert.pem' "$stage/system/lib64/libcurl.so"

# Resolve dependency closures only from the exact packaged guest kernel.
module_dir=/lib/modules/$kernel_version
test -f "/boot/vmlinuz-$kernel_version"
mkdir -p "$stage/lib/modules/$kernel_version"
for metadata in modules.builtin modules.builtin.modinfo modules.order; do
  if test -f "$module_dir/$metadata"; then cp "$module_dir/$metadata" "$stage/lib/modules/$kernel_version/"; fi
done
for module in e1000 virtio_pci virtio_blk crc32c_generic ext4 qemu_fw_cfg; do
  modprobe --show-depends --set-version "$kernel_version" "$module"
done | awk '$1 == "insmod" { print $2 }' | sort -u > /native-build/module-files
while IFS= read -r module; do
  case "$module" in "$module_dir"/*) ;; *) echo native_guest_module_mismatch >&2; exit 1 ;; esac
  target="$stage$module"
  mkdir -p "$(dirname "$target")"
  case "$module" in
    *.xz) xz -dc "$module" > "${target%.xz}" ;;
    *.zst) zstd -dc "$module" > "${target%.zst}" ;;
    *) cp "$module" "$target" ;;
  esac
done < /native-build/module-files
depmod -b "$stage" "$kernel_version"
find "$stage" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
(cd "$stage"; find . -print0 | sort -z | cpio --null -o -H newc --reproducible --owner=0:0 --quiet | gzip -n -9) > /out/guest/initramfs.cpio.gz
install -m 0644 "/boot/vmlinuz-$kernel_version" /out/guest/vmlinuz
truncate -s 64M /out/guest/data.img
E2FSPROGS_FAKE_TIME="$SOURCE_DATE_EPOCH" mke2fs -q -F -t ext4 -L AMDLV2 \
  -U 5d3be526-4926-4d35-b4f5-2b8ec0784c01 \
  -E lazy_itable_init=0,lazy_journal_init=0,hash_seed=5d3be526-4926-4d35-b4f5-2b8ec0784c01 \
  /out/guest/data.img
install -m 0644 "$source/LICENSE" /out/notices/wrapper-LICENSE
install -m 0644 /native-build/inputs.lock.json /out/evidence/inputs.lock.json
install -m 0644 /native-build/guest-agent.lock.json /native-build/guest-agent-source.json /out/evidence/
dpkg-query -W -f='${Package}=${Version}\n' | sort > /out/evidence/packages.resolved.lock
sha256sum "$stage/system/bin/lite" "$stage/usr/local/android/cert.pem" > /out/evidence/guest-components.sha256
find /out -type f -exec touch -d "@$SOURCE_DATE_EPOCH" {} +
