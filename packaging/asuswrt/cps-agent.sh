#!/bin/sh
# cps-agent on an ASUS router or AiMesh node without the koolshare software center.
# The agent, its config and credential live in this directory (on /jffs, so they survive
# reboots); the log is in RAM.
#
#   cps-agent.sh enroll <server-url> <token> [site] [agent-id]
#   cps-agent.sh start | stop | restart | status | log
#   cps-agent.sh autostart on|off      start on boot (Asuswrt-Merlin: services-start)
#   cps-agent.sh uninstall
#
# Optional settings in ./settings (shell syntax), read on every start:
#   HOST=192.168.50.2     address the server dials (default: this device's LAN address)
#   BASE_PORT=19100       first local port, one per printer
DIR=$(cd "$(dirname "$0")" && pwd)
SELF=$DIR/cps-agent.sh
BIN=$DIR/cps-agent
CONF=$DIR/agent.json
SETTINGS=$DIR/settings
LOG=/tmp/cps-agent.log
PIDFILE=/var/run/cps-agent.pid
ENROLL=/tmp/cps-agent.enroll
LOG_MAX=262144
HOOK=/jffs/scripts/services-start

log() { echo "$(date '+%F %T') [cps-agent.sh] $*" >> $LOG; }
say() { echo "$*"; log "$*"; }

running() {
	[ -f $PIDFILE ] && kill -0 $(cat $PIDFILE) 2>/dev/null
}

# The agent appends (>>), so the file can be cut in place while it runs.
trim_log() {
	[ "$(wc -c < $LOG)" -gt $LOG_MAX ] || return 0
	tail -c $((LOG_MAX / 2)) $LOG > $LOG.tmp
	cat $LOG.tmp > $LOG
	rm -f $LOG.tmp
}

# conf_str <field>: a string field from agent.json (one field per line, two spaces in).
conf_str() {
	[ -f $CONF ] && sed -n "s/^  \"$1\": \"\(.*\)\",\{0,1\}\$/\1/p" $CONF
}

# Supervisor: restarts the agent when it exits and keeps the log small.
supervise() {
	trap '' HUP
	child=
	trap '[ -n "$child" ] && kill $child 2>/dev/null; rm -f $PIDFILE; exit 0' TERM INT
	echo $$ > $PIDFILE
	while :; do
		HOST= BASE_PORT=
		[ -f $SETTINGS ] && . $SETTINGS
		# The LAN address unless set: the agent would otherwise pick the first interface
		# it finds, which on a router can be the WAN.
		set -- -config $CONF -host "${HOST:-$(nvram get lan_ipaddr)}"
		[ -n "$BASE_PORT" ] && set -- "$@" -listen-base "$BASE_PORT"
		enrolling= SERVER= TOKEN= SITE= ID=
		if [ -s $ENROLL ]; then
			. $ENROLL
			rm -f $ENROLL
			enrolling=1
			set -- "$@" -enroll "$TOKEN"
			[ -n "$SITE" ] && set -- "$@" -site "$SITE"
			[ -n "$ID" ] && set -- "$@" -id "$ID"
		elif [ ! -f $CONF ]; then
			log "not enrolled: run cps-agent.sh enroll <server-url> <token>"
			break
		fi

		CPS_AGENT_SERVER="$SERVER" $BIN "$@" >> $LOG 2>&1 &
		child=$!
		while kill -0 $child 2>/dev/null; do
			# The config is written once enrollment succeeds: the previous one is obsolete.
			[ -n "$enrolling" -a -f $CONF ] && rm -f $CONF.old && enrolling=
			trim_log
			sleep 5 &
			wait $!
		done
		wait $child
		rc=$?
		child=

		if [ -f $CONF ]; then
			rm -f $CONF.old
		elif [ -n "$enrolling" -a -f $CONF.old ]; then
			mv -f $CONF.old $CONF
			log "enrollment failed (exit $rc): kept the previous enrollment"
			continue
		fi
		if [ ! -f $CONF ]; then
			log "enrollment failed (exit $rc): check the server URL and the token"
			break
		fi
		# It also exits when it finds no printer: retrying picks one up once plugged in.
		log "cps-agent exited (exit $rc), restarting in 30s"
		sleep 30 &
		wait $!
	done
	rm -f $PIDFILE
}

start() {
	running && { echo "already running"; return 0; }
	if [ ! -f $CONF -a ! -s $ENROLL ]; then
		echo "not enrolled: run $SELF enroll <server-url> <token>"
		return 1
	fi
	say "starting"
	# Asus firmware loads the USB printer driver only for its own printer server; once
	# loaded, a printer plugged in later is picked up too.
	lsmod | grep -q '^usblp' || modprobe usblp 2>/dev/null || log "could not load usblp: USB printers won't be found"
	"$SELF" _supervise < /dev/null > /dev/null 2>&1 &
}

stop() {
	if running; then
		say "stopping"
		kill $(cat $PIDFILE) 2>/dev/null
	fi
	rm -f $PIDFILE
	killall cps-agent > /dev/null 2>&1
}

enroll() {
	if [ -z "$1" -o -z "$2" ]; then
		echo "usage: $SELF enroll <server-url> <token> [site] [agent-id]"
		return 2
	fi
	stop
	umask 077
	printf "SERVER='%s'\nTOKEN='%s'\nSITE='%s'\nID='%s'\n" "$1" "$2" "$3" "$4" > $ENROLL
	# The agent keeps the server of an existing enrollment, so a new token starts from a
	# clean config. The supervisor puts the old one back if this enrollment fails.
	[ -f $CONF ] && mv -f $CONF $CONF.old
	say "enrolling with $1"
	start
	# Enrollment takes a few seconds: show how it went.
	i=0
	while [ $i -lt 20 ]; do
		sleep 1
		i=$((i + 1))
		[ -f $CONF -a ! -f $CONF.old -a ! -s $ENROLL ] && grep -q '"token"' $CONF && break
		running || break
	done
	tail -5 $LOG
}

status() {
	if ! running; then
		echo "agent:      not running"
	elif pid=$(pidof cps-agent); then
		echo "agent:      running (pid $pid)"
	else
		echo "agent:      waiting for a printer, retrying every 30s"
	fi
	echo "version:    $($BIN -version 2>/dev/null | awk '{print $2}')"
	if [ -n "$(conf_str token)" ]; then
		echo "enrolled:   $(conf_str agent_id) site '$(conf_str site)' on $(conf_str server_url)"
	else
		echo "enrolled:   no"
	fi
	HOST=
	[ -f $SETTINGS ] && . $SETTINGS
	echo "dialed at:  ${HOST:-$(nvram get lan_ipaddr)}"
	echo "usb:        $(ls /dev/usb/lp* 2>/dev/null | tr '\n' ' ')"
	for p in u2ec lpd; do
		pidof $p > /dev/null 2>&1 && echo "warning:    the router's printer server ($p) is running and may hold the printer"
	done
	grep -q "$SELF" $HOOK 2>/dev/null && echo "autostart:  on" || echo "autostart:  off"
}

autostart() {
	case $1 in
	on)
		if ! uname -o 2>/dev/null | grep -qi merlin; then
			echo "This firmware does not run /jffs/scripts (Asuswrt-Merlin only):"
			echo "start the agent with '$SELF start' after each reboot."
			return 1
		fi
		mkdir -p /jffs/scripts
		[ -f $HOOK ] || printf '#!/bin/sh\n' > $HOOK
		grep -q "$SELF" $HOOK || echo "$SELF start & # cps-agent" >> $HOOK
		chmod 755 $HOOK
		if [ "$(nvram get jffs2_scripts)" != "1" ]; then
			nvram set jffs2_scripts=1
			nvram commit
			echo "turned on JFFS custom scripts (nvram jffs2_scripts=1)"
		fi
		echo "autostart on: $HOOK"
		;;
	off)
		[ -f $HOOK ] && sed -i "\#$SELF#d" $HOOK
		echo "autostart off"
		;;
	*)
		echo "usage: $SELF autostart on|off"
		return 2
		;;
	esac
}

case $1 in
enroll) shift; enroll "$@" ;;
start) start ;;
stop) stop ;;
restart) stop; start ;;
status) status ;;
log) tail -50 $LOG ;;
autostart) autostart $2 ;;
uninstall)
	stop
	autostart off
	rm -f $ENROLL $LOG
	# agent.json holds the agent's credential: don't leave it behind.
	rm -rf "$DIR"
	echo "removed $DIR"
	;;
_supervise) supervise ;;
*)
	sed -n '2,15s/^# \{0,1\}//p' "$SELF"
	exit 2
	;;
esac
