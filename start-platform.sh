#!/bin/bash
set -e

echo "=== 启动 RayAI Studio 分布式 AI 训练平台开发服务 (Go 1.24 + Vue 3) ==="
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

cd "$DIR/backend"
echo "[1/2] 启动 Go 后端控制面 API 服务 (Port 8080)..."
GOPROXY=https://goproxy.cn,direct go run main.go &
BACKEND_PID=$!

cd "$DIR/frontend"
if [ ! -d "node_modules" ]; then
  echo "[2/2] 安装 Vue 3 npm 依赖..."
  npm install --registry=https://registry.npmmirror.com
fi

echo "[2/2] 启动 Vue 3 开发控制台 (Port 3000)..."
npm run dev &
FRONTEND_PID=$!

echo "=================================================="
echo " RayAI Studio 训练平台启动成功!"
echo " 🎨 Vue 3 Web 控制台: http://localhost:3000"
echo " 🚀 Go 后端 API 地址: http://localhost:8080"
echo "=================================================="

wait $BACKEND_PID $FRONTEND_PID
