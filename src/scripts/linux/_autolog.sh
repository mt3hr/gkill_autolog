#!/bin/sh
# gkill_autolog の Linux 向けスクリプトの共通処理。
#
# run_collect.sh / run_import.sh から . で読み込む。
# POSIX sh で書く。bash が入っていない環境でも動かすため。

set -eu

# autolog_root はリポジトリの直下を返す。
autolog_root() {
    # このファイルは src/scripts/linux/ にある。
    (cd "$AUTOLOG_SCRIPT_DIR/../../.." && pwd)
}

# autolog_load_env は設定ファイルを環境変数として読み込む。
#
# **実環境変数が既にあればそちらを優先する**（Go 側 config.Load と同じ順）。
# ここで上書きすると、環境変数で運用している構成だけ挙動が変わってしまう。
#
# 値はそのまま渡す。クォートの解釈はしない（Windows 側の Read-GkillEnvFile と同じ）。
# 設定ファイルは Windows と共有しうるので、BOM と CRLF を落としてから読む。
autolog_load_env() {
    env_file=$1
    if [ ! -f "$env_file" ]; then
        echo "設定ファイルが無い: $env_file" >&2
        return 0
    fi

    # 先頭の BOM と行末の CR を落とす。残すと最初の変数名と全部の値が壊れる。
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            ''|'#'*) continue ;;
        esac

        key=${line%%=*}
        [ "$key" = "$line" ] && continue
        value=${line#*=}
        key=$(printf '%s' "$key" | tr -d ' \t')

        # 変数名として使える形だけを受け付ける。
        # 壊れた行をそのまま eval へ渡さないための歯止め。
        case "$key" in
            ''|*[!A-Za-z0-9_]*) continue ;;
            [0-9]*) continue ;;
        esac

        # 既に環境変数があればそのまま。
        if [ -z "$(eval "printf '%s' \"\${$key:-}\"")" ]; then
            export "$key=$value"
        fi
    done <<EOF
$(sed -e '1s/^\xef\xbb\xbf//' -e 's/\r$//' "$env_file")
EOF
}

# autolog_home は生ログとログの置き場を返す。
autolog_home() {
    if [ -n "${AUTOLOG_HOME:-}" ]; then
        printf '%s' "$AUTOLOG_HOME"
    else
        printf '%s' "$HOME/.gkill_autolog"
    fi
}

# autolog_find_exe は実行体を探す。
#
# $AUTOLOG_EXE → release/linux_* → リポジトリ直下 → PATH の順。
# Windows 側の run_collect.ps1 / run_import.ps1 と同じ順序にしてある。
autolog_find_exe() {
    if [ -n "${AUTOLOG_EXE:-}" ]; then
        printf '%s' "$AUTOLOG_EXE"
        return 0
    fi

    root=$(autolog_root)
    for candidate in \
        "$root/release/linux_amd64/autolog" \
        "$root/release/linux_arm64/autolog" \
        "$root/release/linux_arm/autolog" \
        "$root/autolog"
    do
        if [ -x "$candidate" ]; then
            printf '%s' "$candidate"
            return 0
        fi
    done

    if command -v autolog >/dev/null 2>&1; then
        command -v autolog
        return 0
    fi

    echo "autolog の実行体が見つからない。npm run build_linux_amd64 でビルドするか AUTOLOG_EXE を設定すること" >&2
    return 1
}

