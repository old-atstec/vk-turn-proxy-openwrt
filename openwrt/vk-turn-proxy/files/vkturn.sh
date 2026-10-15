#!/bin/sh
# netifd protocol handler: proto vkturn — WireGuard over VK's TURN relays
# (vk-turn-proxyd). The daemon creates the tun device named after the
# interface; this handler hands it its config and starts it, and
# vkturn-notify.sh puts the addresses on the device once the tunnel carries
# traffic (and takes the link down when it stops).
#
# shellcheck disable=SC1091,SC2034,SC3043

[ -n "$INCLUDE_ONLY" ] || {
	. /lib/functions.sh
	. ../netifd-proto.sh
	init_proto "$@"
}

VKTURN_BIN=/usr/sbin/vk-turn-proxyd
VKTURN_RUN=/var/run/vkturn

proto_vkturn_init_config() {
	proto_config_add_string "peer"
	proto_config_add_string "private_key"
	proto_config_add_string "public_key"
	proto_config_add_string "preshared_key"
	proto_config_add_array "addresses"
	proto_config_add_int "mtu"
	proto_config_add_int "keepalive"
	proto_config_add_array "vk_link"
	proto_config_add_int "conns"
	proto_config_add_string "turn_transport"
	proto_config_add_string "turn_server"
	proto_config_add_int "pool_reserve"
	proto_config_add_int "uplink_pace"
	proto_config_add_boolean "spread_relays"
	proto_config_add_string "cpu_affinity"
	proto_config_add_string "vk_cookie_file"
	proto_config_add_string "cred_cache"
	proto_config_add_string "fwmark"
	proto_config_add_string "bind_device"
	proto_config_add_string "dns_server"
	proto_config_add_int "down_after"
	proto_config_add_int "stats_every"
	proto_config_add_string "log_level"
	proto_config_add_boolean "simulate_captcha"
	available=1
	no_device=1
	no_proto_task=0
}

vkturn_fail() {
	local config="$1"; shift
	logger -t vkturn "$config: $*"
	proto_notify_error "$config" "$1"
	proto_block_restart "$config"
}

# vkturn_line key value: one key=value line, refused if the value holds a
# newline (it would add a key of its own).
vkturn_line() {
	case "$2" in
	*"
"*) return 1 ;;
	esac
	[ -z "$2" ] || printf '%s=%s\n' "$1" "$2"
}

proto_vkturn_setup() {
	local config="$1"
	local dir="$VKTURN_RUN/$config"
	local cfg="$dir/config"

	local peer private_key public_key preshared_key mtu keepalive conns \
		turn_transport turn_server pool_reserve uplink_pace spread_relays cpu_affinity vk_cookie_file \
		cred_cache fwmark bind_device dns_server down_after stats_every \
		log_level simulate_captcha
	json_get_vars peer private_key public_key preshared_key mtu keepalive conns \
		turn_transport turn_server pool_reserve uplink_pace spread_relays cpu_affinity vk_cookie_file \
		cred_cache fwmark bind_device dns_server down_after stats_every \
		log_level simulate_captcha

	if [ "${#config}" -gt 15 ]; then
		vkturn_fail "$config" INVALID_NAME "the interface name is the tun device's: 15 characters at most"
		return 1
	fi
	[ -x "$VKTURN_BIN" ] || {
		vkturn_fail "$config" NO_DAEMON "$VKTURN_BIN is missing"
		return 1
	}
	[ -n "$peer" ] && [ -n "$private_key" ] && [ -n "$public_key" ] || {
		vkturn_fail "$config" MISSING_OPTION "peer, private_key and public_key are required"
		return 1
	}

	# bind_device may name a logical interface (wan) — the daemon wants the
	# device behind it.
	if [ -n "$bind_device" ] && [ ! -d "/sys/class/net/$bind_device" ]; then
		local l3
		. /lib/functions/network.sh
		network_get_device l3 "$bind_device" && bind_device="$l3"
	fi

	umask 077
	mkdir -p "$dir" || {
		vkturn_fail "$config" SETUP_FAILED "cannot make $dir"
		return 1
	}
	chmod 0700 "$VKTURN_RUN" "$dir"

	{
		vkturn_line iface "$config" &&
		vkturn_line peer "$peer" &&
		vkturn_line private_key "$private_key" &&
		vkturn_line public_key "$public_key" &&
		vkturn_line preshared_key "$preshared_key" &&
		vkturn_line mtu "$mtu" &&
		vkturn_line keepalive "$keepalive" &&
		vkturn_line conns "$conns" &&
		vkturn_line turn_transport "$turn_transport" &&
		vkturn_line turn_server "$turn_server" &&
		vkturn_line pool_reserve "$pool_reserve" &&
		vkturn_line uplink_pace "$uplink_pace" &&
		vkturn_line spread_relays "$spread_relays" &&
		vkturn_line cpu_affinity "$cpu_affinity" &&
		vkturn_line vk_cookie_file "$vk_cookie_file" &&
		vkturn_line cred_cache "${cred_cache:-/etc/vkturn/$config/creds.json}" &&
		vkturn_line fwmark "$fwmark" &&
		vkturn_line bind_device "$bind_device" &&
		vkturn_line dns_server "$dns_server" &&
		vkturn_line down_after "$down_after" &&
		vkturn_line stats_every "$stats_every" &&
		vkturn_line log_level "$log_level" &&
		vkturn_line simulate_captcha "$simulate_captcha" &&
		vkturn_line status_file "$dir/status.json" &&
		vkturn_line notify /lib/netifd/vkturn-notify.sh
	} > "$cfg.tmp" || {
		rm -f "$cfg.tmp"
		vkturn_fail "$config" INVALID_OPTION "an option holds a line break"
		return 1
	}
	json_for_each_item vkturn_add_link vk_link "$cfg.tmp"
	mv -f "$cfg.tmp" "$cfg"

	local err
	if ! err="$("$VKTURN_BIN" -config "$cfg" -check 2>&1)"; then
		vkturn_fail "$config" INVALID_OPTION "$err"
		return 1
	fi

	proto_run_command "$config" "$VKTURN_BIN" -config "$cfg"
}

# json_for_each_item hands the value, its index, then our file.
vkturn_add_link() {
	vkturn_line vk_link "$1" >> "$3"
}

proto_vkturn_teardown() {
	local config="$1"
	proto_kill_command "$config" 15
	rm -f "$VKTURN_RUN/$config/config"
}

[ -n "$INCLUDE_ONLY" ] || {
	add_protocol vkturn
}
