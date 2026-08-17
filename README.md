# Singularity Telegram Bot

Singularity 是一个使用 Go、SQLite 和 Docker Compose 构建的 Telegram Bot。

## 功能

- 入群检测：记录新成员入群事件，并发送算术题验证。
- 入群问答验证：新成员需要在群内回答算术题，超时默认移出群。
- RSS 订阅：支持按聊天添加、移除、查看 RSS 订阅，并定时推送新条目。
- 广告检测封禁：默认关闭；开启后检测广告关键词和链接密度，删除广告消息并封禁发送者。

## 快速启动

1. 创建配置：

```bash
cp .env.example .env
```

2. 修改 `.env` 中的 `TELEGRAM_BOT_TOKEN`。

3. 启动：

```bash
docker compose up -d --build
```

Bot 需要在群里拥有删除消息和封禁用户的管理员权限，广告封禁和验证超时移出才能生效。

## 命令

```text
/start
/help
/rss_add <url>
/rss_remove <url>
/rss_list
/rss_check
```

如果配置了 `BOT_ADMIN_USER_IDS`，只有这些 Telegram 用户 ID 可以管理 RSS 订阅和手动触发检查。多个 ID 用英文逗号分隔。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | 必填 | BotFather 提供的 Bot Token |
| `DATABASE_PATH` | `data/singularity.db` | SQLite 数据库路径 |
| `BOT_ADMIN_USER_IDS` | 空 | 允许管理 RSS 的用户 ID 列表 |
| `BOT_RSS_POLL_INTERVAL` | `5m` | RSS 轮询间隔 |
| `BOT_POLL_TIMEOUT_SECONDS` | `50` | Telegram long polling 超时秒数 |
| `BOT_WELCOME_MESSAGE` | `欢迎 %s 入群。` | 入群欢迎文案，`%s` 会替换为用户名 |
| `BOT_VERIFICATION_ENABLED` | `true` | 是否启用入群算术题验证 |
| `BOT_VERIFICATION_TIMEOUT` | `3m` | 新成员回答验证题的超时时间 |
| `BOT_VERIFICATION_MAX_VALUE` | `20` | 加减法题目的最大随机值，乘法会限制在 1 到 9 |
| `BOT_VERIFICATION_KICK_ON_TIMEOUT` | `true` | 验证超时后是否移出群 |
| `BOT_AD_DETECTION_ENABLED` | `false` | 是否启用广告检测和处置 |
| `BOT_AD_KEYWORDS` | 内置广告词 | 逗号分隔的广告关键词 |
| `BOT_AD_SCORE_THRESHOLD` | `3` | 广告判定分数阈值 |
| `BOT_AD_LINK_LIMIT` | `2` | 链接数量达到该值会增加广告分 |
| `BOT_BAN_ADVERTISERS` | `true` | 是否封禁广告发送者 |
| `BOT_RSS_MAX_ITEMS_PER_POLL` | `5` | 每次每个订阅最多推送的新条目数 |
