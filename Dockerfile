FROM golang:1.23-alpine as builder
LABEL authors="Roy"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -trimpath \
    -o app .

FROM alpine:latest as lottie2gif-builder

RUN apk add --no-cache \
    git \
    cmake \
    make \
    g++ \
    build-base \
    musl-dev

WORKDIR /build

RUN git clone --recursive https://github.com/rroy233/lottie2gif.git

WORKDIR /build/lottie2gif

# 你不觉得20FPS不够看么
# 修改到30FPS以避免被放慢速度
RUN sed -i '1i#include <algorithm>' main.cpp && \
    sed -i 's/player->frameRate() \/ 20\.0/player->frameRate() \/ 30.0/g' main.cpp && \
    # 帧延迟根据实际帧率计算，保持原始播放速度
    # delay = 100 / min(frameRate, 30)
    sed -i 's/addFrame(surface,2\*n)/addFrame(surface, 100.0 \/ std::min(player->frameRate(), 30.0))/g' main.cpp

RUN mkdir build && \
    cd build && \
    cmake -DCMAKE_BUILD_TYPE=Release \
          -DBUILD_SHARED_LIBS=OFF \
          -DCMAKE_CXX_FLAGS="-Os -s -static-libgcc -static-libstdc++" \
          -DCMAKE_EXE_LINKER_FLAGS="-static" \
          .. && \
    make && \
    strip output/lottie2gif || true

FROM alpine:latest as ffmpeg-builder

ARG TARGETARCH

RUN apk add --no-cache \
    git \
    build-base \
    yasm \
    nasm \
    zlib-dev \
    zlib-static

WORKDIR /build
RUN git clone --depth 1 --branch n7.0 https://git.ffmpeg.org/ffmpeg.git

WORKDIR /build/ffmpeg

RUN set -ex && \
    ARCH_FLAGS="" && \
    if [ "$TARGETARCH" = "amd64" ]; then \
        ARCH_FLAGS="--enable-x86asm --enable-inline-asm --enable-lto"; \
        EXTRA_CFLAGS="-Ofast -march=x86-64-v3 -mtune=generic -flto=auto -ffast-math -funroll-loops -fomit-frame-pointer -ffunction-sections -fdata-sections -pipe -fno-plt -fno-semantic-interposition"; \
        EXTRA_LDFLAGS="-flto=auto -Wl,--gc-sections -Wl,--as-needed -Wl,-O1 -Wl,--strip-all -static"; \
    elif [ "$TARGETARCH" = "arm64" ]; then \
        ARCH_FLAGS="--enable-neon --enable-inline-asm --enable-lto"; \
        EXTRA_CFLAGS="-O3 -flto=auto -funroll-loops -fomit-frame-pointer -ffunction-sections -fdata-sections -pipe"; \
        EXTRA_LDFLAGS="-flto=auto -Wl,--gc-sections -Wl,--as-needed -Wl,-O1 -Wl,--strip-all -static"; \
    else \
        EXTRA_CFLAGS="-O3 -ffunction-sections -fdata-sections -pipe"; \
        EXTRA_LDFLAGS="-Wl,--gc-sections -Wl,--as-needed -Wl,-O1 -static"; \
    fi && \
    ./configure \
      --disable-everything \
      --disable-autodetect \
      --enable-decoder=webp,vp8,vp9,h264 \
      --enable-encoder=gif,png \
      --enable-demuxer=webp,matroska,mov,image2 \
      --enable-muxer=gif,image2 \
      --enable-parser=vp8,vp9,h264 \
      --enable-filter=fps,scale \
      --enable-protocol=file \
      --enable-zlib \
      --disable-doc \
      --disable-htmlpages \
      --disable-manpages \
      --disable-podpages \
      --disable-txtpages \
      --disable-network \
      --disable-debug \
      --disable-devices \
      --disable-ffplay \
      --disable-ffprobe \
      --enable-small \
      --enable-optimizations \
      --enable-runtime-cpudetect \
      ${ARCH_FLAGS} \
      --extra-cflags="$EXTRA_CFLAGS" \
      --extra-ldflags="$EXTRA_LDFLAGS" && \
    make -j$(nproc) && \
    strip --strip-all --remove-section=.comment --remove-section=.note ffmpeg

FROM alpine:latest

RUN apk add --no-cache redis ca-certificates

WORKDIR /app

COPY --from=builder /app/app ./
COPY --from=builder /app/languages ./languages

RUN mkdir -p lottie2gif
COPY --from=lottie2gif-builder /build/lottie2gif/output/lottie2gif ./lottie2gif/
COPY --from=ffmpeg-builder /build/ffmpeg/ffmpeg /usr/local/bin/ffmpeg

VOLUME ["/app/storage", "/app/log"]

# 默认启动程序
CMD ["./app"]