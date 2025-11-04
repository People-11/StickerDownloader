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

FROM alpine:latest

RUN apk add --no-cache ffmpeg redis ca-certificates

WORKDIR /app

COPY --from=builder /app/app ./
COPY --from=builder /app/languages ./languages

RUN mkdir -p lottie2gif
COPY --from=lottie2gif-builder /build/lottie2gif/output/lottie2gif ./lottie2gif/

VOLUME ["/app/storage", "/app/log"]

# 默认启动程序
CMD ["./app"]