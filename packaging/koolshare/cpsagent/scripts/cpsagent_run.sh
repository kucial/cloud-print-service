#!/bin/sh
# Supervisor for cps-agent: restarts it when it exits and keeps its log, which lives in
# RAM, small. Started by cpsagent_config.sh; stopped with SIGTERM.
source /koolshare/scripts/base.sh
eval $(dbus export cpsagent_)

BIN=/koolshare/bin/cps-agent
CONF=/koolshare/configs/cpsagent/agent.json
LOG=/tmp/upload/cpsagent_log.txt
PIDFILE=/var/run/cpsagent.pid
ENROLL=/tmp/cpsagent.enroll
LOG_MAX=262144

log() { echo "$(date '+%F %T') [plugin] $*" >> $LOG; }

# The agent appends (>>), so the file can be cut in place while it runs.
trim_log() {
	[ "$(wc -c < $LOG)" -gt $LOG_MAX ] || return 0
	tail -c $((LOG_MAX / 2)) $LOG > $LOG.tmp
	cat $LOG.tmp > $LOG
	rm -f $LOG.tmp
}

child=
trap '[ -n "$child" ] && kill $child 2>/dev/null; rm -f $PIDFILE; exit 0' TERM INT
echo $$ > $PIDFILE

while :; do
	# The router's LAN address unless the page sets another: the agent would otherwise
	# pick the first interface it finds, which on a router can be the WAN.
	set -- -config $CONF -host "${cpsagent_host:-$(nvram get lan_ipaddr)}"
	[ -n "${cpsagent_base_port}" ] && set -- "$@" -listen-base "${cpsagent_base_port}"
	enrolling=
	if [ -s $ENROLL ]; then
		token=$(cat $ENROLL)
		rm -f $ENROLL
		enrolling=1
		set -- "$@" -enroll "$token"
		# Agent id and site are fixed at enrollment.
		[ -n "${cpsagent_id}" ] && set -- "$@" -id "${cpsagent_id}"
		[ -n "${cpsagent_site}" ] && set -- "$@" -site "${cpsagent_site}"
	elif [ ! -f $CONF ]; then
		log "not enrolled: enter an enrollment token on the plugin page"
		break
	fi

	CPS_AGENT_SERVER="${cpsagent_server}" $BIN "$@" >> $LOG 2>&1 &
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
	else
		# It also exits when it finds no printer: retrying picks one up once plugged in.
		log "cps-agent exited (exit $rc), restarting in 30s"
		sleep 30 &
		wait $!
	fi
done
rm -f $PIDFILE
