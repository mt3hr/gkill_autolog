#!/bin/sh
# systemd のユーザーユニットを置いて有効にする。
#
# 使い方:
#   install_units.sh              置いて有効にする
#   install_units.sh --dry-run    置く内容だけ出す
#   install_units.sh --uninstall  消す
#
# **システムのユニットではなくユーザーのユニットにする。**
# 前面ウィンドウとセッションの状態は、画面付きのログインセッションに
# 属するプロセスからしか取れない。管理者権限は要らない。
#
# gkill の同期スクリプトから run_import.sh を呼んでいるなら、
# 取り込みのタイマーは要らない。

set -eu

AUTOLOG_SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
units="gkill-autolog-collect.service gkill-autolog-import.service gkill-autolog-import.timer"

mode=install
case "${1:-}" in
    --dry-run) mode=dry-run ;;
    --uninstall) mode=uninstall ;;
    '') ;;
    *) echo "知らない引数: $1" >&2; exit 2 ;;
esac

if [ "$mode" = dry-run ]; then
    echo "置き場: $unit_dir"
    for unit in $units; do
        echo "--- $unit"
        # ExecStart のパスをこのリポジトリの場所へ差し替えた内容を出す。
        sed "s|%h/Git/gkill_autolog/src/scripts/linux|$AUTOLOG_SCRIPT_DIR|" "$AUTOLOG_SCRIPT_DIR/$unit"
    done
    exit 0
fi

if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl が無い。ユニットを手で置くか、ほかの常駐の仕組みを使うこと" >&2
    exit 1
fi

if [ "$mode" = uninstall ]; then
    systemctl --user disable --now gkill-autolog-collect.service 2>/dev/null || true
    systemctl --user disable --now gkill-autolog-import.timer 2>/dev/null || true
    for unit in $units; do
        rm -f "$unit_dir/$unit"
    done
    systemctl --user daemon-reload
    echo "ユニットを消した"
    exit 0
fi

mkdir -p "$unit_dir"
for unit in $units; do
    # ExecStart をこのリポジトリの実際の場所へ書き換えて置く。
    # 決め打ちのパスを配らないため。
    sed "s|%h/Git/gkill_autolog/src/scripts/linux|$AUTOLOG_SCRIPT_DIR|" \
        "$AUTOLOG_SCRIPT_DIR/$unit" > "$unit_dir/$unit"
done

systemctl --user daemon-reload
systemctl --user enable --now gkill-autolog-collect.service
systemctl --user enable --now gkill-autolog-import.timer

echo "ユニットを置いた: $unit_dir"
echo "状態: systemctl --user status gkill-autolog-collect.service"
echo "ログ: journalctl --user -u gkill-autolog-collect.service -f"
