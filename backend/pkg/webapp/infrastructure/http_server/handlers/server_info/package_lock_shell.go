package server_info

import "strings"

// packageLockPlaceholder marks where install.sh and uninstall.sh take the
// shared package-lock functions. Both scripts touch the package manager, and
// both used to give up on a held lock in their own way: the installer after
// five minutes, the uninstaller silently, reporting a removal that had not
// happened.
const packageLockPlaceholder = "__PACKAGE_LOCK_FUNCTIONS__"

// packageLockShell waits for the package manager the way the agent's own
// package operations do, and names who holds the lock while it waits.
//
// The bound is longer than the agent's (25 minutes) on purpose. The usual
// holder is the distribution's first automatic upgrade on a freshly installed
// system, which upgrades the whole image and can run for close to half an
// hour. The agent bounds its wait because every other command for the node
// queues behind it; nothing queues behind these scripts, and an operator who
// pasted one command should not have to run it twice. It is still a bound,
// and the holder is never killed: interrupting dpkg mid-unpack is how a
// system gets broken.
//
// The holder is read from /proc/locks, which lists every POSIX lock with the
// pid that holds it, so naming it never competes for the lock. The variables
// are plain assignments so a test can point them at a fake /proc and a short
// clock.
const packageLockShell = `PKG_LOCK_FILES="/var/lib/dpkg/lock-frontend /var/lib/dpkg/lock"
PKG_PROC=/proc
PKG_LOCK_MAX=2700
PKG_LOCK_POLL=5
PKG_LOCK_REPORT_EVERY=60
PKG_LOCK_WAITED=0
PKG_LOCK_TIMED_OUT=0
PKG_ERR=""

# pkg_lock_holder prints who holds the package lock ("unattended-upgrade
# (pid 1234)"), or nothing when it cannot tell.
pkg_lock_holder() {
  [ -r "$PKG_PROC/locks" ] || return 0
  for _pkg_f in $PKG_LOCK_FILES; do
    [ -e "$_pkg_f" ] || continue
    # Two fixed paths; ls -i is the inode read every userland has.
    # shellcheck disable=SC2012
    _pkg_ino=$(ls -di "$_pkg_f" 2>/dev/null | awk '{print $1}')
    [ -n "$_pkg_ino" ] || continue
    _pkg_pid=$(awk -v ino="$_pkg_ino" '$2 != "->" { n = split($6, d, ":"); if (d[n] == ino) { print $5; exit } }' "$PKG_PROC/locks")
    case "$_pkg_pid" in
      '') continue ;;
      *[!0-9]*) echo "another process"; return 0 ;;
    esac
    # The program, or for an interpreter (unattended-upgrade runs under
    # python3) the script it runs. Inline code (-c) is never printed.
    _pkg_name=$(tr '\000' '\n' < "$PKG_PROC/$_pkg_pid/cmdline" 2>/dev/null | awk '
      function base(s) { sub(/.*\//, "", s); return s }
      NR == 1 { prog = base($0); if (prog !~ /^(python|perl|sh|bash|dash)[0-9.]*$/) { print prog; done = 1; exit } next }
      $0 == "" { next }
      mod || dd { print base($0); done = 1; exit }
      $0 == "-m" { mod = 1; next }
      $0 == "--" { dd = 1; next }
      /^-[^-]*c/ { print prog; done = 1; exit }
      /^-/ { next }
      { print base($0); done = 1; exit }
      END { if (!done && prog != "") print prog }')
    if [ -n "$_pkg_name" ]; then
      echo "$_pkg_name (pid $_pkg_pid)"
    else
      echo "process $_pkg_pid"
    fi
    return 0
  done
}

# pkg_run runs one package command, retrying while another process holds the
# package lock. Every call draws on ONE budget, so a sequence of package steps
# never waits longer than PKG_LOCK_MAX in total. On failure PKG_ERR holds the
# command's own message, and PKG_LOCK_TIMED_OUT=1 says the lock was the cause.
pkg_run() {
  _pkg_since_report=-1
  while :; do
    if PKG_ERR=$("$@" 2>&1 >/dev/null); then
      return 0
    fi
    case "$PKG_ERR" in
      *lock*) ;;
      *) return 1 ;;
    esac
    _pkg_holder=$(pkg_lock_holder)
    [ -n "$_pkg_holder" ] || _pkg_holder="another process"
    if [ "$PKG_LOCK_WAITED" -ge "$PKG_LOCK_MAX" ]; then
      PKG_LOCK_TIMED_OUT=1
      PKG_ERR="the package lock was still held by $_pkg_holder after $((PKG_LOCK_MAX / 60)) minutes"
      return 1
    fi
    if [ "$_pkg_since_report" -lt 0 ] || [ "$_pkg_since_report" -ge "$PKG_LOCK_REPORT_EVERY" ]; then
      echo "Waiting for $_pkg_holder to release the package lock, $((PKG_LOCK_WAITED / 60))m elapsed (giving up after $((PKG_LOCK_MAX / 60))m); a fresh system's first upgrade can take up to half an hour."
      _pkg_since_report=0
    fi
    sleep "$PKG_LOCK_POLL"
    PKG_LOCK_WAITED=$((PKG_LOCK_WAITED + PKG_LOCK_POLL))
    _pkg_since_report=$((_pkg_since_report + PKG_LOCK_POLL))
  done
}`

// withPackageLockShell substitutes the shared functions into a script.
func withPackageLockShell(script string) string {
	return strings.ReplaceAll(script, packageLockPlaceholder, packageLockShell)
}
