#!/bin/bash

[ -n "$DEBUG" ] && set -o xtrace
set -o nounset
set -o errexit
shopt -s nullglob

## Description  : create specify bridge for specify network interface
## Autor        : lvguanglin
## Modified Time: 2015/06/23
IFNAME=${GARDEN_HOST_IFNAME:-}
BRNAME=${GARDEN_HOST_BRNAME:-}

function check_specify_if() {
	if [ -d "/sys/class/net/${IFNAME}" -a ! -d "/sys/class/net/${IFNAME}/bridge" ]; then
		IFMAC=$(cat /sys/class/net/${IFNAME}/address)
		IFMTU=$(cat /sys/class/net/${IFNAME}/mtu)
		IFADDRV4=$(ip addr show ${IFNAME}|awk '/inet /{print $2}')
		IFIPV4=${IFADDRV4%%/*}
		IFADDRV6=$(ip addr show ${IFNAME}|awk '/inet6 /{print $2}')
		IFIPV6=${IFADDRV6%%/*}
		#IFCIDRV4=${IFADDRV4#*/}
		#IFCIDRV4=${IFCIDRV4%%/*}
		IFROUTESPEC=$(ip route|grep "$IFNAME"|awk '/default via .* dev /')
		IFROUTESPEC=${IFROUTESPEC#*${IFNAME} }
		IFROUTEGW=$(ip route|grep "$IFNAME"|awk '/default via .* dev /{print $3}')
	fi
}

function init_bridge_with_specify_if() {
	if [ ! -d "/sys/class/net/${BRNAME}" ]; then
		check_specify_if
		if [ -z $IFADDRV4 ]; then
			echo "${IFNAME} - IFADDRV4: ${IFADDRV4}" >&2
			exit 1
		fi
		(ip link add dev $BRNAME type bridge > /dev/null 2>&1) || (brctl addbr $BRNAME)
		ip addr add $IFADDRV4 br + dev $BRNAME
		ip addr del $IFADDRV4 dev $IFNAME
		
		[ "$IFADDRV6" ] && {
			ip addr add $IFADDRV6 dev $BRNAME
			ip addr del $IFADDRV6 dev $IFNAME
		} || true #just in case
		(ip link set $IFNAME master $BRNAME > /dev/null 2>&1) || (brctl addif $BRNAME $IFNAME)
		[ "$IFMTU" ] && {
			ip link set $BRNAME mtu $IFMTU
		} || true #just in case
		[ "$IFMAC" ] && {
			ip link set "$BRNAME" address "$IFMAC"
		} || true #just in case
		ip link set $IFNAME up
		ip link set $BRNAME up
		[ "$IFROUTEGW" ] && {
			#echo "IFROUTEGW - '$IFROUTEGW',IFROUTESPEC - '$IFROUTESPEC'" 
			ip route add default via $IFROUTEGW dev $BRNAME $IFROUTESPEC
		} || true #just in case
		#echo "${BRNAME} configured with ${IFNAME}"
	fi
}

function setup_bridge() {
	[ $IFNAME ] && [ $BRNAME ] && {
		init_bridge_with_specify_if
	} || true #just in case
}

case "${1}" in
  setup)
    setup_bridge

    ;;
  *)
    echo "Unknown command: ${1}" 1>&2
    exit 1
    ;;
esac
