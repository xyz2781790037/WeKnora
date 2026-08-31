#!/bin/bash

# Returns success when the external plugin runtime is enabled.
plugin_runtime_is_enabled() {
    local enabled
    enabled=$(printf '%s' "${WEKNORA_PLUGIN_RUNTIME_ENABLED:-false}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
    [ "$enabled" = "true" ]
}

# Loads one shared token for WeKnora and plugin-runtime. Explicit environment
# configuration wins. Otherwise a token is generated once in a private local
# state directory and reused by every later startup.
ensure_plugin_runtime_auth_token() {
    local configured_token
    configured_token="$(printf '%s' "${WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN:-}" | tr -d '\r\n' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    if [ -n "$configured_token" ]; then
        WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN="$configured_token"
        export WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN
        return 0
    fi

    local token_file
    local managed_file=false
    if [ -n "${WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN_FILE:-}" ]; then
        token_file="$WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN_FILE"
        case "$token_file" in
            /*) ;;
            *) token_file="$PROJECT_ROOT/$token_file" ;;
        esac
    else
        token_file="$PROJECT_ROOT/.runtime/plugin-runtime-auth-token"
        managed_file=true
    fi

    if [ "$managed_file" = true ]; then
        local token_dir="$PROJECT_ROOT/.runtime"
        if [ -L "$token_dir" ] || [ -L "$token_file" ]; then
            log_error "plugin-runtime 令牌目录或文件不能是符号链接: $token_file"
            return 1
        fi
        if ! mkdir -p -m 700 "$token_dir" || ! chmod 700 "$token_dir"; then
            log_error "无法创建 plugin-runtime 私有状态目录: $token_dir"
            return 1
        fi
        if [ ! -e "$token_file" ]; then
            local generated_token
            if command -v openssl &> /dev/null; then
                generated_token="$(openssl rand -hex 32)" || return 1
            else
                generated_token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" || return 1
            fi
            if (umask 077; set -C; printf '%s\n' "$generated_token" > "$token_file") 2>/dev/null; then
                log_success "已生成并保存 plugin-runtime 共享令牌"
            elif [ ! -f "$token_file" ]; then
                log_error "无法安全创建 plugin-runtime 共享令牌: $token_file"
                return 1
            fi
        fi
        if ! chmod 600 "$token_file"; then
            log_error "无法收紧 plugin-runtime 共享令牌权限: $token_file"
            return 1
        fi
    fi

    if [ ! -f "$token_file" ] || [ ! -r "$token_file" ]; then
        log_error "plugin-runtime 共享令牌文件不存在或不可读: $token_file"
        return 1
    fi

    WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN="$(tr -d '\r\n' < "$token_file" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    if [ -z "$WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN" ]; then
        log_error "plugin-runtime 共享令牌文件为空: $token_file"
        return 1
    fi
    export WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN
    export WEKNORA_PLUGIN_RUNTIME_AUTH_TOKEN_FILE="$token_file"
    return 0
}
