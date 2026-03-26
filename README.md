# OKX 量化交易（Go + Web 界面）

基于 [go-okx](https://github.com/iaping/go-okx) 对接 OKX API v5，提供 Web 界面：配置 API、查余额、看行情、现货下单、当前委托与撤单。

## 功能

- **API 配置**：页面内填写 API Key / Secret / Passphrase，支持模拟盘开关（密钥仅存内存，刷新即清空）
- **账户余额**：查询账户总权益与各币种余额
- **行情**：查询指定交易对最新价、买卖一、24h 成交量
- **下单**：现货市价/限价买入、卖出
- **当前委托**：查询未成交委托并支持撤单

## 环境要求

- Go 1.21+

## 快速开始

```bash
# 拉取依赖
go mod tidy

# 启动服务（默认 http://localhost:8080）
go run .
```


浏览器打开 `http://localhost:8080`，在「API 配置」中填写 OKX API 信息后即可使用。

### 编译为 EXE

在项目根目录执行：

```powershell
go build -o okx.exe .
```

编译成功后会在当前目录生成 `okx.exe`。

运行：

```powershell
.\okx.exe
```

### 使用环境变量（可选）

生产环境建议用环境变量，避免在页面输入密钥：

```bash
# Windows PowerShell
$env:OKX_API_KEY="your-api-key"
$env:OKX_SECRET_KEY="your-secret"
$env:OKX_PASSPHRASE="your-passphrase"
$env:OKX_SIMULATED="1"   # 1 或 true 为模拟盘
$env:PORT="8080"
go run .
```

参见 `.env.example`（需自行复制为 `.env` 并按需加载）。

## API Key 说明

1. 登录 [OKX](https://www.okx.com) → 个人中心 → API。
2. 创建 API Key，勾选「交易」等所需权限。
3. 妥善保管 Secret 与 Passphrase，Secret 仅展示一次。
4. 测试建议先用「模拟交易」并勾选界面上的「模拟盘」。

## 项目结构

```
.
├── go.mod
├── main.go           # HTTP 服务与 API 路由
├── internal/
│   └── okx/
│       └── client.go # OKX REST 封装（余额/行情/下单/撤单/委托）
├── web/
│   └── index.html   # 前端单页
├── .env.example
└── README.md
```

## 免责声明

本程序仅供学习与自用，使用 OKX API 进行真实交易需自行承担风险。请遵守 OKX 用户协议与当地法规。

## 一键推送

```bash
push.bat
```

说明：

- 已新增项目根目录脚本：`push.bat`
- 每次修改代码后，双击运行或在项目根目录执行 `.\push.bat`
- 脚本会自动执行：`git add .` -> `git commit -m "提交信息"` -> `git push -u origin 当前分支`
- 运行时可输入提交信息；如果留空，会自动使用当前时间作为提交说明

如果你还想保留手动命令，可使用：

```bash
git add .
git commit -m "your message"
git push -u origin main
```

## 查询提交次数

```bash
git rev-list --count HEAD
git shortlog -s -n
```

说明：

- `git rev-list --count HEAD`：查看当前仓库总提交次数
- `git shortlog -s -n`：按提交次数从高到低显示贡献者统计