'use strict';
'require ui';
'require rpc';
'require form';
'require network';

// proto vkturn: WireGuard over VK's TURN relays (vk-turn-proxyd). The options
// are the protocol handler's (/lib/netifd/proto/vkturn.sh).

const callGenerateKeyPair = rpc.declare({
	object: 'vkturn',
	method: 'generateKeyPair',
	expect: { keys: {} }
});

const callGetPublicKey = rpc.declare({
	object: 'vkturn',
	method: 'getPublicKey',
	params: [ 'privkey' ],
	expect: { pubkey: '' }
});

function validateKey(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	if (value.length != 44 || !value.match(/^[A-Za-z0-9+\/]{43}=$/))
		return _('Invalid Base64 key string');
	return true;
}

function validateHostPort(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	const m = value.match(/^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+):(\d{1,5})$/);
	if (!m || +m[2] < 1 || +m[2] > 65535)
		return _('Expecting host:port');
	return true;
}

function validateVKLink(section_id, value) {
	if (value == null || value.length == 0)
		return true;
	if (!value.match(/^https:\/\/(vk\.com|vk\.ru|m\.vk\.com|m\.vk\.ru)\/call\/join\/[A-Za-z0-9_-]+\/?$/))
		return _('Expecting a VK call link: https://vk.com/call/join/…');
	return true;
}

// The client's public key: what goes into the server's [Peer].
const cbiPublicKey = form.DummyValue.extend({
	cfgvalue(section_id) {
		return null;
	},

	renderWidget(section_id) {
		const node = E('code', { 'id': 'vkturn-pubkey-' + section_id }, [ '—' ]);
		const priv = this.section.formvalue(section_id, 'private_key') || this.map.data.get(this.map.config, section_id, 'private_key');

		if (priv && validateKey(section_id, priv) === true)
			callGetPublicKey(priv).then(pub => { node.textContent = pub || '—'; });

		return node;
	}
});

const cbiKeyPairGenerate = form.DummyValue.extend({
	cfgvalue(section_id, value) {
		return E('button', {
			'class': 'btn',
			'click': ui.createHandlerFn(this, function(section_id, ev) {
				const prv = this.section.getUIElement(section_id, 'private_key');
				const node = document.getElementById('vkturn-pubkey-' + section_id);

				return callGenerateKeyPair().then(function(keypair) {
					prv.setValue(keypair.priv);
					if (node)
						node.textContent = keypair.pub;
				});
			}, section_id)
		}, [ _('Generate new key pair') ]);
	}
});

return network.registerProtocol('vkturn', {
	getI18n() {
		return _('VK TURN (WireGuard)');
	},

	getIfname() {
		return this._ubus('l3_device') || this.sid;
	},

	getPackageName() {
		return 'vk-turn-proxy';
	},

	isFloating() {
		return true;
	},

	isVirtual() {
		return true;
	},

	getDevices() {
		return null;
	},

	containsDevice(ifname) {
		return (network.getIfnameOf(ifname) == this.getIfname());
	},

	renderFormOptions(s) {
		let o;

		o = s.taboption('general', form.Value, 'peer', _('Server'),
			_('The server\'s SRTP listener (vk-turn-proxy server, <code>-srtp</code>): <code>host:port</code>.'));
		o.rmempty = false;
		o.validate = validateHostPort;
		o.placeholder = '203.0.113.10:56004';

		o = s.taboption('general', form.Value, 'public_key', _('Server public key'),
			_('The WireGuard public key of the server.'));
		o.rmempty = false;
		o.validate = validateKey;

		o = s.taboption('general', form.Value, 'private_key', _('Private key'),
			_('The WireGuard private key of this router.'));
		o.password = true;
		o.rmempty = false;
		o.validate = validateKey;

		s.taboption('general', cbiPublicKey, '_public_key', _('Public key'),
			_('Add it as a peer on the server: <code>wg set &lt;iface&gt; peer &lt;key&gt; allowed-ips &lt;address&gt;/32</code>.'));
		s.taboption('general', cbiKeyPairGenerate, '_gen_keypair', ' ');

		o = s.taboption('general', form.Value, 'preshared_key', _('Preshared key'),
			_('Optional. A WireGuard preshared key shared with the server.'));
		o.password = true;
		o.validate = validateKey;

		o = s.taboption('general', form.DynamicList, 'addresses', _('IP addresses'),
			_('The tunnel address of this router, with the prefix (e.g. <code>10.66.66.2/24</code>).'));
		o.datatype = 'ipaddr';
		o.rmempty = false;

		o = s.taboption('general', form.DynamicList, 'vk_link', _('VK call links'),
			_('A VK call link the TURN credentials are minted from. The anonymous mode uses the first one.'));
		o.validate = validateVKLink;
		o.rmempty = false;

		o = s.taboption('general', form.Value, 'conns', _('Connections'),
			_('TURN allocations, 1…120. Up to 20 per call link are known to work.'));
		o.datatype = 'range(1,120)';
		o.placeholder = '20';

		o = s.taboption('general', form.ListValue, 'turn_transport', _('Transport to the VK relay'));
		o.value('udp', 'UDP');
		o.value('tcp', 'TCP');
		o.default = 'udp';

		o = s.taboption('advanced', form.Value, 'mtu', _('MTU'),
			_('1000…1400, 1280 by default.'));
		o.datatype = 'range(1000,1400)';
		o.placeholder = '1280';

		o = s.taboption('advanced', form.Value, 'keepalive', _('Keepalive'),
			_('WireGuard persistent keepalive, seconds.'));
		o.datatype = 'range(0,3600)';
		o.placeholder = '25';

		o = s.taboption('advanced', form.Value, 'down_after', _('Down after'),
			_('Seconds without a live session before the interface goes down (and comes back up by itself).'));
		o.datatype = 'range(5,86400)';
		o.placeholder = '60';

		o = s.taboption('advanced', form.Value, 'turn_server', _('TURN server'),
			_('Optional. A TURN relay of your choice instead of VK\'s: <code>host:port</code>.'));
		o.validate = validateHostPort;

		o = s.taboption('advanced', form.Value, 'pool_reserve', _('Credential reserve'),
			_('Reserve credential sets. One set rides a network change without minting.'));
		o.datatype = 'range(0,10)';
		o.placeholder = '1';

		o = s.taboption('advanced', form.Value, 'uplink_pace', _('Uplink pacer'),
			_('KiB/s per connection, 0 = off.'));
		o.datatype = 'uinteger';
		o.placeholder = '0';

		o = s.taboption('advanced', form.Value, 'cred_cache', _('Credential cache'),
			_('Where the TURN credentials are kept between restarts. On flash it survives a reboot; under <code>/var/run</code> it does not. Default: <code>/etc/vkturn/&lt;interface&gt;/creds.json</code>.'));
		o.placeholder = '/etc/vkturn/<interface>/creds.json';

		o = s.taboption('advanced', form.Value, 'vk_cookie_file', _('VK login cookies'),
			_('Optional. A browser\'s cookies.txt of a VK login: the logged-in mode.'));

		o = s.taboption('advanced', form.Value, 'fwmark', _('Firewall mark'),
			_('Optional. A mark on the proxy\'s own sockets, so that no policy routing catches them. <code>0x200000</code> is the one NetShift lets pass.'));
		o.validate = function(section_id, value) {
			if (value == null || value.length == 0)
				return true;
			if (!value.match(/^(0x[0-9a-fA-F]{1,8}|[0-9]{1,10})$/) || +value > 4294967295)
				return _('Expecting a hexadecimal (0x…) or decimal mark');
			return true;
		};

		o = s.taboption('advanced', form.Value, 'bind_device', _('Bind to'),
			_('Optional. Bind the proxy\'s own sockets to this device or interface (e.g. <code>wan</code>).'));

		o = s.taboption('advanced', form.Value, 'dns_server', _('DNS server'),
			_('Optional. Resolve VK\'s hosts with this server instead of the router\'s resolver.'));
		o.datatype = 'or(ipaddr,ipaddrport(1))';

		o = s.taboption('advanced', form.ListValue, 'log_level', _('Log level'));
		o.value('info', _('Info'));
		o.value('debug', _('Debug'));
		o.default = 'info';

		o = s.taboption('advanced', form.Value, 'stats_every', _('Statistics interval'),
			_('Seconds between statistics lines in the log, 0 = none.'));
		o.datatype = 'range(0,86400)';
		o.placeholder = '0';
	}
});
