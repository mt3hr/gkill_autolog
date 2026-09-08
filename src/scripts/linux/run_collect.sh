#!/bin/sh
# gkill_autolog の常駐収集（Linux）
#
# autolog.env を環境変数として読み込んでから autolog collect を起動する。
# systemd のユーザーユニット (install_units.sh) はこれ経由で起動する。
#
# collect を素で起動すると autolog.env が効かず、端末名や撮影間隔などの
# 設定が既定値のまま動いてしまう。
#
# 出力は journald が受ける。journalctl --user -u gkill-autolog-collect.service で読む。
# 手で起動したときは端末にそのまま出る。

set -eu

AUTOLOG_SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
export AUTOLOG_SCRIPT_DIR
. "$AUTOLOG_SCRIPT_DIR/_autolog.sh"

env_file=${1:-$AUTOLOG_SCRIPT_DIR/../autolog.env}
autolog_load_env "$env_file"

autolog=$(autolog_find_exe)
echo "$(date +%Y-%m-%dT%H:%M:%S%z) 収集を開始する: $autolog"

# **exec で置き換える。** 間にシェルを挟むと systemd の SIGTERM が
# シェルへ届き、autolog が停止の後始末（collector_stop の書き出し）を
# する前に落ちることがある。collector_stop は normalize が
# 「観測の切れ目」として使う唯一のマーカーで、失うと接続区間が閉じない。
exec "$autolog" collect
