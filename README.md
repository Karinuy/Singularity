# Singularity

Singularity 是一个使用 Go、SQLite 和 Docker Compose 构建的 Telegram Bot。

## 功能

- 入群检测：记录新成员入群事件，并发送算术题按钮验证。
- 入群问答验证：新成员需要点击群内按钮回答算术题，答错或超时会被临时移出群。
- 自动消息清理：Bot 的入群验证题、欢迎消息、RSS 管理命令回复等自动消息会延迟删除，避免群聊天堆积。
- RSS 订阅：支持按聊天添加、移除、查看 RSS 订阅，并定时把新条目发送到订阅所在群组或私聊。
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

Bot 需要在群里拥有删除消息和封禁用户的管理员权限，自动清理、广告处置和验证超时移出才能生效。

## 命令

```text
/start
/help
/rss_add <url>
/rss_remove <url>
/rss_list
/rss_check
```

Bot 启动时会向 Telegram 注册这些命令，所以可以在私聊或群组输入 `/` 从命令菜单唤出。

RSS 设置只允许 `BOT_OWNER_USER_ID` 指定的 Bot 创建者操作。`BOT_ADMIN_USER_IDS` 不授予 RSS 管理权限，只用于其他管理员白名单场景。

## RSS 机制

RSS 订阅只支持群组。你需要把 Bot 加入目标群组，然后在该群组内执行 `/rss_add <url>`，后续新条目会推送到这个群组。

私聊不再支持添加、删除或查看 RSS 订阅；已有私聊 RSS 订阅会在数据库迁移时清理。

添加订阅时，Bot 会把当前已有条目标记为已读，避免历史内容刷屏。之后定时轮询时，只发送新条目。RSS 新文章推送默认不会自动删除。

## 配置

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | 必填 | BotFather 提供的 Bot Token |
| `DATABASE_PATH` | `data/singularity.db` | SQLite 数据库路径 |
| `BOT_OWNER_USER_ID` | 空 | Bot 创建者的 Telegram 用户 ID；只有该用户可以设置 RSS |
| `BOT_ADMIN_USER_IDS` | 空 | 管理员白名单，当前用于广告检测豁免等非 RSS 管理场景 |
| `BOT_RSS_POLL_INTERVAL` | `5m` | RSS 轮询间隔 |
| `BOT_POLL_TIMEOUT_SECONDS` | `50` | Telegram long polling 超时秒数 |
| `BOT_WELCOME_MESSAGE` | `Welcome %s.` | 入群欢迎文案，`%s` 会替换为用户名 |
| `BOT_AUTO_CLEANUP_ENABLED` | `true` | 是否自动清理 Bot 自动消息和入群服务消息 |
| `BOT_AUTO_CLEANUP_DELAY` | `2m` | 自动消息发送多久后删除 |
| `BOT_VERIFICATION_ENABLED` | `true` | 是否启用入群算术题验证 |
| `BOT_VERIFICATION_TIMEOUT` | `3m` | 新成员点击验证答案的超时时间 |
| `BOT_VERIFICATION_MAX_VALUE` | `20` | 加减法题目的最大随机值，乘法会限制在 1 到 9 |
| `BOT_VERIFICATION_KICK_ON_TIMEOUT` | `true` | 验证超时后是否移出群 |
| `BOT_VERIFICATION_BAN_DURATION` | `300s` | 验证答错或超时后的临时封禁时长，不是永久封禁 |
| `BOT_AD_DETECTION_ENABLED` | `false` | 是否启用广告检测和处置 |
| `BOT_AD_KEYWORDS` | 内置广告词 | 逗号分隔的广告关键词 |
| `BOT_AD_SCORE_THRESHOLD` | `3` | 广告判定分数阈值 |
| `BOT_AD_LINK_LIMIT` | `2` | 链接数量达到该值会增加广告分 |
| `BOT_BAN_ADVERTISERS` | `true` | 是否封禁广告发送者 |
| `BOT_RSS_MAX_ITEMS_PER_POLL` | `5` | 每次每个订阅最多推送的新条目数 |
