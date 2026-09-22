# mage-mediagc

[English](README.md) · **简体中文**

**Magento 2 商品图片的独立、可回滚垃圾回收工具。**

[![CI](https://github.com/shuaiZend/mage-mediagc/actions/workflows/ci.yml/badge.svg)](https://github.com/shuaiZend/mage-mediagc/actions/workflows/ci.yml)
[![CodeQL](https://github.com/shuaiZend/mage-mediagc/actions/workflows/codeql.yml/badge.svg)](https://github.com/shuaiZend/mage-mediagc/actions/workflows/codeql.yml)
[![Release](https://img.shields.io/github/v/release/shuaiZend/mage-mediagc)](https://github.com/shuaiZend/mage-mediagc/releases)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.21%2B-00ADD8.svg)](https://go.dev)

---

## 快速使用（5 分钟上手）

只想先跑一遍看看？照下面走，**第 2 步是只读的，在任何生产环境、任何时间都能跑**。

### ① 安装

```sh
# 取最新版本号
VERSION=$(curl -sSL https://api.github.com/repos/shuaiZend/mage-mediagc/releases/latest \
  | grep -o '"tag_name": *"v[^"]*"' | cut -d'"' -f4)

# 下载二进制包与校验和，先校验再解包
curl -sSLO "https://github.com/shuaiZend/mage-mediagc/releases/download/${VERSION}/mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
curl -sSLO "https://github.com/shuaiZend/mage-mediagc/releases/download/${VERSION}/checksums.txt"
sha256sum --check checksums.txt --ignore-missing

tar xzf "mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
sudo install -m 0755 mage-mediagc /usr/local/bin/
mage-mediagc version
```

也可以装系统包（`.deb` / `.rpm` / `.apk`），或直接用容器镜像
`ghcr.io/shuaiZend/mage-mediagc`。详见[安装](#安装)。

### ② 只读扫描（零配置）

```sh
cd /var/www/magento        # 站点根目录，即含 app/etc/env.php 的目录
mage-mediagc scan
```

**零配置**：数据库凭证、媒体路径、表前缀全部由 `app/etc/env.php` 直接解析得到，
不需要 PHP、不需要 `bin/magento`、不需要 Composer，也不向站点里装任何东西。

先确认它解析到的配置对不对（密码已脱敏）：

```sh
mage-mediagc config show
```

### ③ 读懂报告

`scan` 只回答一个问题：**磁盘上哪些图，数据库里已经没有任何引用指向它们了。**

```
media root /var/www/magento/pub/media/catalog/product
magento    /var/www/magento
database   magento (8.0.36)

  files on disk    1212481  128.10 GB
  thumbnail cache  806540   34.80 GB
  db references    36482
  still used       36482   9.00 GB
  orphaned         369440  84.30 GB (30.5%)

reclaimable
  cache 34.80 GB + orphans 84.30 GB = 119.10 GB
```

关键看两行：

- **`still used`** —— 确认还活着的原图。数字对不上预期就说明引用收集有问题，别往下走。
- **`orphaned`** —— 孤儿原图，`reclaimable` 是清完之后能回收的空间。

加 `-v` 可以看到每条引用来源的明细（哪张表贡献了多少条路径），这是排查
「为什么某张图被判成孤儿」最快的入口：

```sh
mage-mediagc scan -v
```

### ④ 清派生缓存（零风险，建议先做）

Magento 的缩略图缓存是派生数据，删掉后访客访问时会自动重建。这是最安全的一刀。

```sh
mage-mediagc cache stats
mage-mediagc cache clean          # 预演，只报告不动手
mage-mediagc cache clean --apply  # 实际清空
```

### ⑤ 隔离孤儿原图（是「移动」，不是「删除」）

```sh
mage-mediagc quarantine           # 预演：报告会移动什么、碎片率多少
mage-mediagc quarantine --apply   # 确认后执行
```

`quarantine` 把孤儿文件**移动**到同分区的一个暂存目录里，并写下一份 JSONL 清单。
同分区移动是 inode 操作，几十万个文件只要几秒，**不额外占用磁盘**。

此时磁盘空间还没释放（文件还在同一块盘上），但线上目录已经干净了。

### ⑥ 看着站点，然后二选一

观察一个完整业务周期（建议至少覆盖一次大促或一个自然日）：

```sh
mage-mediagc verify               # 复核：孤儿应为 0，缺失数不应变化

# 一切正常 → 真正释放空间（此步不可逆）
mage-mediagc purge --apply

# 发现异常 → 全量还原，每个文件回到原来的路径
mage-mediagc restore --apply
```

数据库侧是**独立**的一步，请单独决策、先备份：

```sh
mysqldump --single-transaction magento > pre-db-clean.sql
mage-mediagc db-clean             # 先看统计
mage-mediagc db-clean --apply     # 再删
```

### 常用命令速查

| 我想…… | 命令 |
| --- | --- |
| 先看看有多少垃圾 | `mage-mediagc scan` |
| 输出 JSON / Markdown | `mage-mediagc scan -f json -o report.json` |
| 输出**中文**报告 | `mage-mediagc scan --language zh -f markdown -o 报告.md` |
| 列出孤儿文件路径（喂给 rsync） | `mage-mediagc list --kind orphan -o orphans.txt` |
| 清缩略图缓存 | `mage-mediagc cache clean --apply` |
| 隔离孤儿原图 / 回滚 | `mage-mediagc quarantine --apply` / `mage-mediagc restore --apply` |
| 真删、释放空间 | `mage-mediagc purge --apply` |
| 清数据库孤儿行 | `mage-mediagc db-clean --apply` |
| 清理后复核 | `mage-mediagc verify` |

> **所有会改动数据的命令默认都是预演（dry run）**，必须显式加 `--apply` 才会真正执行。

---

## 它解决什么问题

Magento 2 **没有**媒体文件的垃圾回收机制。

从后台管理界面逐个删产品，目录会自己收拾干净。但真实店铺删产品的方式是
批量导入、直接 `DELETE`、迁移失败、第三方扩展——这些路径**什么都不级联清理**：

- 行还留在 `catalog_product_entity_media_gallery`、
  `..._media_gallery_value_to_entity`、`..._media_gallery_value`，以及 5 张商品 EAV 表
  （`_varchar`、`_int`、`_text`、`_decimal`、`_datetime`）里
- 这些行引用过的图片文件永久留在磁盘上
- 派生缩略图缓存同样留下，每张图每个尺寸变体一个目录

`bin/magento` 里没有任何命令能清理它们。**Magento 官方没有图片 GC 命令。**
这个增长过程是静默的、永久的。

这不是理论担忧。对某个长期运行的生产目录做过一次实测，结果如下（数字已做近似，
因为精确数字透露的更多是这家店的信息，而不是问题本身）：

| 指标 | 数值 |
| --- | --- |
| `pub/media/catalog/product` | **约 130 GB**，约 120 万个文件 |
| 仍然被引用的原图 | **约 36,000** 个文件，占原图体积的约 10% |
| 孤儿原图 | **约 370,000** 个文件，**占原图体积的约 90%** |
| 派生缩略图缓存 | 约 800,000 个文件，占该目录体积的约 27% |
| 数据库中的产品数 | 约 5,900 |
| 残留 EAV 实体 ID | 约 57,000 |
| 孤儿数据库行 | 约 190 万 |

而且增长是**持续性**的，不是历史遗留：自 2020 年起，每年新增数万个孤儿文件。

`mage-mediagc` 找出这些垃圾并清掉它们——**且永远不会在一个你无法撤销的步骤里
删掉一原图**。

## 为什么不用现成的 Magento 模块

已经有一些 Magento 模块做这件事的一部分。有两个问题让它们**不安全**，不该指向线上店铺。

**它们只跟 `media_gallery` 表比对。** 一张图能不能被删，远不只看 gallery 表：

| 引用来源 | 只比对 gallery 的工具 | mage-mediagc |
| --- | :---: | :---: |
| `catalog_product_entity_media_gallery` | 有 | 有 |
| `image` / `small_image` / `thumbnail` / `swatch_image` 属性 | 无 | 有 |
| 分类的 `image` / `thumbnail` 属性 | 无 | 有 |
| 嵌入在商品描述里的图片 | 无 | 有 |
| 嵌入在 CMS 页面和区块里的图片 | 无 | 有 |
| Magento 的占位图（placeholder） | 无 | 有 |

分类横幅图、或者 CMS 区块里的一张商品图，在 gallery 表里**没有任何引用**。
只比对 gallery 的工具会把它们报告成孤儿并删掉——这就是线上店铺的数据丢失。

占位图是更隐蔽的一种情况：它们**只**被 `core_config_data` 引用，那是一张**配置表**
而不是媒体表，所以哪怕把每张媒体表都读一遍的工具也看不见它们。`mage-mediagc`
直接无条件保护 `catalog/product/placeholder/` 整个目录。

**它们是原地删除。** 没有暂存区、没有清单、没有回滚。

## 分期执行模型

| 阶段 | 命令 | 风险 | 可否回滚 |
| --- | --- | --- | --- |
| 出报告 | `scan` | 无，只读 | — |
| 清缩略图 | `cache clean --apply` | 无，Magento 会自动重建 | — |
| 隔离孤儿 | `quarantine --apply` | 文件被移动，原图完好 | `restore --apply` |
| 释放空间 | `purge --apply` | **不可逆** | 否 |
| 清数据库行 | `db-clean --apply` | **不可逆** | 从备份恢复 |

设计原则很简单：**任何一步都不删原图。** 清除一个孤儿文件意味着把它**移动**到
同一文件系统上的暂存目录，在那里 `os.Rename` 是一次 inode 操作——每个文件微秒级，
不额外占盘、不发生复制。隔离几十万个文件只要几秒。如果之后发现店铺不对，
`restore` 会把每个文件精确放回原位。

## 安装

### ① 发布包二进制（推荐）

```sh
VERSION=$(curl -sSL https://api.github.com/repos/shuaiZend/mage-mediagc/releases/latest \
  | grep -o '"tag_name": *"v[^"]*"' | cut -d'"' -f4)

curl -sSLO "https://github.com/shuaiZend/mage-mediagc/releases/download/${VERSION}/mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
tar xzf "mage-mediagc_${VERSION#v}_linux_amd64.tar.gz"
sudo install -m 0755 mage-mediagc /usr/local/bin/
mage-mediagc version
```

也可以到 [releases 页面](https://github.com/shuaiZend/mage-mediagc/releases) 自行挑选——
**Linux 与 macOS**，`amd64` 与 `arm64`，全部以 `CGO_ENABLED=0` 静态链接。
无运行时依赖、无需 `composer require`、不向站点里装任何东西。

### ② 系统包

每次发布都附带 `.deb`、`.rpm`、`.apk`。它们把二进制装到 `/usr/local/bin`，
把带注释的配置装到 `/etc/mage-mediagc/`，并安装可选的 systemd 单元
（**故意保持禁用状态**）：

```sh
sudo apt install ./mage-mediagc_*_linux_amd64.deb
sudo $EDITOR /etc/mage-mediagc/mage-mediagc.env     # 设置 MAGEGC_MAGENTO_ROOT
mage-mediagc scan --config /etc/mage-mediagc/mage-mediagc.yaml
```

### ③ 容器

```sh
docker run --rm \
  --network host \
  -v /var/www/magento:/magento \
  ghcr.io/shuaiZend/mage-mediagc scan --magento-root /magento
```

当 MySQL 监听在宿主机而不是容器内时，需要 `--network host`。镜像默认以 root 运行，
因为它的工作就是重命名属于 Web 服务器的文件；想用店铺自己的 uid 运行就传 `--user`。

### ④ 从源码构建

```sh
git clone https://github.com/shuaiZend/mage-mediagc
cd mage-mediagc
make build          # 遵循 GOHOSTOS/GOHOSTARCH
sudo ./deploy/install.sh --binary ./mage-mediagc
```

## 配置

五个来源，后面的覆盖前面的：

1. 内置默认值
2. `mage-mediagc.yaml`（来自 `--config`，或在工作目录自动发现）
3. `<Magento 根>/app/etc/env.php` —— **只补空缺，绝不覆盖**
4. `MAGEGC_*` 环境变量
5. 命令行参数

第 3 条的「只补空缺」规则，正是让工具可以对着副本库或 socket 使用而无需改任何配置的原因：
凡是你显式设置过的值，永远不会被替换。

从这两个文件改起：[examples/mage-mediagc.yaml](examples/mage-mediagc.yaml)（全注释）
和 [examples/mage-mediagc.env](examples/mage-mediagc.env)（给 systemd 用）。
细节见 [docs/configuration.md](docs/configuration.md)。

## 部署

二进制是静态且自包含的，所以部署就是拷贝一个文件。两种方式，取决于你想要的自动化程度。

**手动 / 临时** —— 拷二进制、直接跑。不需要别的。

**定时、只做安全操作** —— 系统包里带两个 systemd 定时器：

| 定时器 | 周期 | 作用 |
| --- | --- | --- |
| `mage-mediagc-scan.timer` | 每周，带抖动 | 把只读的 Markdown 报告写到 `/var/log/mage-mediagc/scan-latest.md` |
| `mage-mediagc-cache.timer` | 每天，带抖动 | 清空派生缩略图缓存 |

两者默认禁用，且从设计上就是只读或可再生的。
**隔离和数据库清理永远不会被定时执行**——它们必须人工来，因为那是需要人先看数字再动手的步骤。

```sh
sudo systemctl enable --now mage-mediagc-scan.timer mage-mediagc-cache.timer
journalctl -u mage-mediagc-scan.service -n 50
```

完整运维手册（含推荐的分阶段上线流程和精确回滚步骤）：
[docs/deployment.md](docs/deployment.md)、[docs/operations.md](docs/operations.md)。

## 安全模型

- **先读后写。** `scan`、`list`、`cache stats`、`config show`、`verify` 不可能改动任何东西。
- **默认预演。** 所有破坏性命令都必须显式加 `--apply`。
- **只移动，不删除。** 孤儿文件被重命名进暂存目录，并写下记录每个路径的 JSONL 清单，
  因此 `restore` 是精确的。
- **强制同文件系统。** 跨设备会被拒绝，因为静默的复制会翻倍占用磁盘、耗时数小时。
  `--allow-cross-device` 可覆盖。
- **碎片率闸门。** 当碎片率超过 `cleanup.maxDeleteFraction`（默认 0.98）时 `quarantine`
  会中止。如此极端的比例，几乎总是意味着引用收集失败了，而不是这家店真的全是垃圾。
- **保守匹配。** 只要**任何**候选路径能匹配上，文件就算存活，包括大小写不敏感匹配。
  误判为「存活」只浪费磁盘；误判为「孤儿」会丢图。
- **数据库侧护栏。** 执行任何一条 `DELETE` 之前先校验每张表是否存在；行以有界批次、
  各自独立事务删除；外键检查仅在专用连接上临时关闭，之后恢复。
- **`purge` 拒绝自作主张。** 没有清单的目录它不会删，除非你传 `--force`。

## 中文报告

报告支持中文，`--language zh`（或配置项 `output.language: zh`）。

```sh
mage-mediagc scan --language zh --format markdown -o 媒体碎片报告.md
```

Markdown 报告会翻译表头、摘要表、孤儿集中目录和后续步骤，适合直接贴进工单：

```markdown
# mage-mediagc 媒体碎片分析报告

- 媒体目录: `/var/www/magento/pub/media/catalog/product`
- Magento 根: `/var/www/magento`
- 数据库: `magento` (8.0.36)

## 摘要

| 指标 | 数量 | 体积 |
|---|---:|---:|
| 磁盘原图 | 1212481 | 128.00 GB |
| 派生缓存 | 806540 | 34.00 GB |
| 数据库引用 | 36482 | — |
| **存活文件** | **36482** | **9.00 GB** |
| **孤儿碎片** | **369440** | **84.00 GB** |
| 碎片率 | **30.5%** | |
| **合计可回收** | | **118.00 GB** |

## 建议的后续步骤

1. 先做零风险项：`mage-mediagc cache clean --apply` 清空派生缩略图缓存，Magento 会按需重建。
2. 先预演隔离：`mage-mediagc quarantine` 会报告将移动哪些文件；碎片率异常时会拒绝执行。
3. 确认后实际隔离：`mage-mediagc quarantine --apply`。文件只是移出媒体目录，并未删除；`mage-mediagc restore --apply` 可原样回滚。
4. 观察一个完整业务周期且无异常后：`mage-mediagc purge --apply` 才真正释放空间，此步不可逆。
5. 数据库侧独立进行：先备份数据库，核对 `mage-mediagc db-clean` 的统计，再执行 `mage-mediagc db-clean --apply`。
```

两点说明：

- **警告、跳过原因、错误信息始终是英文。** 它们是诊断信息，也是人们会贴进 bug 报告的内容，
  翻译它们会让不同安装之间的报告难以横向比对。
- 已知的**排版**局限：控制台 `table` 格式用 Go 的 `text/tabwriter` 对齐列，而它按
  **rune 数**而非**显示宽度**计算单元格宽度，因此中文标签的列会明显错位。
  `markdown` 和 `json` 格式不受影响，而 `markdown` 正是给工单用的格式。

## 目录结构

```
.
├── cmd/mage-mediagc/         入口
├── internal/
│   ├── phpconfig/            解析 app/etc/env.php 的 PHP 词法/语法分析器（不需要 PHP）
│   ├── config/               五层配置解析
│   ├── magento/              引用收集、孤儿统计、行清理
│   ├── media/                并行媒体目录扫描
│   ├── analyzer/             存活/孤儿判定与统计
│   ├── action/               缓存清理、隔离、还原、purge
│   ├── report/               table / JSON / Markdown 渲染
│   └── cli/                  cobra 命令层
├── internal/integration/     针对真实 MySQL 的端到端测试
├── deploy/                   systemd 单元、安装脚本、打包钩子
├── examples/                 带注释的配置与环境变量示例
├── docs/                     架构、配置、部署、运维、FAQ、参考手册
└── .github/workflows/        CI、Release、CodeQL
```

架构和每一层存在的理由见 [docs/architecture.md](docs/architecture.md)。

## 交付标准

本项目按生产级开源工具的惯例构建，以下每一条都在 CI 中被强制执行：

| 门禁 | 方式 |
| --- | --- |
| 格式 | `gofmt -s -l` 必须为空 |
| 静态检查 | `go vet` + `golangci-lint`（24 个 linter，零告警） |
| 模块整洁 | `go mod tidy` 必须是 no-op |
| 单元测试 | 在 Linux 与 macOS 上跑 `go test -race` |
| Go 版本兼容 | 1.21、1.22、1.23、1.24 |
| 集成测试 | 对**真实** MySQL 5.7 **和** 8.0 跑完整流程 |
| 交叉编译 | linux/darwin × amd64/arm64 |
| 发布产物 | goreleaser：归档包、deb/rpm/apk、校验和、变更日志 |
| 容器 | GHCR 上的多架构 `linux/amd64` + `linux/arm64` 镜像 |
| 供应链 | CodeQL（security-and-quality）、Dependabot、SHA-256 校验和 |

集成测试是最关键的一环：它搭出一套**迷你但忠实**的 Magento 目录库表结构，灌入本工具
存在的理由——那些垃圾数据，然后跑完整流程
scan → analyze → 预演 → 清理 → 隔离 → 还原 → purge 并断言结果，包括那条级联链：
删掉某个产品的最后一条 gallery 链接后，gallery 条目变成孤儿，进而它的 per-store 值也变成孤儿。

## 与现成方案对比

| | mage-mediagc | 做同样事情的 Magento 模块 |
| --- | --- | --- |
| 语言 | Go，单个静态二进制 | PHP，装进店铺里 |
| 安装 | 拷一个文件 | `composer require` + `setup:upgrade` |
| 店铺宕机时可用 | 可以 | 不行（需 Magento bootstrap） |
| 引用来源 | 5 类，含 CMS 与商品描述 | 只有 gallery |
| 回滚 | 暂存区 + 清单 + `restore` | 无 |
| 上规模 | 并行、分片、二分查找 | 单进程、进程内 |
| 机器可读输出 | JSON 与 Markdown | 无 |
| 删除数据库行 | 可选、分批、有校验 | 视实现而定 |

## 环境要求

- 一套 Magento 2 安装（已在 2.2、2.3、2.4 的库表结构上测试），或任何 `magento_*` 形状的数据库
- MySQL 5.7+ 或 MariaDB 10.2+；`db-clean` 需要读写权限，其余命令只读即可
- 宿主机上不需要 PHP，不需要 `bin/magento`，不需要 Composer
- Unix 系主机。二进制**只发布** Linux 与 macOS：安全保证依赖 POSIX 设备号与文件属主，
  依赖这些的步骤在没有它们的平台上会**拒绝执行**，而不是去猜

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/reference.md](docs/reference.md) | 每一条命令、参数、配置项、环境变量与磁盘产物 |
| [docs/configuration.md](docs/configuration.md) | 五个配置来源及它们如何相互作用 |
| [docs/deployment.md](docs/deployment.md) | 安装路径、系统包、容器、systemd、分阶段上线 |
| [docs/operations.md](docs/operations.md) | 运维手册：看什么、跑什么、怎么回滚 |
| [docs/architecture.md](docs/architecture.md) | 各组件如何拼在一起，每层为什么存在 |
| [docs/faq.md](docs/faq.md) | 安全性问题、意外结果、兼容性 |
| [CHANGELOG.md](CHANGELOG.md) | 变更历史 |

## 贡献

欢迎提 Issue 和 PR。请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md)——尤其是这一条：
**任何可能删除或移动数据的改动，都必须附上一个能证明它所防范的故障模式的测试。**

## 许可证

Apache License 2.0。见 [LICENSE](LICENSE)。
