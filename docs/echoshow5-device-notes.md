# Echo Show 5 (2nd Gen / cronos) BLU + LineageOS 18.1 化プロジェクト

参考: note.com/neen696/n/n1350c0af3a28 (更新日 2026-08-13)
一次情報: XDA [UNLOCK][ROOT][TWRP][UNBRICK] Thread / LineageOS 18.1 cronos Thread

## 目標
Echo Show 5 2nd gen (cronos) を:
1. BLU (ブートローダーアンロック) + TWRP 導入
2. LineageOS 18.1 (Android 11) をインストールして Android 化

## 実績 (2026-09-15)
- 新機体 `G091MK0614440194` Android化完了
  - FireOS 6.7.5.3 (Amazonログイン済) → Fastboot `CRONOS / unlock:false / lk:44072a3-20240709_162755`
  - `amonet-cronos-v2.0.1 / fastbrick.img` でBLU+TWRP 3.7.0_9-0 成功 (自動でomni_cronos recoveryに再起動)
  - バックアップ `backup/G091MK0614440194/` に15パーティション全数SHA256検証済みで保存 (userdataは実サイズ4042543 blocks / 4139564544 bytesで再取得)
  - `lineage-18.1-20260904-UNOFFICIAL-cronos.zip (v0.4)` をpush→`twrp install`成功→Format Data→Reboot
  - 初回起動確認: `lineage_cronos-userdebug 11 RQ3A.211001.001 eng.r0rt1z.20260905 / 18.1-20260905-UNOFFICIAL-cronos / boot_completed=1`
  - 注意: 2回目のFormat Dataで/sdcard内lineage.zipは消える (正常)
  - Magisk 30.7 (30700) 導入済み: Magisk.apkをTWRP installでbootパッチ→daemon稼働確認(`magisk -v`=30.7:MAGISK:D)
    - stock: `backup/G091MK0614440194/boot_los_stock.img` (af939c82…)
    - patched: `backup/G091MK0614440194/boot_magisk_30.7.img` (33a43bf3…)

## 重要: 前提条件 (必須・BLU前に確認)
- [ ] Amazon アカウントへ登録済み・初期セットアップ完了済みであること
      (未登録のまま BLU すると、後で FireOS に戻しても OOBE がクラッシュして使えなくなる)
- [ ] FireOS が 6.5.7.0 より新しい (最新) であること
      (XDA 曰く「6.5.7.0 より新しいバージョンならすべて対応」)
- [ ] リスク承知: 失敗すると永久ブリックの可能性 (USBDL無い、復旧不可)
      / LK・Preloader・TEE の旧バージョン書き込みは絶対禁止
- [ ] micro-USB ケーブルがデータ転送対応であること (充電専用ケーブルはNG)
      → 手順途中でケーブルを抜かない・電源を切らない (HardBrick)

## ダウンロード済み (SHA256 検証済み)
| ファイル | 状態 | SHA256 |
|---|---|---|
| lineage-18.1-20260904-UNOFFICIAL-cronos.zip (v0.4, 2026-09-04) | ✓ | 4c355998061a454792128d4b730932b47ed05a3d2a6d2628599218f44cc84678 |
| lineage-18.1-20260624-UNOFFICIAL-cronos.zip (v0.3, 2026-06-24) | ✓ | a7ae5375798ac00d6c1c30b4036a291feaf2e54fc941efa79a3772f281f8b114 |

※ note記事は v0.3 基準、XDA 現行は v0.4。**v0.4 を使用推奨** (内容は XDA ROM スレッド参照)。

## 必要 (ユーザーが入手して配置)
- [ ] **amonet-cronos-v2.0.1.zip** を XDA からダウンロードし
      `~/echoshow5/dl/` に配置
      URL: https://xdaforums.com/attachments/amonet-cronos-v2-0-1-zip.6373838/
      (XDA ログインが必要。添付ファイル欄の最新版 amonet-cronos-v2.0.1.zip)
      ※ note記事の v1.1.4 より新しく、FireOS 復元方式が改善されている

## オプション
- [ ] MindTheGapps-11.0.0-arm (Google アプリ群、任意。要らなければ F-Droid 等で代替可)
- [ ] Magisk (root 強化、任意。LineageOS 初回起動後に実施)

## 手順 (v2.0.1 ベース)
### Phase 1: BLU + TWRP
1. AC 電源接続状態で 3ボタン (音量+ / 音量- / ミュート) 長押し
   → 画面に "=> FASTBOOT mode..." 表示
2. micro-USB で Mac に接続 → `fastboot devices` で認識確認
3. `./fastbrick.sh` 実行 → デバイスのファームウェアバージョン確認
   → 正しいイメージ名 ("Fire OS 6.5.7.0 or newer" = full-2024xx.img) が表示されたら YES
4. 10秒猶予後は絶対に中断しない (5分程度) → TWRP に自動再起動
5. TWRP 初回起動時 "Allow Modifications" をスワイプ

### Phase 2: バックアップ (重要)
6. TWRP で Backup (Boot / System / Data 等) を作成 → PC に退避
   (または TWRP ターミナルで dd によるパーティションダンプ)
   ※ v2.0.1 では update.bin による FireOS 復元も可能だが、バックアップは二重の保険

### Phase 3: LineageOS 18.1 インストール (XDA の手順を厳守)
7. TWRP → Wipe > Format Data > yes
8. TWRP → Wipe > Advanced Wipe (Data / System / Cache)
9. ADB Sideload で ROM zip を送信 (`adb sideload <rom>.zip`)
10. TWRP → Install で ROM を選択してフラッシュ
11. TWRP → Wipe > Format Data > yes (再度)
12. TWRP → Reboot > System (初回起動 5〜10分)
13. (任意) Magisk / Gapps / F-Droid 等

## 既知の問題 (LineageOS cronos)
- カメラ動作しない / マイク音量が小さい場合あり / SELinux Permissive (機密データ禁止)
- ディープスリープ無効 / バッテリー常時100%表示 / **ミュートボタン = 電源ボタン**
- 操作は scrcpy を使うと便利
- ROM は 2021 年製 2nd gen (cronos) 専用

## 復元 (FireOS へ戻す)
- TWRP 起動 (電源接続時に 音量+ 長押し) → Factory Reset → stock update.bin を install
  (ftvdb.com 等から入手、.bin → .zip にリネームが必要)