#!/bin/sh
# vk-turn-proxyd's hook to netifd, as pppd's ppp-up / ppp-down:
#
#   vkturn-notify.sh up    the tunnel carries traffic: the link is up, the
#                          interface's addresses go on the device (netifd adds
#                          the subnet routes, and the default route only if
#                          the interface asks for one: defaultroute 1; it is
#                          off unless set)
#   vkturn-notify.sh down  nothing carried for down_after seconds: the link is
#                          down, netifd takes the addresses and routes off; the
#                          daemon keeps trying and says "up" again
#
# INTERFACE (the logical interface) and IFNAME (the tun device, the same
# name) come from the daemon.
#
# shellcheck disable=SC1091

[ -n "$INTERFACE" ] && [ -n "$IFNAME" ] || exit 1

. /lib/functions.sh
. /lib/netifd/netifd-proto.sh

case "$1" in
up)
	config_load network
	addresses=
	config_get addresses "$INTERFACE" addresses
	config_get_bool defaultroute "$INTERFACE" defaultroute 0
	proto_init_update "$IFNAME" 1
	for address in $addresses; do
		case "$address" in
		*:*/*) proto_add_ipv6_address "${address%%/*}" "${address##*/}" ;;
		*.*/*) proto_add_ipv4_address "${address%%/*}" "${address##*/}" ;;
		*:*) proto_add_ipv6_address "$address" 128 ;;
		*.*) proto_add_ipv4_address "$address" 32 ;;
		esac
	done
	# Only on request: the tunnel takes no traffic but what NetShift sends
	# into it. netifd applies metric and ip4table to this route as usual.
	[ "$defaultroute" = 1 ] && proto_add_ipv4_route 0.0.0.0 0
	proto_send_update "$INTERFACE"
	;;
down)
	proto_init_update "$IFNAME" 0
	proto_send_update "$INTERFACE"
	;;
*)
	echo "usage: $0 up|down" >&2
	exit 2
	;;
esac
