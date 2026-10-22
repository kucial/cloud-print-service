#!/bin/sh
# Starts and stops cps-agent. Called two ways:
#   at boot / WAN up, through /koolshare/init.d/S99cpsagent.sh:  start | stop | restart
#   from Module_cpsagent.asp through /_api/:                     <request id> apply | reset
# The page saves its fields to dbus (cpsagent_*) before this script runs.
source /koolshare/scripts/base.sh
eval $(dbus export cpsagent_)

CONF=/koolshare/configs/cpsagent/agent.json
LOG=/tmp/upload/cpsagent_log.txt
PIDFILE=/var/run/cpsagent.pid
ENROLL=/tmp/cpsagent.enroll

log() { echo "$(date '+%F %T') [plugin] $*" >> $LOG; }

running() {
	[ -f $PIDFILE ] && kill -0 $(cat $PIDFILE) 2>/dev/null
}

start_agent() {
	[ "${cpsagent_enable}" = "1" ] || return 0
	running && return 0
	if [ ! -f $CONF -a ! -s $ENROLL ]; then
		log "not enrolled: enter an enrollment token on the plugin page"
		return 1
	fi
	log "starting"
	# The supervisor writes its own pid file and keeps cps-agent running.
	nohup sh /koolshare/scripts/cpsagent_run.sh >/dev/null 2>&1 &
}

stop_agent() {
	if running; then
		log "stopping"
		kill $(cat $PIDFILE) 2>/dev/null
	fi
	rm -f $PIDFILE
	killall cps-agent >/dev/null 2>&1
}

# Takes a token entered on the page out of dbus, where it would otherwise stay, and
# leaves it for the supervisor to use once.
take_enroll_token() {
	[ -n "${cpsagent_enroll}" ] || return 0
	umask 077
	printf '%s' "${cpsagent_enroll}" > $ENROLL
	dbus remove cpsagent_enroll
	# The agent keeps the server of an existing enrollment, so a new token starts from a
	# clean config. The supervisor puts the old one back if this enrollment fails.
	[ -f $CONF ] && mv -f $CONF $CONF.old
	log "enrolling with ${cpsagent_server:-the default server}"
}

case $1 in
start)
	start_agent
	;;
stop)
	stop_agent
	;;
restart)
	stop_agent
	start_agent
	;;
*)
	# web request: $1 is the request id, $2 the action
	case $2 in
	reset)
		stop_agent
		rm -f $CONF $CONF.old $ENROLL
		dbus set cpsagent_enable=0
		log "enrollment removed"
		;;
	*)
		stop_agent
		take_enroll_token
		start_agent
		;;
	esac
	http_response "$1"
	;;
esac
