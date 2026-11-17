# Discord Bots

チャンネル権限管理とリンク補正を提供するDiscord Botです。`times-bot` と `utils-bot` は、それぞれ独立したDiscordアプリケーション、トークン、プロセスで動作します。

| 実行ファイル | 役割 |
| --- | --- |
| `times-bot` | 指定カテゴリーで通常のDiscord操作により作成されたチャンネルを、作成者が管理できる状態に変更 |
| `utils-bot` | X / Twitterの投稿URLに、FxTwitterの埋め込み用URLを返信 |

Goと `disgo` を使用します。Botごとに状態をJSONファイルへ保存し、各プロセスは1つのDiscordサーバーを管理します。

## 1. チャンネル権限の制約

**カテゴリーの `@everyone` に「チャンネルの管理」を許可すると、カテゴリー自体の編集・削除も許可されます。** Discordには「チャンネルの作成だけ」を許可する専用権限がありません。`times-bot` は作成後に権限を更新するため、権限の切り替えまでに時間差が生じます。停止中の新規チャンネルは、Botの再起動後に再照合されます。

また、次の点はDiscordの権限モデルによる制約です。

- サーバー所有者とAdministrator権限を持つ人は、チャンネルの拒否設定を回避できます。
- 親カテゴリーでの管理権限によって、子チャンネルの並べ替えができる場合があります。
- カテゴリーを削除すると、子チャンネルはカテゴリーから外れます。Botはカテゴリーを自動再作成しません。
- Botが閲覧できない非公開チャンネルは修正できません。新規チャンネルでもBotの閲覧・管理権限を保持してください。Discordが不可視チャンネルの伏せられた情報を返した場合は、権限を上書きせずエラーにします。

カテゴリーの編集・削除を一般メンバーに許可しない運用では、標準のチャンネル作成操作に代わる専用の作成フローが必要です。

## 2. Discord側の準備

[Discord Developer Portal](https://discord.com/developers/applications) で2つのApplicationを作り、それぞれのBotトークンを用意します。OAuth2の `bot` スコープで対象サーバーに招待します。

Discordの「ユーザー設定 → 詳細設定 → 開発者モード」を有効にすると、サーバー・カテゴリー・チャンネルのIDをコピーできます。

### times-bot

Botに必要な権限は次のとおりです。

| 場所 | 権限 |
| --- | --- |
| サーバー | 監査ログを表示 / View Audit Log |
| 各対象カテゴリー | チャンネルを見る / View Channel |
| 各対象カテゴリー | チャンネルの管理 / Manage Channels |
| 各対象カテゴリー | 権限の管理 / Manage Permissions（API名はManage Roles） |
| 各対象カテゴリー | 接続 / Connect |

対象カテゴリーでは、Bot自身に必要な権限を明示的に許可してください。Connectは、同じカテゴリー内のボイス・ステージチャンネルの作成者に接続権限を付与するためにも必要です。

この使用するGateway IntentはGuildsとGuildModerationです。Message Content IntentとServer Members Intentは不要です。

**準備の順序：**

1. Botに必要な権限を付け、設定ファイルを作ります。
2. 既存の子チャンネルの権限を確認します。Bot初回起動より古いチャンネルは自動変更しません。必要な既存チャンネルは手動で、一般メンバーの管理権限を明示的に拒否してください。
3. Botを起動し、ログで `gateway_ready` を確認します。
4. 対象カテゴリーの `@everyone` に「チャンネルの管理」を許可します。一般メンバーの「権限の管理」は許可する必要がありません。
5. 一般メンバーのアカウントで、カテゴリーを右クリック →「チャンネルを作成」を試します。

カテゴリーで作成権限が許可されていない場合も起動できますが、警告を記録します。

### utils-bot

BotのDeveloper Portalで **Message Content IntentをON** にしてください。通常のメッセージ本文からURLを読み取るために使います。コードではGuilds、GuildMessages、MessageContentを指定しています。

対象チャンネルで次の権限を付けます。

- チャンネルを見る / View Channel
- メッセージを送信 / Send Messages
- 埋め込みリンク / Embed Links
- メッセージ履歴を読む / Read Message History
- スレッド内でも使う場合は、スレッドでメッセージを送信 / Send Messages in Threads

元投稿は編集・削除しないため、Manage Messages権限は不要です。

## 3. 設定ファイル

リポジトリを取得します。

```bash
git clone https://github.com/astrapath-dev/discord-bot.git
cd discord-bot
```

Botごとの設定ファイルを作ります。

```bash
cp .env.times.example .env.times
cp .env.utils.example .env.utils
chmod 600 .env.times .env.utils
```

それぞれのファイルの `DISCORD_TOKEN`、`GUILD_ID` を置き換えます。`.env.times` には `CATEGORY_IDS` も設定します。2つのファイルには別々のBotトークンを使ってください。

`.env.times` の例：

```dotenv
DISCORD_TOKEN=replace_with_times_bot_token
GUILD_ID=123456789012345678
CATEGORY_IDS=234567890123456789,345678901234567890
STATE_FILE=./data/owner.json
RECONCILE_INTERVAL_SECONDS=60
REQUEST_TIMEOUT_SECONDS=30
WORKERS=4
QUEUE_CAPACITY=1024
```

`.env.utils` の例：

```dotenv
DISCORD_TOKEN=replace_with_utils_bot_token
GUILD_ID=123456789012345678
CHANNEL_IDS=
MAX_LINKS=5
RETENTION_DAYS=7
STATE_FILE=./data/links.json
RECONCILE_INTERVAL_SECONDS=300
REQUEST_TIMEOUT_SECONDS=30
WORKERS=4
QUEUE_CAPACITY=1024
```

| 設定 | 説明 |
| --- | --- |
| `DISCORD_TOKEN` | そのBotのトークン。必須 |
| `GUILD_ID` | 対象サーバーのID。必須 |
| `CATEGORY_IDS` | times-bot用。対象カテゴリーIDをカンマ区切り。必須 |
| `CHANNEL_IDS` | utils-bot用。空なら対象サーバーのアクセス可能なチャンネル。指定時は完全一致のチャンネル・スレッドID。カテゴリーや親チャンネルを指定しても子に展開されません |
| `STATE_FILE` | 永続状態の保存先。2つのBotで共有しないでください |
| `RECONCILE_INTERVAL_SECONDS` | 再照合の周期。times-bot既定60秒、utils-bot既定300秒。範囲15〜86400秒 |
| `REQUEST_TIMEOUT_SECONDS` | RESTリクエストごとの期限。既定30秒、範囲5〜300秒 |
| `WORKERS` | Bot内の並行処理数。イベント処理と定期再照合で共有。既定4、範囲1〜16 |
| `QUEUE_CAPACITY` | 未処理・処理中のイベントキー数上限。既定1024、範囲16〜10000 |
| `MAX_LINKS` | 1投稿あたりの補正リンク数上限。既定5、範囲1〜5 |
| `RETENTION_DAYS` | リンク投稿を追跡する期間。既定7日、範囲1〜90日 |

直接起動の `-env` は `KEY=value`、単一・二重引用符、コメントだけの行に対応します。変数展開やシェル実行はしません。行末コメントは使わず、コメントは別行にしてください。既に設定されている環境変数がファイルより優先されます。

## 4. 起動

### A. Docker Compose

Docker EngineとComposeプラグインを使える環境で、設定ファイル作成後に実行します。

```bash
docker compose up -d --build
docker compose logs -f
```

どちらか一方だけ起動する場合：

```bash
docker compose up -d --build times-bot
```

```bash
docker compose up -d --build utils-bot
```

状態は `owner-data` / `links-data` のnamed volumeに保存します。保存先は `/app/data/owner.json` / `/app/data/links.json` です。更新時も同じvolumeを使用してください。`docker compose down -v` は保存状態を削除します。

コンテナは非rootで動作し、ホストへの待受ポートはありません。DiscordへのHTTPS/WebSocket接続とDNSが必要です。

### B. ソースからビルドして実行

Go 1.24以降を使用します。CIではGo 1.27.4を使用し、実行ファイルを生成します。

```bash
make build
```

ビルド後の実行環境にはGoランタイムの追加インストールは不要です。OSのCA証明書は必要です。

```bash
./bin/times-bot -env .env.times -check-config
./bin/utils-bot -env .env.utils -check-config
```

`-check-config` はローカル設定の形式だけを検査します。トークンの有効性やDiscord上の権限は、実際の起動時に検査します。

別々のターミナル、またはプロセス管理サービスから起動します。

```bash
./bin/times-bot -env .env.times
```

```bash
./bin/utils-bot -env .env.utils
```

Makeを使わない場合：

```bash
go build -buildvcs=false -trimpath -o bin/times-bot ./cmd/times-bot
go build -buildvcs=false -trimpath -o bin/utils-bot ./cmd/utils-bot
```

Linux ARM64向けの例：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -o bin/times-bot-arm64 ./cmd/times-bot
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -o bin/utils-bot-arm64 ./cmd/utils-bot
```

### systemdでの常駐

`deploy/discord-bot@.service` を同梱しています。専用ユーザー `discordbots` を用意し、バイナリを `/opt/discord-bots/bin/`、書き込み可能な状態ディレクトリを `/opt/discord-bots/data/` に配置してください。

環境ファイルは `/etc/discord-bots/times-bot.env` と `/etc/discord-bots/utils-bot.env` に保存します。環境ファイルはroot所有・0600とし、状態ファイルはそれぞれ `/opt/discord-bots/data/owner.json` と `/opt/discord-bots/data/links.json` を指定します。unitを `/etc/systemd/system/discord-bot@.service` に配置後：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now discord-bot@times-bot discord-bot@utils-bot
sudo journalctl -u discord-bot@times-bot -u discord-bot@utils-bot -f
```

## 5. times-botの動作

1. 指定カテゴリー内の新しい通常チャンネルを検知します。
2. 作成者が未判明なら、先にBot以外のチャンネル管理・権限管理を拒否します。
3. チャンネルIDに一致する作成監査ログから、実行者IDを確定します。単に「最新ログのユーザー」を作成者とはみなしません。
4. 所有者情報を保存し、最新のチャンネル情報を取り直してから権限を変更します。

| 対象 | Manage Channels | Manage Roles / Permissions | 閲覧など |
| --- | --- | --- | --- |
| `@everyone` と他のロール・メンバー上書き | 拒否 | 拒否 | 既存の無関係な権限を保持 |
| 作成者 | 許可 | 拒否 | View Channelを許可。音声系ではConnectも許可 |
| times-bot Bot | 許可 | 許可 | View Channelを許可。音声系ではConnectも許可 |

作成者はチャンネル名の変更や削除などを行えます。権限の再委譲・公開範囲の変更はできません。管理者による例外は前述のとおりです。

他のロール上書きに管理権限の許可があると `@everyone` の拒否だけでは防げないため、その管理ビットも整理します。管理・権限管理以外のビットは保持します。作成者・Botの閲覧／接続許可は明示的な追加です。

子チャンネルの権限はカテゴリーとの同期から外れます。その後のカテゴリー権限変更は、その子へ自動的には反映されません。誰かが子の管理権限を再び広げた場合は、更新イベントまたは定期再照合で設定に戻します。

対象はテキスト、ボイス、アナウンス、ステージ、フォーラム、メディアチャンネルです。スレッド・カテゴリー・DMは対象外です。別カテゴリーへ移動したチャンネルは、その間は修正しません。

監査ログ検索は最新のチャンネル作成ログ最大500件です。遅延を考慮して短時間の再試行を行い、その後は定期再照合で再試行します。正確な作成記録を取得できない場合は、一般メンバーに管理を開放せず、管理者が対応できる状態を維持します。

初回起動時刻は状態ファイルに保持します。それより古い未追跡チャンネルは自動採用しません。初回起動後にBot停止中に作られたチャンネルは、再起動時の再照合で対象になります。監査ログの保持期間や検索上限により、長期停止後に作成者を解決できない場合があります。

## 6. utils-botの動作

投稿例：

```text
https://x.com/example/status/1234567890123456789?s=20
```

Botの返信：

```text
https://fxtwitter.com/example/status/1234567890123456789
```

- `x.com` / `twitter.com` と、それぞれの `www` / `mobile` ホストを扱います。
- 対象は投稿URLです。プロフィール、似た名前のドメイン、認証情報入りURLなどは扱いません。
- クエリ・フラグメント・写真／動画番号の接尾辞を除き、投稿全体へのリンクに統一します。同じ投稿IDは重複除外します。
- インラインコード、コードブロック、`<URL>`、`||URL||` は補正対象から外します。
- BotとWebhookからの投稿を無視します。返信で `@everyone` やユーザーへの通知を発生させない設定です。
- 元投稿を保持し、Bot自身の返信のみを作成・更新・削除します。元の埋め込みの抑制は行いません。
- 追跡中の元投稿が編集されたら返信を更新し、URLが無くなったり元投稿が削除されたら返信を削除します。
- 最初から対象URLが含まれていた新規投稿を処理します。Botが受信していない過去の投稿や、未追跡の投稿へ後からURLを追加した編集は新規処理しません。
- 再起動・再接続後は追跡済み投稿を再照合します。未追跡の履歴をさかのぼって返信しません。

返信だけを手動削除すると、次の再照合で再作成されます。プレビューを止めたい場合は、元のURLを削除するか `<URL>` などへ変更してください。リンクをすべて削除して追跡が終わった投稿に、後から再度リンクを追加しても新規処理しません。

追跡は既定7日、最大1万件です。期限が来ると追跡情報だけを削除し、Discord上の既存返信は残します。その後は元投稿の編集・削除に追従しません。

`utils-bot` が返すのは補正URLです。プレビューはDiscordとFxEmbedサービスが生成するため、外部サービスの状態によっては表示されない場合があります。投稿内容の安全性やセンシティブ判定は行いません。

## 7. 状態保存・運用

- 状態ファイルは一時ファイルへの書き込み・同期・renameで更新します。書き込みに失敗した状態で、保存済みと扱いません。
- 同じ状態ファイルを使うプロセスの二重起動をOSのファイルロックで拒否します。Botごとに1インスタンスで運用してください。別ホストや別状態ファイルで同じトークンを重複稼働させることは防げません。
- 状態が壊れている、保存したGuildと設定が異なる、といった場合は起動を止めます。自動で空の状態に初期化しません。
- リンクの状態にはID、ハッシュ、時刻、再送用の世代情報を保存します。元のメッセージ本文は保存しません。
- 通常の重複イベントはまとめて処理し、Discord側のnonce重複抑止も使います。送信成功直後・永続保存前のプロセスクラッシュまで含めた厳密な一回だけの送信は保証しません。
- リンク送信後に保存が失敗した場合は、同じプロセス内では成功済み返信IDを保持し、再送せずに保存を再試行します。
- キュー満杯はログへ出します。チャンネル管理は定期スキャンで再取得します。リンクの新規投稿がキューへ入る前にあふれた場合、その未追跡投稿は履歴から自動再取得しません。
- SIGINT / SIGTERMで処理を止め、状態ファイルのロックを解放します。ログはJSON形式で標準出力へ出し、設定したBotトークンを伏せます。

## 8. テストと確認範囲

```bash
go test ./...
go test -race ./...
go vet ./...
```

権限の競合、監査ログの誤対応防止、保存失敗、再起動時の復旧、URLの誤認・展開除外、並行イベント、Discord RESTの送信内容をテストしています。RESTアダプターのテストはHTTPをモック化し、SDKが実際に作るリクエストを検証します。

本番環境での動作は、テスト用カテゴリーでのチャンネル作成と権限確認、リンク投稿の作成・編集・削除を通じて検証してください。CIのテストではDiscord REST APIをモック化しています。

依存ライブラリのライセンスは `licenses/` に収録しています。

## 9. GitHub Actions

`.github/workflows/ci.yml` は、`main` へのpush、Pull Request、手動実行で次を検証します。

- Goのフォーマットと依存関係の整合性
- `go vet ./...`
- `go test -race ./...`
- 両Botのビルド

成功したrunのArtifactsから、Linux amd64向けの実行ファイルとチェックサムを含むアーカイブを取得できます。Artifactの保持期間は7日です。トークンを使うDiscord接続や本番へのデプロイは行いません。

既定は `ubuntu-24.04` です。セルフホストrunnerを使う場合は、リポジトリのActions変数 `CI_RUNNER_LABELS` に、例えば次のJSON配列を設定します。

```json
["self-hosted", "Linux", "X64"]
```

Linux x86-64のrunnerに、更新済みのGitHub Actions runner、Git、Bash、Make、Cコンパイラ（race検査用）、tarを用意してください。Goはsetup-goでインストールします。外部からのPull Requestをセルフホストrunnerで実行する場合は、信頼できるコードだけを実行する運用にしてください。

## 10. 主な公式資料

- [Discord permissions](https://docs.discord.com/developers/topics/permissions)
- [Discord channel API](https://docs.discord.com/developers/resources/channel)
- [Discord audit logs](https://docs.discord.com/developers/resources/audit-log)
- [Discord Gateway intents](https://docs.discord.com/developers/events/gateway#message-content-intent)
- [Discord message API](https://docs.discord.com/developers/resources/message)
- [disgo](https://github.com/disgoorg/disgo)
- [FxEmbed / FxTwitter](https://github.com/FxEmbed/FxEmbed)
