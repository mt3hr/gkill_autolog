#!/data/data/com.termux/files/usr/bin/sh
# Android 側のスクリーンショットが撮れない原因を切り分けるための情報を集める。
#
#   sh collect_android_diag.sh
#
# /sdcard/gkill_autolog_diag.txt に書き出す。中身を見てから渡すこと。
# パスワードのハッシュは伏せ字にしてあるが、アプリ名や通知の断片が
# ログに含まれることはある。
#
# root が要るのは設定ファイルと logcat の取得。無くても残りは集まる。

out=/sdcard/gkill_autolog_diag.txt
shared=/sdcard/gkill_autolog
pkg=com.mt3hr.gkill_autolog

exec > "$out" 2>&1

echo "===== 収集日時 ====="
date

echo
echo "===== 1. アプリの設定 (これが本命) ====="
# capture_screenshots が false なら、それが答え。
# APK を入れ直すと SharedPreferences ごと消えて既定の false に戻る。
su -c "cat /data/data/$pkg/shared_prefs/gkill_autolog.xml" 2>&1 ||
  echo "!! 読めなかった (root が無いか許可されていない)"

echo
echo "===== 2. root で screencap が通るか ====="
# ここが失敗するなら、撮影できないのは root の許可の問題。
#
# su 側のプロセスに /sdcard へ書かせない。su 経由のシェルは補助グループを
# 持たず /sdcard を開けないことがある (CLAUDE.md の落とし穴。本体アプリも
# cacheDir へ書かせている)。画像は stdout で受け取り、Termux 側で保存する。
diag_shot=/sdcard/gkill_autolog_diag_shot.png
su -c "screencap -p" > "$diag_shot"
if [ -s "$diag_shot" ]; then
  echo "OK: screencap は通る"
  ls -l "$diag_shot"
  rm -f "$diag_shot"
else
  echo "!! screencap が画像を作れなかった"
  rm -f "$diag_shot"
fi

echo
echo "===== 3. 共有ストレージの状況 ====="
# screenshots に溜まっていれば「撮れているが運ばれていない」。
# 空なら「撮れていない」。
for dir in screenshots events gpslog audio; do
  echo "--- $shared/$dir ---"
  ls -l "$shared/$dir" 2>&1 | head -20
  echo "  件数: $(ls -1 "$shared/$dir" 2>/dev/null | wc -l)"
done

echo
echo "===== 4. 収集アプリが動いているか ====="
su -c "dumpsys activity services $pkg" 2>&1 | grep -i "ServiceRecord\|isForeground\|app=" | head -10
echo "--- プロセス ---"
su -c "ps -A" 2>&1 | grep "$pkg" || echo "!! プロセスが見つからない"

echo
echo "===== 5. 権限 ====="
su -c "dumpsys package $pkg" 2>&1 |
  grep -i "MANAGE_EXTERNAL_STORAGE\|ACCESS_FINE_LOCATION\|ACCESS_BACKGROUND_LOCATION\|granted=" | head -20
echo "--- バージョン ---"
su -c "dumpsys package $pkg" 2>&1 | grep -i "versionName\|versionCode\|lastUpdateTime\|firstInstallTime" | head -6

echo
echo "===== 6. アプリのログ ====="
# 撮影の失敗はここに WARN で出る。
su -c "logcat -d -s AutologScreenshot:V AutologService:V AutologLocation:V AutologAudio:V AutologConfig:V" 2>&1 | tail -80

echo
echo "===== 7. Termux 側の autolog 取り込み ====="
# 撮れていても取り込みが止まっていれば gkill には出ないので、こちらも見る。
echo "--- autolog のホーム ---"
ls -l "$HOME/.gkill_autolog" 2>&1 | head -20
echo "--- 直近の取り込みログ ---"
for f in $(ls -t "$HOME/.gkill_autolog/logs/"*.log 2>/dev/null | head -3); do
  echo "=== $f ==="
  tail -40 "$f"
done

echo
echo "===== 8. config.env (パスワードは伏せる) ====="
sed 's/^\(GKILL_[A-Z_]*PASSWORD[A-Z_0-9]*=\).*/\1<伏せ字>/' "$shared/config.env" 2>&1

echo
echo "===== 9. 運搬先 (端末内の gkill) ====="
gkill_server dvnf get 2>&1 | head -3
ls -ld "$HOME/gkill/datas/"*auto* 2>&1 | head -10
