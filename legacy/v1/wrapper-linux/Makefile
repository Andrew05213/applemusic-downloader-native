NDK_TOOLCHAIN = $(NDK_PROJECT_PATH)/toolchains/llvm/prebuilt/linux-x86_64/bin
CC = $(NDK_TOOLCHAIN)/x86_64-linux-android21-clang
CXX = $(NDK_TOOLCHAIN)/x86_64-linux-android21-clang++

all: handle.o main wrapper

handle.o: main.cpp
	$(CXX) -c -o handle.o main.cpp

main: handle.o test.c
	$(CC) -DMyRelease -o rootfs/system/bin/main handle.o test.c -L ./rootfs/system/lib64 -Wl,-rpath=./rootfs/system/lib64 -Wl,-rpath-link=./rootfs/system/lib64 -landroidappmusic -lstoreservicescore -lmediaplatform -lc++_shared -landroid -lc -lm

wrapper: wrapper.c
	gcc -O3 -Wall -o wrapper wrapper.c

clean:
	rm -f handle.o wrapper rootfs/system/bin/main
