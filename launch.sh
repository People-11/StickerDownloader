#!/bin/sh
set -e

mkdir -p /app/redis_data

redis-server \
  --daemonize yes \
  --save 600 1 \
  --appendonly no \
  --bind 127.0.0.1 \
  --port 6379 \
  --dir /app/redis_data \
  --dbfilename dump.rdb \

sleep 1

shutdown() {
  echo "Shutting down gracefully..."
  pkill -TERM app && sleep 3
  redis-cli SAVE && redis-cli SHUTDOWN NOSAVE
}

trap shutdown SIGTERM SIGINT

./app &
wait $!
