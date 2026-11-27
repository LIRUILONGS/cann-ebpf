#!/bin/sh
# cann-ebpf stacks 的只读预检。
#
# 本脚本只读取 /proc、/sys 和当前进程的能力位；不 attach、不加载 BPF、
# 不打开 perf event、不修改 sysctl、不重启服务、不写业务目录。
#
# 退出码：
#   0  环境满足采样和符号化的硬性条件
#   1  存在阻断项（不能据此宣称可采）
#   2  参数错误

set -eu

PROG=${0##*/}
PIDS=
FAIL=0
WARN=0

usage() {
    cat <<'EOF'
用法:
  scripts/stacks_preflight.sh --pid <host-pid> [--pid <host-pid> ...]

选项:
  --pid <pid>   目标 vLLM 的宿主 PID；可重复指定多个宿主 TGID
  -h, --help    显示本帮助

说明:
  - 只读预检，不采样、不 attach、不加载 BPF。
  - 容器内看到的 PID 不是宿主 PID；请先在宿主机确认 Tgid/NSpid。
  - bpftool 和 clang 只检查是否存在，不执行、不要求目标机安装。

退出码:
  0=可采  1=有阻断项  2=参数错误
EOF
}

say() {
    printf '%s\n' "$*"
}

ok() {
    printf '[ OK ] %s\n' "$*"
}

warn() {
    WARN=$((WARN + 1))
    printf '[WARN] %s\n' "$*"
}

block() {
    FAIL=$((FAIL + 1))
    printf '[FAIL] %s\n' "$*"
}

section() {
    printf '\n== %s ==\n' "$*"
}

add_pid() {
    value=$1
    case "$value" in
        ''|*[!0-9]*)
            say "$PROG: --pid 必须是正整数: $value" >&2
            exit 2
            ;;
    esac
    if [ "$value" -le 0 ]; then
        say "$PROG: --pid 必须大于 0: $value" >&2
        exit 2
    fi
    case " $PIDS " in
        *" $value "*) ;;
        *) PIDS="${PIDS}${PIDS:+ }$value" ;;
    esac
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --pid)
            if [ "$#" -lt 2 ]; then
                say "$PROG: --pid 缺少参数" >&2
                exit 2
            fi
            add_pid "$2"
            shift 2
            ;;
        --pid=*)
            add_pid "${1#--pid=}"
            shift
            ;;
        *)
            say "$PROG: 未知参数: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [ -z "$PIDS" ]; then
    say "$PROG: 至少需要一个 --pid <host-pid>" >&2
    usage >&2
    exit 2
fi

section '基本信息'
say "time_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || printf 'unknown')"
say "script=$0"
say "hostname=$(hostname 2>/dev/null || uname -n 2>/dev/null || printf 'unknown')"
say "uname=$(uname -a 2>/dev/null || printf 'unknown')"

KREL=$(uname -r 2>/dev/null || printf 'unknown')
OS_NAME=$(uname -s 2>/dev/null || printf 'unknown')
ARCH=$(uname -m 2>/dev/null || printf 'unknown')
say "kernel_release=$KREL"
say "arch=$ARCH"

if [ "$OS_NAME" = "Linux" ]; then
    ok "运行在 Linux"
else
    block "当前不是 Linux（$OS_NAME），cann-ebpf stacks 不能在目标模式运行"
fi

NPROC=$(nproc 2>/dev/null || true)
if [ -z "$NPROC" ]; then
    NPROC=$(getconf _NPROCESSORS_ONLN 2>/dev/null || true)
fi
if [ -z "$NPROC" ]; then
    NPROC='?'
fi
say "nproc=$NPROC"

if [ -r /proc/meminfo ]; then
    MEM_TOTAL_KB=$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo 2>/dev/null || true)
    MEM_AVAILABLE_KB=$(awk '/^MemAvailable:/ {print $2; exit}' /proc/meminfo 2>/dev/null || true)
    say "mem_total_kb=${MEM_TOTAL_KB:-unknown}"
    say "mem_available_kb=${MEM_AVAILABLE_KB:-unknown}"
else
    warn "无法读取 /proc/meminfo，内存信息未知"
fi

MY_UID=$(id -u 2>/dev/null || true)
if [ -z "$MY_UID" ]; then
    MY_UID='?'
fi
say "euid=$MY_UID"

CAP_EFF=$(awk '/^CapEff:/ {print $2; exit}' /proc/self/status 2>/dev/null || true)
if [ -n "$CAP_EFF" ]; then
    say "capeff=$CAP_EFF"
else
    warn "无法读取 CapEff；权限判断可能不完整"
fi

cap_has() {
    # cap_has <linux-capability-number>；0 表示未设置，1 表示已设置。
    [ -n "$CAP_EFF" ] || return 1
    cap_bit=$1
    [ $((0x$CAP_EFF & (1 << cap_bit))) -ne 0 ]
}

if [ "$MY_UID" = "0" ]; then
    say "privilege=euid0"
else
    say "privilege=non-root"
fi

PRIV_OK=0
if [ "$MY_UID" = "0" ] || cap_has 38 || cap_has 21; then
    PRIV_OK=1
fi

section 'perf_event_open / BPF 权限'
PERF_PARANOID_FILE=/proc/sys/kernel/perf_event_paranoid
if [ -r "$PERF_PARANOID_FILE" ]; then
    PERF_PARANOID=$(tr -d '[:space:]' < "$PERF_PARANOID_FILE" 2>/dev/null || true)
else
    PERF_PARANOID=''
fi
say "perf_event_paranoid=${PERF_PARANOID:-unknown}"

if [ -z "$PERF_PARANOID" ]; then
    block "无法读取 $PERF_PARANOID_FILE；不能确认 perf_event_open 权限"
else
    case "$PERF_PARANOID" in
        0|1)
            ok "perf_event_paranoid=$PERF_PARANOID，满足非 root 采样的常规条件"
            ;;
        2)
            if [ "$PRIV_OK" -eq 1 ]; then
                warn "perf_event_paranoid=2；root/CAP_PERFMON 通常仍可打开，但容器/seccomp/LSM 仍可能 EPERM，需实测"
            else
                block "perf_event_paranoid=2 且没有 root/PERFMON 权限；perf_event_open 预计 EPERM"
            fi
            ;;
        *)
            if [ "$PRIV_OK" -eq 1 ]; then
                warn "perf_event_paranoid=$PERF_PARANOID；权限假设成立时不阻断，但必须处理 EPERM"
            else
                block "perf_event_paranoid=$PERF_PARANOID 且没有足够权限"
            fi
            ;;
    esac
fi

if [ -r /proc/sys/kernel/perf_event_mlock_kb ]; then
    say "perf_event_mlock_kb=$(cat /proc/sys/kernel/perf_event_mlock_kb 2>/dev/null || printf 'unknown')"
else
    warn "/proc/sys/kernel/perf_event_mlock_kb 不存在；map 加载失败时要检查 memlock"
fi

MEMLOCK=$(ulimit -l 2>/dev/null || true)
say "rlimit_memlock=${MEMLOCK:-unknown}"

LOCKDOWN_FILE=/sys/kernel/security/lockdown
if [ -r "$LOCKDOWN_FILE" ]; then
    LOCKDOWN=$(cat "$LOCKDOWN_FILE" 2>/dev/null || true)
    say "lockdown=${LOCKDOWN:-unknown}"
    case "$LOCKDOWN" in
        *'[integrity]'*|*'[confidentiality]'*)
            block "lockdown 为 integrity/confidentiality，BPF/perf 可能被禁止"
            ;;
        *)
            ok "lockdown 未显示 integrity/confidentiality"
            ;;
    esac
else
    warn "未读到 $LOCKDOWN_FILE；lockdown 状态未知"
fi

UNPRIV_BPF=''
if [ -r /proc/sys/kernel/unprivileged_bpf_disabled ]; then
    UNPRIV_BPF=$(tr -d '[:space:]' < /proc/sys/kernel/unprivileged_bpf_disabled 2>/dev/null || true)
    say "unprivileged_bpf_disabled=${UNPRIV_BPF:-unknown}"
else
    warn "/proc/sys/kernel/unprivileged_bpf_disabled 不存在"
fi

BPF_OK=0
if [ "$MY_UID" = "0" ] || cap_has 39 || cap_has 21; then
    BPF_OK=1
fi
if [ "$BPF_OK" -eq 1 ]; then
    ok "具备加载 BPF 的粗粒度权限（euid0/CAP_BPF/CAP_SYS_ADMIN）"
else
    block "没有 euid0/CAP_BPF/CAP_SYS_ADMIN；非 root 通常无法加载 perf_event BPF 程序"
fi

if [ "$UNPRIV_BPF" = "0" ]; then
    say "说明: unprivileged_bpf_disabled=0，但 perf_event BPF 仍可能要求 CAP_BPF/CAP_PERFMON"
fi

BPF_SYSCALL_FOUND=0
if [ -r /proc/kallsyms ]; then
    if awk '$3 ~ /^(__arm64_sys_bpf|__x64_sys_bpf|__sys_bpf)$/ {found=1; exit} END {exit !found}' /proc/kallsyms 2>/dev/null; then
        BPF_SYSCALL_FOUND=1
    fi
fi
if [ "$BPF_SYSCALL_FOUND" -eq 1 ]; then
    ok "内核符号中存在 bpf 系统调用"
else
    block "未在 /proc/kallsyms 找到 bpf 系统调用符号；不能确认 BPF syscall 可用"
fi

if [ -d /sys/fs/bpf ]; then
    if awk '$2 == "/sys/fs/bpf" && $3 == "bpf" {found=1; exit} END {exit !found}' /proc/mounts 2>/dev/null; then
        ok "/sys/fs/bpf 已挂载为 bpf 文件系统"
    else
        block "/sys/fs/bpf 存在但未以 bpf 文件系统挂载（验收要求 BPF fs 可用；若实现不 pin，仍先按阻断处理）"
    fi
else
    block "/sys/fs/bpf 不存在"
fi

if [ -r /sys/kernel/btf/vmlinux ]; then
    ok "内核 BTF 存在: /sys/kernel/btf/vmlinux"
else
    warn "未找到 /sys/kernel/btf/vmlinux；当前 BPF 对象不依赖 BTF/CO-RE 时可继续，需实机确认"
fi

section 'kallsyms / 模块符号'
KPTR=''
if [ -r /proc/sys/kernel/kptr_restrict ]; then
    KPTR=$(tr -d '[:space:]' < /proc/sys/kernel/kptr_restrict 2>/dev/null || true)
    say "kptr_restrict=${KPTR:-unknown}"
else
    warn "无法读取 /proc/sys/kernel/kptr_restrict"
fi

KALL_N=0
KALL_NZ=0
KALL_Z=0
if [ -r /proc/kallsyms ]; then
    KALL_STATS=$(awk 'NR <= 200 && NF >= 3 { n++; if ($1 ~ /^0+$/) z++; else nz++ } END { printf "%d %d %d", n + 0, nz + 0, z + 0 }' /proc/kallsyms 2>/dev/null || true)
    if [ -n "$KALL_STATS" ]; then
        set -- $KALL_STATS
        KALL_N=$1
        KALL_NZ=$2
        KALL_Z=$3
    fi
    say "kallsyms_sample_lines=$KALL_N"
    say "kallsyms_nonzero_addresses=$KALL_NZ"
    say "kallsyms_zero_addresses=$KALL_Z"
    if [ "$KALL_N" -lt 5 ]; then
        block "/proc/kallsyms 样例不足，无法确认符号化可用"
    elif [ "$KALL_NZ" -eq 0 ]; then
        block "/proc/kallsyms 前 200 行地址全为 0；kptr 限制或权限导致符号化不可用"
    else
        ok "/proc/kallsyms 有非零地址，地址到符号的映射具备基本条件"
    fi
else
    block "无法读取 /proc/kallsyms"
fi

case "$KPTR" in
    0)
        if [ "$KALL_NZ" -gt 0 ]; then
            ok "kptr_restrict=0"
        else
            warn "kptr_restrict=0，但 kallsyms 样例仍全为 0；WSL/加固策略或内核配置仍可能禁止符号化"
        fi
        ;;
    1|2)
        if [ "$KALL_NZ" -gt 0 ]; then
            ok "kptr_restrict=$KPTR，但当前 kallsyms 地址仍可见（可能具备 CAP_SYSLOG）"
        elif cap_has 34; then
            warn "kptr_restrict=$KPTR 且样例仍全 0；即使有 CAP_SYSLOG，kptr_restrict=2 也可能归零"
        else
            warn "kptr_restrict=$KPTR 且没有 CAP_SYSLOG；符号覆盖率会下降，报告必须标记不可用"
        fi
        ;;
    '')
        warn "kptr_restrict 未知；按 kallsyms 实际样例判断"
        ;;
    *)
        warn "kptr_restrict=$KPTR 不是常见值；按 kallsyms 实际样例判断"
        ;;
esac

if [ -r /proc/modules ]; then
    MODULE_COUNT=$(awk 'NF > 0 {n++} END {print n + 0}' /proc/modules 2>/dev/null || true)
    say "module_count=${MODULE_COUNT:-unknown}"
    if [ "${MODULE_COUNT:-0}" -eq 0 ]; then
        warn "/proc/modules 为空；使用模块地址的 NPU/驱动符号可能不可见"
    else
        ok "/proc/modules 可读，模块符号具备基本来源"
    fi
else
    warn "无法读取 /proc/modules；模块化 NPU 驱动可能显示为 [unknown]"
fi

section '目标 PID 与命名空间'
for pid in $PIDS; do
    proc_dir=/proc/$pid
    if [ ! -d "$proc_dir" ]; then
        block "PID $pid 不存在或已退出；必须在目标进程存活时采样"
        continue
    fi

    status_file=$proc_dir/status
    if [ ! -r "$status_file" ]; then
        block "PID $pid 的 /proc/$pid/status 不可读"
        continue
    fi

    NAME=$(awk '/^Name:/ {sub(/^Name:[[:space:]]*/, ""); print; exit}' "$status_file" 2>/dev/null || true)
    STATE=$(awk '/^State:/ {print $2; exit}' "$status_file" 2>/dev/null || true)
    TGID=$(awk '/^Tgid:/ {print $2; exit}' "$status_file" 2>/dev/null || true)
    THREADS=$(awk '/^Threads:/ {print $2; exit}' "$status_file" 2>/dev/null || true)
    NSPID=$(awk '/^NSpid:/ {sub(/^NSpid:[[:space:]]*/, ""); print; exit}' "$status_file" 2>/dev/null || true)

    say "pid=$pid name=${NAME:-unknown} state=${STATE:-unknown} tgid=${TGID:-unknown} threads=${THREADS:-unknown}"
    if [ -n "$TGID" ] && [ "$TGID" != "$pid" ]; then
        warn "PID $pid 不是线程组 leader（Tgid=$TGID）；建议改传宿主 Tgid，除非已确认要采该线程"
    fi
    if [ "$STATE" = "Z" ]; then
        block "PID $pid 是 zombie，采样不会得到有效内核栈"
    fi

    if [ -n "$NSPID" ]; then
        NS_COUNT=$(printf '%s\n' "$NSPID" | awk '{print NF; exit}')
        NS_FIRST=$(printf '%s\n' "$NSPID" | awk '{print $1; exit}')
        say "pid=$pid nspid=$NSPID"
        if [ "$NS_COUNT" -gt 1 ]; then
            say "pid=$pid 容器/宿主提示: NSpid 第一列通常是当前 procfs 视角的首个 PID；宿主视角下应与路径 PID 一致"
            if [ "$NS_FIRST" != "$pid" ]; then
                warn "传入的 --pid 与 NSpid 第一列不一致；无法确认它是宿主 PID，请从宿主机复核"
            fi
        else
            warn "PID $pid 只显示一个 NSpid；若脚本在容器内运行，容器 PID 不能直接当作宿主 PID"
        fi
    else
        warn "PID $pid 没有 NSpid 字段；无法判断容器/宿主命名空间"
    fi
done

section '磁盘与可选工具'
AVAIL_KB=$(df -Pk . 2>/dev/null | awk 'NR == 2 {print $4; exit}' || true)
if [ -z "$AVAIL_KB" ]; then
    warn "无法读取当前目录文件系统剩余空间；采样前请手工确认"
elif [ "$AVAIL_KB" -lt 102400 ]; then
    block "当前目录剩余空间小于 100 MiB（${AVAIL_KB} KiB）；不覆盖旧证据的前提下换目录"
else
    AVAIL_MB=$((AVAIL_KB / 1024))
    ok "当前目录剩余空间约 ${AVAIL_MB} MiB"
fi

if command -v bpftool >/dev/null 2>&1; then
    ok "bpftool 可选工具存在: $(command -v bpftool)"
else
    warn "bpftool 未安装（可选；编译/排障时可能需要）"
fi

if command -v clang >/dev/null 2>&1; then
    ok "clang 可选工具存在: $(command -v clang)"
else
    warn "clang 未安装（可选；目标机运行不需要，开发机编译 BPF 对象需要）"
fi

section '结论'
say "blocking=$FAIL warning=$WARN target_pids=$PIDS"
if [ "$FAIL" -gt 0 ]; then
    say 'RESULT=NOT_READY'
    say '先解决上面的 FAIL 项；不要为了通过而修改生产 sysctl、重启服务或伪造采样结果。'
    exit 1
fi

say 'RESULT=READY'
say '静态预检通过；实际 perf_event_open/BPF attach 仍可能受容器/安全模块限制，以采集结果为准。'
say '本脚本只读：未 attach、未加载 BPF、未打开 perf event、未修改 sysctl、未写业务目录。'
exit 0
