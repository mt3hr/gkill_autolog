#!/bin/sh
# gkill_autolog の取り込み（Linux）
#
# autolog.env を環境変数として読み込んでから autolog import を実行する。
# systemd のタイマー (install_units.sh) と、gkill の同期スクリプトから呼ぶ。
#
# 使い方:
#   run_import.sh                 上限時刻は autolog の既定（直近の午前4時）
#   run_import.sh --until-now     「いま」の少し手前まで
#   run_import.sh --dry-run       書き込まずに内容だけ出す
#
# 出力は $AUTOLOG_HOME/logs/import_YYYYMMDD.log にも残す。

set -eu

AUTOLOG_SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
export AUTOLOG_SCRIPT_DIR
. "$AUTOLOG_SCRIPT_DIR/_autolog.sh"

env_file=""
set -- "$@"
args=""

# 引数はほぼそのまま autolog へ渡す。--env-file だけこちらで受ける。
parsed=""
while [ $# -gt 0 ]; do
    case "$1" in
        --env-file)
            shift
            env_file=${1:-}
            ;;
        *)
            parsed="$parsed $1"
            ;;
    esac
    shift
done

[ -z "$env_file" ] && env_file="$AUTOLOG_SCRIPT_DIR/../autolog.env"
autolog_load_env "$env_file"

# shellcheck disable=SC2086
set -- import $parsed

# --dry-run が付いていなければ、書き込みに使うパスワードを確かめる。
# 設定漏れのまま取り込みが静かに失敗し続けるのを防ぐ。
case " $parsed " in
    *" --dry-run "*) ;;
    *)
        if [ -z "${GKILL_AUTO_PASSWORD_SHA256:-}" ] && [ -z "${GKILL_PASSWORD_SHA256:-}" ]; then
            echo "パスワードが設定されていないため取り込まない。autolog.env を確かめること" >&2
            exit 1
        fi
        ;;
esac

autolog=$(autolog_find_exe)

log_file="$(autolog_home)/logs/import_$(date +%Y%m%d).log"
mkdir -p "$(dirname "$log_file")"

echo "$(date +%Y-%m-%dT%H:%M:%S%z) 取り込みを開始する: $autolog $*" | tee -a "$log_file"

# パイプの終了コードは最後のコマンドのものになるので、
# autolog 自身の終了コードを別に受け取る。POSIX sh には PIPESTATUS が無い。
#
# `|| echo $?` の形にするのは set -e のため。`; echo $?` と書くと、
# 失敗した時点でこの中括弧の中が終わり、終了コードを書く前に抜けてしまう。
# 成功したときは何も書かれないので、空を 0 として読む。
status_file=$(mktemp)
trap 'rm -f "$status_file"' EXIT

{ "$autolog" "$@" 2>&1 || echo $? > "$status_file"; } | tee -a "$log_file"
status=$(cat "$status_file")
[ -z "$status" ] && status=0

if [ "$status" -ne 0 ]; then
    # 失敗しても生ログは残る。次回やり直される。
    echo "$(date +%Y-%m-%dT%H:%M:%S%z) 取り込みに失敗した (終了コード $status)。次回やり直される" \
        | tee -a "$log_file" >&2
    exit "$status"
fi
echo "$(date +%Y-%m-%dT%H:%M:%S%z) 取り込みが終わった" | tee -a "$log_file"
