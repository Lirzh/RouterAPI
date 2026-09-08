# RouterAPI 启动脚本 (PowerShell)
# 用法: .\start.ps1 [dev|build|backend|frontend]

param(
    [string]$Action = "dev"
)

$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $MyInvocation.MyCommand.Path

function Write-Step($msg) {
    Write-Host "`n[RouterAPI] $msg" -ForegroundColor Cyan
}

function Check-Command($cmd) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Host "[RouterAPI] $cmd 未安装" -ForegroundColor Red
        return $false
    }
    return $true
}

switch ($Action) {
    "install" {
        Write-Step "安装依赖..."
        Push-Location "$RootDir\backend"
        if (Check-Command "go") {
            go mod tidy
        }
        Pop-Location
        Push-Location "$RootDir\frontend"
        if (Check-Command "pnpm") {
            pnpm install
        }
        Pop-Location
        Write-Step "依赖安装完成"
    }

    "backend" {
        Write-Step "启动后端 (http://localhost:3000)..."
        Push-Location "$RootDir\backend"
        if (-not (Check-Command "go")) { exit 1 }
        go run main.go
        Pop-Location
    }

    "frontend" {
        Write-Step "启动前端 (http://localhost:5173)..."
        Push-Location "$RootDir\frontend"
        if (-not (Check-Command "pnpm")) { exit 1 }
        pnpm dev
        Pop-Location
    }

    "build" {
        Write-Step "构建前端..."
        Push-Location "$RootDir\frontend"
        if (Check-Command "pnpm") { pnpm build }
        Pop-Location

        Write-Step "构建后端..."
        Push-Location "$RootDir\backend"
        if (Check-Command "go") {
            go build -o "$RootDir\bin\routerapi.exe" main.go
        }
        Pop-Location
        Write-Step "构建完成: bin\routerapi.exe"
    }

    "dev" {
        Write-Step "启动开发模式..."
        if (-not (Check-Command "go")) { Write-Host "请先安装 Go: https://go.dev/dl/" -ForegroundColor Yellow }
        if (-not (Check-Command "pnpm")) { Write-Host "请先安装 pnpm: npm i -g pnpm" -ForegroundColor Yellow }

        # 启动后端
        $backendJob = Start-Job -ScriptBlock {
            param($dir)
            Set-Location "$dir\backend"
            go run main.go
        } -ArgumentList $RootDir

        # 启动前端
        $frontendJob = Start-Job -ScriptBlock {
            param($dir)
            Set-Location "$dir\frontend"
            npx vite --host
        } -ArgumentList $RootDir

        Write-Host "[RouterAPI] 后端: http://localhost:3000" -ForegroundColor Green
        Write-Host "[RouterAPI] 前端: http://localhost:5173" -ForegroundColor Green
        Write-Host "[RouterAPI] 按 Ctrl+C 停止" -ForegroundColor Yellow

        try {
            while ($true) {
                Start-Sleep -Seconds 1
                if ($backendJob.State -eq "Failed") {
                    Write-Host "[RouterAPI] 后端进程异常" -ForegroundColor Red
                    Receive-Job $backendJob
                    break
                }
                if ($frontendJob.State -eq "Failed") {
                    Write-Host "[RouterAPI] 前端进程异常" -ForegroundColor Red
                    Receive-Job $frontendJob
                    break
                }
            }
        }
        finally {
            Stop-Job $backendJob -ErrorAction SilentlyContinue
            Stop-Job $frontendJob -ErrorAction SilentlyContinue
            Remove-Job $backendJob -ErrorAction SilentlyContinue
            Remove-Job $frontendJob -ErrorAction SilentlyContinue
        }
    }

    default {
        Write-Host "用法: .\start.ps1 [install|dev|build|backend|frontend]" -ForegroundColor Yellow
    }
}
