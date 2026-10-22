#!/bin/sh
# Writes the agent's state as JSON to /tmp/upload/cpsagent_status.txt, which the page
# reads as /_temp/cpsagent_status.txt.
source /koolshare/scripts/base.sh

CONF=/koolshare/configs/cpsagent/agent.json
PIDFILE=/var/run/cpsagent.pid
OUT=/tmp/upload/cpsagent_status.txt

# A string field from agent.json, still JSON-escaped, so it can be copied as is. The file
# is written by json.MarshalIndent: one field per line, two spaces in.
conf_str() {
	[ -f $CONF ] && sed -n "s/^  \"$1\": \"\(.*\)\",\{0,1\}\$/\1/p" $CONF
}

pid=$(pidof cps-agent)
running=0
# 1: the supervisor is up; the agent itself may be between restarts (pid empty).
[ -f $PIDFILE ] && kill -0 $(cat $PIDFILE) 2>/dev/null && running=1
enrolled=0
[ -n "$(conf_str token)" ] && enrolled=1
version=$(/koolshare/bin/cps-agent -version 2>/dev/null | awk '{print $2}')
usb=$(ls /dev/usb/lp* 2>/dev/null | tr '\n' ' ')
# Asus's own printer server (u2ec, LPD) keeps the USB printer open.
busy=
for p in u2ec lpd; do
	pidof $p >/dev/null 2>&1 && busy="$busy $p"
done

cat > $OUT.tmp <<EOF
{"running":$running,"pid":"$pid","version":"$version","enrolled":$enrolled,
"agent_id":"$(conf_str agent_id)","site":"$(conf_str site)","server":"$(conf_str server_url)",
"lan_ip":"$(nvram get lan_ipaddr)","usb":"${usb% }","busy":"${busy# }"}
EOF
mv -f $OUT.tmp $OUT
http_response "ok"
