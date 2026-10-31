# 使用 Ubuntu 作为基础镜像，因为需要编译环境
FROM ubuntu:20.04

# 设置非交互式模式，避免安装过程中的交互提示
ENV DEBIAN_FRONTEND=noninteractive

# 安装必要的依赖
RUN apt-get update && apt-get install -y \
    wget \
    unzip \
    build-essential \
    clang \
    curl \
    && rm -rf /var/lib/apt/lists/*

# 下载并安装 Android NDK
ENV NDK_VERSION=r23b
ENV ANDROID_NDK_HOME=/opt/android-ndk-${NDK_VERSION}
RUN wget -q https://dl.google.com/android/repository/android-ndk-${NDK_VERSION}-linux.zip -O /tmp/ndk.zip
RUN unzip -q /tmp/ndk.zip -d /opt/ \
    && rm /tmp/ndk.zip

# 设置编译器环境变量
ENV CC=${ANDROID_NDK_HOME}/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android21-clang
ENV CXX=${ANDROID_NDK_HOME}/toolchains/llvm/prebuilt/linux-x86_64/bin/x86_64-linux-android21-clang++

# 设置工作目录
WORKDIR /app

# 复制源代码和构建文件
COPY . /app/

# 编译 wrapper
RUN make clean || true
RUN make

# 赋予执行权限
RUN chmod +x /app/wrapper
RUN chmod +x /app/rootfs/system/bin/linker64
RUN chmod +x /app/rootfs/system/bin/main

# 暴露 wrapper 监听的端口
# 根据 apple-music-downloader/config.yaml 中的 decrypt-m3u8-port: "127.0.0.1:10020"
EXPOSE 10020

# 定义容器启动时执行的命令
CMD ["/app/wrapper"]
