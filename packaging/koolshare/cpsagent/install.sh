#!/bin/sh
# Koolshare software center installer for cps-agent. The software center unpacks
# cpsagent.tar.gz to /tmp/cpsagent and runs this script from there.
source /koolshare/scripts/base.sh
DIR=$(cd $(dirname $0); pwd)
module=${DIR##*/}
TITLE="Cloud Print Agent"
DESCR="Serves USB printers to Cloud Print Service"

log() { echo "$(date '+%F %T') $*"; }

exit_install() {
	rm -rf /tmp/${module}* >/dev/null 2>&1
	exit $1
}

# Same gate as the rogsoft plugins: koolshare firmware on an hnd/axhnd/axhnd.675x kernel.
platform_test() {
	local LINUX_VER=$(uname -r | awk -F"." '{print $1$2}')
	if [ ! -d "/koolshare" -o ! -f "/usr/bin/skipd" -o "${LINUX_VER}" -lt "41" ]; then
		log "This plugin is for koolshare firmware on hnd/axhnd/axhnd.675x routers."
		exit_install 1
	fi
	# One ARMv7 build serves both: the 64-bit models run a 32-bit userland.
	case $(uname -m) in
	armv7l | aarch64) ;;
	*)
		log "Unsupported CPU: $(uname -m)"
		exit_install 1
		;;
	esac
}

install_now() {
	local PLVER=$(cat ${DIR}/version)
	local ENABLE=$(dbus get ${module}_enable)

	if [ -f /koolshare/scripts/${module}_config.sh ]; then
		log "Stopping the running agent before the upgrade"
		sh /koolshare/scripts/${module}_config.sh stop >/dev/null 2>&1
	fi
	find /koolshare/init.d/ -name "*${module}*" | xargs rm -rf >/dev/null 2>&1

	log "Installing files"
	cp -f ${DIR}/bin/cps-agent /koolshare/bin/cps-agent
	cp -rf ${DIR}/res/* /koolshare/res/
	cp -rf ${DIR}/scripts/* /koolshare/scripts/
	cp -rf ${DIR}/webs/* /koolshare/webs/
	cp -f ${DIR}/uninstall.sh /koolshare/scripts/uninstall_${module}.sh
	chmod 755 /koolshare/bin/cps-agent /koolshare/scripts/${module}_*.sh /koolshare/scripts/uninstall_${module}.sh
	mkdir -p /koolshare/configs/${module}
	chmod 700 /koolshare/configs/${module}

	# Started by the software center on boot and WAN up.
	ln -sf /koolshare/scripts/${module}_config.sh /koolshare/init.d/S99${module}.sh

	dbus set ${module}_version="${PLVER}"
	dbus set softcenter_module_${module}_version="${PLVER}"
	dbus set softcenter_module_${module}_install="1"
	dbus set softcenter_module_${module}_name="${module}"
	dbus set softcenter_module_${module}_title="${TITLE}"
	dbus set softcenter_module_${module}_description="${DESCR}"

	if [ "${ENABLE}" = "1" ]; then
		log "Restarting the agent"
		sh /koolshare/scripts/${module}_config.sh start
	fi
	log "${TITLE} ${PLVER} installed"
	exit_install 0
}

platform_test
install_now
