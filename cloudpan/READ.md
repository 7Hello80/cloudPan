# 原神抽卡分析工具

一个轻量级的原神抽卡记录分析工具，支持多平台运行，提供 Web 界面进行数据分析和管理。

## 功能特性

- **抽卡记录分析** - 支持角色池、武器池、常驻池、集录池、千星奇域等多种卡池
- **数据统计** - 显示总抽数、五星/四星数量、垫抽数、原石消耗等
- **保底进度** - 可视化显示保底进度条，标注软保底和硬保底
- **历史记录** - 自动保存用户数据，支持增量更新
- **Excel 导出** - 一键导出抽卡记录到 Excel 文件
- **自动更新** - 后台自动检测更新，支持跨平台自动升级
- **多平台支持** - 支持 Windows、Linux、macOS，多种架构

## 快速开始

### 下载安装

从 [Releases](https://github.com/7Hello80/GenshinLog-Obtain/releases) 页面下载对应平台的可执行文件：

| 平台 | 架构 | 文件名 |
|------|------|--------|
| Windows | x64 | `yuanshen-go-windows-amd64.exe` |
| Windows | arm64 | `yuanshen-go-windows-arm64.exe` |
| Linux | x64 | `yuanshen-go-linux-amd64` |
| Linux | arm64 | `yuanshen-go-linux-arm64` |
| macOS | x64 | `yuanshen-go-darwin-amd64` |
| macOS | arm64 | `yuanshen-go-darwin-arm64` |

### 运行

```bash
# Linux/macOS
chmod +x yuanshen-go-linux-amd64
./yuanshen-go-linux-amd64

# Windows
yuanshen-go-windows-amd64.exe
```

服务启动后访问 `http://localhost:5004` 即可使用。

### 配置文件

首次运行会自动生成 `config.yaml` 配置文件：

```yaml
# 原神抽卡分析系统配置文件

# 服务器配置
server:
  port: 5004          # 服务端口
  host: "0.0.0.0"     # 监听地址

# 数据存储配置
storage:
  data_dir: "./data"  # 数据存储目录

# 请求配置
request:
  timeout: 30         # 请求超时时间（秒）
  delay: 500          # 请求间隔时间（毫秒）
```

## 使用说明

### 获取抽卡链接

1. 使用 HoYoGet 工具获取抽卡记录链接
2. 复制链接粘贴到输入框
3. 点击「开始分析」

### 功能说明

- **历史记录** - 左侧显示已保存的用户记录，点击可快速加载
- **卡池切换** - 顶部标签切换不同卡池数据
- **五星记录** - 显示五星物品获取信息，包含抽数和是否歪了
- **四星记录** - 显示四星物品获取信息
- **导出 Excel** - 导出当前数据到 Excel 文件

## 自动更新

程序自动检测更新，发现新版本时会：

1. 弹窗提示有新版本
2. 自动下载对应平台的更新包
3. 自动替换可执行文件
4. 自动重启程序

## API 接口

| 接口 | 方法 | 说明 |
|------|------|------|
| `/api/gachaLog` | POST | 分析抽卡数据 |
| `/api/loadHistory` | GET | 加载历史记录 |
| `/api/userList` | GET | 获取用户列表 |
| `/api/deleteUser` | DELETE | 删除用户数据 |
| `/api/getPage` | GET | 获取任务进度 |
| `/api/exportExcel` | POST | 导出 Excel |
| `/api/update/status` | GET | 获取更新状态 |

## 技术栈

- **后端**: Go 1.24
- **前端**: Vue 2 + Element UI + MDUI
- **数据存储**: JSON 文件
- **Excel 导出**: excelize